package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	dbfx "github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/db/bindingtx"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestIssueRuntimeWakeupConfigurationRecoveryTeardownLockOrder(t *testing.T) {
	for _, action := range []string{"edit_instruction", "save", "enable", "trigger"} {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			f, svc := seedDelegatedFailureFixture(t)
			fx := dbfx.New(f.pool, f.workspaceID, f.userID)
			overrideID := fx.Runtime(t, "surviving wakeup override")
			fx.InsertNoID(t, "issue_runtime_override", dbfx.Cols{
				"workspace_id": f.workspaceID, "issue_id": f.issueID, "agent_id": f.coordinator,
				"runtime_id": overrideID, "updated_by": f.userID,
			}, "issue_id=$1", f.issueID)
			issueID, memberID := util.MustParseUUID(f.issueID), util.MustParseUUID(f.userID)
			wakeups := &IssueWakeupService{Tasks: svc}
			input := WakeupInput{AgentID: f.coordinator, Kind: "event", Mode: "continuous", EventTypes: []string{"comment.created"}, Instruction: "Check delegated work"}
			rule, err := wakeups.Create(ctx, issueID, memberID, pgtype.UUID{}, input)
			if err != nil {
				t.Fatal(err)
			}
			failedID := f.insertWorkerTask(t, "failed", "comment", 1, 2)
			fx.Exec(t, `UPDATE agent_task_queue SET completed_at=now() WHERE id=$1 OR id=$2`, failedID, f.sourceTask)

			editTx, err := bindingtx.Begin(ctx, f.pool, bindingtx.Workspaces(f.workspaceID))
			if err != nil {
				t.Fatal(err)
			}
			defer editTx.Rollback(context.Background())
			if _, err := db.New(editTx).LockWakeupIssue(ctx, issueID); err != nil {
				t.Fatal(err)
			}
			recoveryConn, err := f.pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer recoveryConn.Release()
			teardownConn, err := f.pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer teardownConn.Release()
			recoveryTasks := NewTaskService(db.NewBinding(recoveryConn), recoveryConn, nil, svc.Bus)
			recoveryDone, teardownDone := make(chan error, 1), make(chan error, 1)
			go func() {
				target, created, err := recoveryTasks.ensureDelegatedFailureRecoveryComment(ctx, failedID)
				if err == nil && (target == nil || !created) {
					err = fmt.Errorf("recovery signal missing: created=%v", created)
				}
				recoveryDone <- err
			}()
			go func() {
				tx, err := bindingtx.Begin(ctx, teardownConn, bindingtx.Workspaces(f.workspaceID))
				if err != nil {
					teardownDone <- err
					return
				}
				defer tx.Rollback(context.Background())
				q := db.New(tx)
				_, err = TeardownRuntime(ctx, q, util.MustParseUUID(f.runtimeID), RuntimeTeardownOptions{CancelNonTerminalTasks: true})
				if err == nil {
					err = q.DeleteAgentRuntime(ctx, util.MustParseUUID(f.runtimeID))
				}
				if err == nil {
					err = tx.Commit(ctx)
				}
				teardownDone <- err
			}()
			// Exactly three connections: both competing paths wait at the same
			// advisory prefix, before they can own a profile or failed task.
			setupErr := waitBindingPrefix(ctx, editTx, recoveryConn.Conn().PgConn().PID())
			if setupErr == nil {
				setupErr = waitBindingPrefix(ctx, editTx, teardownConn.Conn().PgConn().PID())
			}
			if setupErr == nil {
				_, setupErr = editTx.Exec(ctx, `SELECT id FROM agent WHERE id=$1 FOR UPDATE NOWAIT`, f.coordinator)
			}
			if setupErr == nil {
				_, setupErr = editTx.Exec(ctx, `SELECT id FROM agent_task_queue WHERE id=$1 FOR UPDATE NOWAIT`, failedID)
			}

			var editErr error
			if setupErr != nil {
				_ = editTx.Rollback(context.Background())
			} else {
				editTasks := NewTaskService(svc.Queries, delegatedRecoveryLockedTxStarter{tx: editTx}, nil, svc.Bus)
				editor := &IssueWakeupService{Tasks: editTasks}
				switch action {
				case "edit_instruction":
					editErr = editor.EditInstruction(ctx, issueID, rule.ID, memberID, WakeupInstructionInput{Instruction: "Check again", ExpectedInstruction: rule.Instruction, Revision: rule.Revision})
				case "save":
					_, editErr = editor.Save(ctx, issueID, memberID, pgtype.UUID{}, rule.ID, input)
				case "enable":
					_, editErr = editor.Enable(ctx, issueID, memberID, pgtype.UUID{}, rule.ID, WakeupEnableInput{Revision: rule.Revision})
				case "trigger":
					editErr = editor.Trigger(ctx, issueID, rule.ID, memberID)
				}
			}
			recoveryErr, teardownErr := <-recoveryDone, <-teardownDone
			if setupErr != nil || editErr != nil || recoveryErr != nil || teardownErr != nil {
				t.Fatalf("setup=%v configuration=%v recovery=%v teardown=%v; want all three to commit", setupErr, editErr, recoveryErr, teardownErr)
			}
			svc.TxStarter = f.pool
			if n := fx.Count(t, `SELECT count(*) FROM agent_runtime WHERE id=$1`, f.runtimeID); n != 0 {
				t.Fatal("default A survived teardown")
			}
			if n := fx.Count(t, `SELECT count(*) FROM issue_runtime_override WHERE issue_id=$1 AND runtime_id=$2`, issueID, overrideID); n != 1 {
				t.Fatal("override B did not survive teardown")
			}
			if n := fx.Count(t, `SELECT count(*) FROM comment WHERE source_task_id=$1 AND type=$2`, failedID, delegatedFailureRecoveryCommentType); n != 1 {
				t.Fatal("recovery signal was not durable")
			}
			if result, err := svc.RecoverPendingDelegatedFailures(ctx, 100); err != nil || result.Replayed != 1 {
				t.Fatalf("surviving override replay=%+v error=%v", result, err)
			}
			if n := fx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id=$1 AND agent_id=$2 AND runtime_id=$3 AND runtime_override_id=$3 AND status='queued'`, issueID, f.coordinator, overrideID); n != 1 {
				t.Fatalf("override B received %d recovery runs, want one", n)
			}
		})
	}
}

func TestIssueRuntimeFailureRetryConcurrentTeardown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	f, svc := seedDelegatedFailureFixture(t)
	fx := dbfx.New(f.pool, f.workspaceID, f.userID)
	overrideID := fx.Runtime(t, "surviving worker retry override")
	fx.InsertNoID(t, "issue_runtime_override", dbfx.Cols{
		"workspace_id": f.workspaceID, "issue_id": f.workerIssue, "agent_id": f.worker,
		"runtime_id": overrideID, "updated_by": f.userID,
	}, "issue_id=$1", f.workerIssue)
	fx.Exec(t, `UPDATE agent_task_queue SET completed_at=now() WHERE id=$1`, f.sourceTask)
	failedID := f.insertWorkerTask(t, "running", "comment", 1, 2)
	failureConn, err := f.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer failureConn.Release()
	teardownTx, err := bindingtx.Begin(ctx, f.pool, bindingtx.Workspaces(f.workspaceID))
	if err != nil {
		t.Fatal(err)
	}
	defer teardownTx.Rollback(context.Background())
	qtx, runtimeID := db.New(teardownTx), util.MustParseUUID(f.runtimeID)
	if _, err := qtx.LockAgentRuntime(ctx, runtimeID); err != nil {
		t.Fatal(err)
	}
	if _, err := qtx.ListUserAgentsByRuntimeForUpdate(ctx, runtimeID); err != nil {
		t.Fatal(err)
	}
	svc.Queries, svc.TxStarter = db.New(failureConn), failureConn
	failureDone := make(chan error, 1)
	go func() {
		_, err := svc.FailTask(ctx, failedID, "execution timed out", "", "", "", "timeout", false, "", "")
		failureDone <- err
	}()
	var setupErr error
	for {
		var waiting bool
		setupErr = teardownTx.QueryRow(ctx, `SELECT $1::int=ANY(pg_blocking_pids($2::int))`, teardownTx.Conn().PgConn().PID(), failureConn.Conn().PgConn().PID()).Scan(&waiting)
		if setupErr != nil || waiting {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	var teardownErr error
	if setupErr == nil {
		_, teardownErr = TeardownRuntime(ctx, qtx, runtimeID, RuntimeTeardownOptions{CancelNonTerminalTasks: true})
		if teardownErr == nil {
			teardownErr = qtx.DeleteAgentRuntime(ctx, runtimeID)
		}
		if teardownErr == nil {
			teardownErr = teardownTx.Commit(ctx)
		}
	}
	if setupErr != nil || teardownErr != nil {
		_ = teardownTx.Rollback(context.Background())
	}
	failureErr := <-failureDone
	if setupErr != nil {
		t.Fatalf("failed to establish retry lock wait: %v", setupErr)
	}
	if failureErr != nil || teardownErr != nil {
		t.Fatalf("failure/retry=%v teardown=%v; want both to commit", failureErr, teardownErr)
	}
	if n := fx.Count(t, `SELECT count(*) FROM agent_runtime WHERE id=$1`, f.runtimeID); n != 0 {
		t.Fatal("default A survived teardown")
	}
	if n := fx.Count(t, `SELECT count(*) FROM issue_runtime_override WHERE issue_id=$1 AND runtime_id=$2`, f.workerIssue, overrideID); n != 1 {
		t.Fatal("override B did not survive teardown")
	}
	if n := fx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE id=$1 AND status='cancelled' AND completed_at IS NOT NULL`, failedID); n != 1 {
		t.Fatal("teardown did not durably cancel the task before retry acquired its owners")
	}
}

