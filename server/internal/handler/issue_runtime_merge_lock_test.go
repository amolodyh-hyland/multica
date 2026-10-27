package handler

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/db/bindingtx"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func waitHandlerBindingPrefix(ctx context.Context, holder pgx.Tx, waitingPID uint32) error {
	for {
		var waiting bool
		if err := holder.QueryRow(ctx, `SELECT $1::int=ANY(pg_blocking_pids($2::int)) AND EXISTS (
   SELECT 1 FROM pg_locks WHERE pid=$2::int AND locktype='advisory' AND NOT granted
  )`, holder.Conn().PgConn().PID(), waitingPID).Scan(&waiting); err != nil {
			return err
		}
		if waiting {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestIssueRuntimeSwitchConcurrentLegacyMerge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	old := dbfx.Runtime(t, "old issue override")
	merged := dbfx.Runtime(t, "merged old runtime")
	shared := dbfx.Runtime(t, "shared profile default")
	next := dbfx.Runtime(t, "new issue override")
	agent := dbfx.Agent(t, "merge and override switch", shared)
	issue := dbfx.Issue(t, "merge override lock order", testutil.Cols{"assignee_type": "agent", "assignee_id": agent})
	setIssueRuntime(t, testHandler, issue, agent, old, 200)
	task := dbfx.Task(t, agent, testutil.Cols{"issue_id": issue, "runtime_id": old, "runtime_override_id": old})
	dbfx.Cleanup(t, `DELETE FROM issue_runtime_override WHERE issue_id=$1`, issue)
	tx, err := bindingtx.Begin(ctx, testHandler.TxStarter, bindingtx.Workspaces(testWorkspaceID))
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	mergeConn, err := testPool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer mergeConn.Release()
	h := *testHandler
	h.Queries, h.TxStarter = db.NewBinding(mergeConn), mergeConn
	mergeDone := make(chan error, 1)
	go func() {
		mergeDone <- h.mergeLegacyRuntime(ctx, parseUUID(merged), parseUUID(old), "merge-lock-order-test", "claude")
	}()
	if err := waitHandlerBindingPrefix(ctx, tx, mergeConn.Conn().PgConn().PID()); err != nil {
		t.Fatal(err)
	}
	q := db.New(tx)
	if _, err := q.LockAgentRuntimeForAccessChange(ctx, parseUUID(next)); err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetAgentForUpdate(ctx, parseUUID(agent)); err != nil {
		t.Fatal(err)
	}
	if _, err := q.LockIssueForRuntimeSwitch(ctx, db.LockIssueForRuntimeSwitchParams{IssueID: parseUUID(issue), WorkspaceID: parseUUID(testWorkspaceID)}); err != nil {
		t.Fatal(err)
	}
	if err := q.SetIssueRuntimeOverride(ctx, db.SetIssueRuntimeOverrideParams{IssueID: parseUUID(issue), WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(agent), RuntimeID: parseUUID(next), UpdatedBy: parseUUID(testUserID)}); err != nil {
		t.Fatal(err)
	}
	_, switchErr := q.MovePendingIssueAgentTasksToRuntime(ctx, db.MovePendingIssueAgentTasksToRuntimeParams{IssueID: parseUUID(issue), AgentID: parseUUID(agent), RuntimeID: parseUUID(next), RuntimeOverrideID: parseUUID(next)})
	if switchErr == nil {
		switchErr = tx.Commit(ctx)
	} else {
		_ = tx.Rollback(context.Background())
	}
	mergeErr := <-mergeDone
	if switchErr != nil || mergeErr != nil {
		t.Fatalf("switch=%v merge=%v; want both to commit", switchErr, mergeErr)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue_runtime_override WHERE issue_id=$1 AND runtime_id=$2`, issue, next); n != 1 {
		t.Fatal("legacy merge overwrote the new preference")
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE id=$1 AND runtime_id=$2 AND runtime_override_id=$2 AND status='queued'`, task, next); n != 1 {
		t.Fatal("retargeted queued attempt did not survive legacy merge")
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_runtime WHERE id=$1`, old); n != 0 {
		t.Fatal("legacy runtime survived merge")
	}
}

func TestIssueRuntimeEnqueueLocksAssignment(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shared := dbfx.Runtime(t, "assignment default")
	override := dbfx.Runtime(t, "assignment override")
	agent := dbfx.Agent(t, "enqueue assignment owner", shared)
	other := dbfx.Agent(t, "next assignment owner", shared)
	issue := dbfx.Issue(t, "stable enqueue assignment", testutil.Cols{"assignee_type": "agent", "assignee_id": agent})
	setIssueRuntime(t, testHandler, issue, agent, override, 200)
	dbfx.Cleanup(t, `DELETE FROM issue_runtime_override WHERE issue_id=$1`, issue)
	enqueueTx, err := testHandler.TxStarter.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer enqueueTx.Rollback(context.Background())
	task, err := db.New(enqueueTx).CreateAgentTask(ctx, db.CreateAgentTaskParams{AgentID: parseUUID(agent), IssueID: parseUUID(issue), RuntimeID: parseUUID(override)})
	if err != nil {
		t.Fatal(err)
	}
	assignmentTx, err := testHandler.TxStarter.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer assignmentTx.Rollback(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := assignmentTx.Exec(ctx, `UPDATE issue SET assignee_id=$2 WHERE id=$1`, issue, other)
		if err == nil {
			err = assignmentTx.Commit(ctx)
		}
		done <- err
	}()
	var observed bool
	var assignmentErr error
	finished := false
	for !observed && !finished && err == nil {
		select {
		case assignmentErr = <-done:
			finished = true
		default:
			err = enqueueTx.QueryRow(ctx, `SELECT $1::int=ANY(pg_blocking_pids($2::int))`, enqueueTx.Conn().PgConn().PID(), assignmentTx.Conn().PgConn().PID()).Scan(&observed)
			if !observed {
				time.Sleep(5 * time.Millisecond)
			}
		}
	}
	commitErr := enqueueTx.Commit(ctx)
	if !finished {
		assignmentErr = <-done
	}
	if !observed || err != nil || commitErr != nil || assignmentErr != nil {
		t.Fatalf("assignment changed before enqueue committed: blocked=%v observer=%v enqueue=%v assignment=%v", observed, err, commitErr, assignmentErr)
	}
	if task.RuntimeID != parseUUID(override) || task.RuntimeOverrideID != parseUUID(override) {
		t.Fatalf("enqueue lost consistent override authority: runtime=%v authority=%v", task.RuntimeID, task.RuntimeOverrideID)
	}
}

func TestBindingPrefixOrdinaryEnqueueConcurrentLegacyMerge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	old := dbfx.Runtime(t, "ordinary enqueue old runtime")
	destination := dbfx.Runtime(t, "ordinary enqueue merged runtime")
	agent := dbfx.Agent(t, "merge default profile", old)
	issue := dbfx.Issue(t, "enqueue and merge", testutil.Cols{"assignee_type": "agent", "assignee_id": agent})
	enqueueTx, err := bindingtx.Begin(ctx, testHandler.TxStarter, bindingtx.Workspaces(testWorkspaceID))
	if err != nil {
		t.Fatal(err)
	}
	defer enqueueTx.Rollback(context.Background())
	// Establish the first conflicting legacy ownership-fence row without
	// letting enqueue advance to the runtime yet.
	if _, err := enqueueTx.Exec(ctx, `SELECT id FROM agent WHERE id=$1 FOR KEY SHARE`, agent); err != nil {
		t.Fatal(err)
	}
	mergeConn, err := testPool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer mergeConn.Release()
	h := *testHandler
	h.Queries, h.TxStarter = db.NewBinding(mergeConn), mergeConn
	mergeDone := make(chan error, 1)
	go func() {
		mergeDone <- h.mergeLegacyRuntime(ctx, parseUUID(destination), parseUUID(old), "merge-enqueue-prefix-test", "claude")
	}()
	if err := waitHandlerBindingPrefix(ctx, enqueueTx, mergeConn.Conn().PgConn().PID()); err != nil {
		t.Fatal(err)
	}
	// Merge must not own the runtime while waiting for enqueue's profile.
	if _, err := enqueueTx.Exec(ctx, `SELECT id FROM agent_runtime WHERE id=$1 FOR UPDATE NOWAIT`, old); err != nil {
		t.Fatal(err)
	}
	task, err := db.New(enqueueTx).CreateAgentTask(ctx, db.CreateAgentTaskParams{AgentID: parseUUID(agent), IssueID: parseUUID(issue), RuntimeID: parseUUID(old)})
	if err != nil {
		t.Fatal(err)
	}
	enqueueErr := enqueueTx.Commit(ctx)
	mergeErr := <-mergeDone
	if enqueueErr != nil || mergeErr != nil {
		t.Fatalf("enqueue=%v merge=%v; want both commits", enqueueErr, mergeErr)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_runtime WHERE id=$1`, old); n != 0 {
		t.Fatal("old runtime survived merge")
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent WHERE id=$1 AND runtime_id=$2`, agent, destination); n != 1 {
		t.Fatal("profile did not move")
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE id=$1 AND runtime_id=$2 AND runtime_override_id IS NULL AND status='queued'`, task.ID, destination); n != 1 {
		t.Fatal("ordinary attempt was lost or gained override authority")
	}
}
