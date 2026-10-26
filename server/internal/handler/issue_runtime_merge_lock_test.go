package handler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type mergeOverrideProbeStarter struct {
	base    service.TxStarter
	reached chan struct{}
	resume  chan struct{}
}

func (s mergeOverrideProbeStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.base.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &mergeOverrideProbeTx{Tx: tx, reached: s.reached, resume: s.resume}, nil
}

type mergeOverrideProbeTx struct {
	pgx.Tx
	reached, resume chan struct{}
	paused          bool
}

func (tx *mergeOverrideProbeTx) pause(ctx context.Context) error {
	if !tx.paused {
		tx.paused = true
		close(tx.reached)
		select {
		case <-tx.resume:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (tx *mergeOverrideProbeTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if strings.Contains(sql, "-- name: LockIssueRuntimeOverridesByRuntime") {
		if err := tx.pause(ctx); err != nil {
			return nil, err
		}
	}
	return tx.Tx.Query(ctx, sql, args...)
}

func (tx *mergeOverrideProbeTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(sql, "-- name: ReassignIssueRuntimeOverrides") || strings.Contains(sql, "-- name: ReassignAgentsToRuntime") {
		if err := tx.pause(ctx); err != nil {
			return pgconn.CommandTag{}, err
		}
	}
	return tx.Tx.Exec(ctx, sql, args...)
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
	reached, resume := make(chan struct{}), make(chan struct{})
	h := *testHandler
	h.TxStarter = mergeOverrideProbeStarter{base: testHandler.TxStarter, reached: reached, resume: resume}
	mergeDone := make(chan error, 1)
	go func() {
		mergeDone <- h.mergeLegacyRuntime(ctx, parseUUID(merged), parseUUID(old), "merge-lock-order-test", "claude")
	}()
	select {
	case <-reached:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	tx, err := testHandler.TxStarter.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
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
	close(resume)
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