func TestIssueRuntimeAdvisoryReadsDoNotLockProfile(t *testing.T) {
	f, svc := seedDelegatedFailureFixture(t)
	fx := dbfx.New(f.pool, f.workspaceID, f.userID)
	overrideID := fx.Runtime(t, "admission without profile lock")
	fx.InsertNoID(t, "issue_runtime_override", dbfx.Cols{
		"workspace_id": f.workspaceID, "issue_id": f.issueID, "agent_id": f.coordinator,
		"runtime_id": overrideID, "updated_by": f.userID,
	}, "issue_id=$1", f.issueID)
	fx.Exec(t, `UPDATE agent SET runtime_id=NULL WHERE id=$1`, f.coordinator)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	issueTx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer issueTx.Rollback(context.Background())
	q := db.New(issueTx)
	issue, err := q.LockWakeupIssue(ctx, util.MustParseUUID(f.issueID))
	if err != nil {
		t.Fatal(err)
	}
	profileTx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer profileTx.Rollback(context.Background())
	agent, err := db.New(profileTx).GetAgentForUpdate(ctx, util.MustParseUUID(f.coordinator))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveIssueAgentRuntime(ctx, q, issue.ID, agent)
	if err != nil || resolved.RuntimeID != util.MustParseUUID(overrideID) {
		t.Fatalf("issue admission blocked on locked profile or lost override: runtime=%v error=%v", resolved.RuntimeID, err)
	}
	target, err := resolveWakeTarget(ctx, q, issue)
	if err != nil || target.Agent.RuntimeID != util.MustParseUUID(overrideID) {
		t.Fatalf("system wakeup admission blocked on locked profile or lost override: runtime=%v error=%v", target.Agent.RuntimeID, err)
	}
	wakeups := &IssueWakeupService{Tasks: svc}
	if err := wakeups.authorize(ctx, q, issue.WorkspaceID, util.MustParseUUID(f.userID), issue.ID, agent); err != nil {
		t.Fatalf("wakeup admission blocked on locked profile or lost unbound-default override: %v", err)
	}
}

func TestIssueRuntimeRecoveryWaitsForPrefixBeforeTask(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f, svc := seedDelegatedFailureFixture(t)
	failedID := f.insertWorkerTask(t, "failed", "comment", 1, 2)
	issueTx, err := bindingtx.Begin(ctx, f.pool, bindingtx.Workspaces(f.workspaceID))
	if err != nil {
		t.Fatal(err)
	}
	defer issueTx.Rollback(context.Background())
	if _, err := db.New(issueTx).LockWakeupIssue(ctx, util.MustParseUUID(f.issueID)); err != nil {
		t.Fatal(err)
	}
	recoveryConn, err := f.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer recoveryConn.Release()
	svc.Queries, svc.TxStarter = db.New(recoveryConn), recoveryConn
	done := make(chan error, 1)
	go func() {
		target, created, err := svc.ensureDelegatedFailureRecoveryComment(ctx, failedID)
		if err == nil && (target == nil || !created) {
			err = fmt.Errorf("recovery signal missing")
		}
		done <- err
	}()
	for {
		var waiting bool
		err = issueTx.QueryRow(ctx, `SELECT $1::int=ANY(pg_blocking_pids($2::int))`, issueTx.Conn().PgConn().PID(), recoveryConn.Conn().PgConn().PID()).Scan(&waiting)
		if err != nil || waiting {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err == nil {
		_, err = issueTx.Exec(ctx, `SELECT id FROM agent_task_queue WHERE id=$1 FOR UPDATE NOWAIT`, failedID)
	}
	_ = issueTx.Rollback(context.Background())
	recoveryErr := <-done
	if err != nil || recoveryErr != nil {
		t.Fatalf("task must stay available while recovery waits on issue: task lock=%v recovery=%v", err, recoveryErr)
	}
}

func waitBindingPrefix(ctx context.Context, holder pgx.Tx, waitingPID uint32) error {
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

func TestIssueRuntimeOverrideSwitchConcurrentTeardown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	f, _ := seedDelegatedFailureFixture(t)
	fx := dbfx.New(f.pool, f.workspaceID, f.userID)
	defaultID := fx.Runtime(t, "worker shared default")
	nextID := fx.Runtime(t, "worker next issue override")
	fx.Exec(t, `UPDATE agent SET runtime_id=$2 WHERE id=$1`, f.worker, defaultID)
	fx.Exec(t, `UPDATE agent_task_queue SET completed_at=now() WHERE id=$1`, f.sourceTask)
	fx.InsertNoID(t, "issue_runtime_override", dbfx.Cols{
		"workspace_id": f.workspaceID, "issue_id": f.workerIssue, "agent_id": f.worker,
		"runtime_id": f.runtimeID, "updated_by": f.userID,
	}, "issue_id=$1", f.workerIssue)
	taskID := f.insertWorkerTask(t, "queued", "comment", 1, 2)
	fx.Exec(t, `UPDATE agent_task_queue SET runtime_override_id=$2 WHERE id=$1`, taskID, f.runtimeID)
	switchTx, err := bindingtx.Begin(ctx, f.pool, bindingtx.Workspaces(f.workspaceID))
	if err != nil {
		t.Fatal(err)
	}
	defer switchTx.Rollback(context.Background())
	teardownConn, err := f.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer teardownConn.Release()
	teardownDone := make(chan error, 1)
	go func() {
		tx, err := bindingtx.Begin(ctx, teardownConn, bindingtx.Workspaces(f.workspaceID))
		if err != nil {
			teardownDone <- err
			return
		}
		defer tx.Rollback(context.Background())
		q := db.New(tx)
		_, err = TeardownRuntime(ctx, q, util.MustParseUUID(f.runtimeID), RuntimeTeardownOptions{CancelNonTerminalTasks: true})
		if err == nil {
			err = q.DeleteAgentRuntime(ctx, util.MustParseUUID(f.runtimeID))
		}
		if err == nil {
			err = tx.Commit(ctx)
		}
		teardownDone <- err
	}()
	if err := waitBindingPrefix(ctx, switchTx, teardownConn.Conn().PgConn().PID()); err != nil {
		t.Fatal(err)
	}
	q := db.New(switchTx)
	if _, err := q.LockAgentRuntimeForAccessChange(ctx, util.MustParseUUID(nextID)); err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetAgentForUpdate(ctx, util.MustParseUUID(f.worker)); err != nil {
		t.Fatal(err)
	}
	if _, err := q.LockIssueForRuntimeSwitch(ctx, db.LockIssueForRuntimeSwitchParams{IssueID: util.MustParseUUID(f.workerIssue), WorkspaceID: util.MustParseUUID(f.workspaceID)}); err != nil {
		t.Fatal(err)
	}
	if err := q.SetIssueRuntimeOverride(ctx, db.SetIssueRuntimeOverrideParams{IssueID: util.MustParseUUID(f.workerIssue), WorkspaceID: util.MustParseUUID(f.workspaceID), AgentID: util.MustParseUUID(f.worker), RuntimeID: util.MustParseUUID(nextID), UpdatedBy: util.MustParseUUID(f.userID)}); err != nil {
		t.Fatal(err)
	}
	_, switchErr := q.MovePendingIssueAgentTasksToRuntime(ctx, db.MovePendingIssueAgentTasksToRuntimeParams{IssueID: util.MustParseUUID(f.workerIssue), AgentID: util.MustParseUUID(f.worker), RuntimeID: util.MustParseUUID(nextID), RuntimeOverrideID: util.MustParseUUID(nextID)})
	if switchErr == nil {
		switchErr = switchTx.Commit(ctx)
	} else {
		_ = switchTx.Rollback(context.Background())
	}
	teardownErr := <-teardownDone
	if switchErr != nil || teardownErr != nil {
		t.Fatalf("switch=%v teardown=%v; want both to commit", switchErr, teardownErr)
	}
	if n := fx.Count(t, `SELECT count(*) FROM agent_runtime WHERE id=$1`, f.runtimeID); n != 0 {
		t.Fatal("old override runtime survived deletion")
	}
	if n := fx.Count(t, `SELECT count(*) FROM issue_runtime_override WHERE issue_id=$1 AND runtime_id=$2`, f.workerIssue, nextID); n != 1 {
		t.Fatal("new preference was removed by old-runtime teardown")
	}
	if n := fx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE id=$1 AND runtime_id=$2 AND runtime_override_id=$2 AND status='queued'`, taskID, nextID); n != 1 {
		t.Fatal("retargeted queued attempt did not survive old-runtime teardown")
	}
}
