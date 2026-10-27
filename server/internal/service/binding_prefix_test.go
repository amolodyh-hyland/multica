package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	dbfx "github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/db/bindingtx"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

func TestBindingPrefixCapturedQuickCreateConcurrentRetry(t *testing.T) {
	for _, mode := range []string{"failed_sweeper", "failure_transition"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			f, svc := seedDelegatedFailureFixture(t)
			fx := dbfx.New(f.pool, f.workspaceID, f.userID)
			status := "failed"
			if mode == "failure_transition" {
				status = "running"
			}
			parentID := f.insertWorkerTask(t, status, "comment", 1, 2)
			fx.Exec(t, `UPDATE agent_task_queue SET failure_reason='timeout' WHERE id=$1`, parentID)
			parent, err := svc.Queries.GetAgentTask(ctx, parentID)
			if err != nil {
				t.Fatal(err)
			}
			anchor := fx.Comment(t, f.workerIssue, "Create a sub-issue from this context")
			ws, user, agent, issue := util.MustParseUUID(f.workspaceID), util.MustParseUUID(f.userID), util.MustParseUUID(f.worker), util.MustParseUUID(f.workerIssue)
			build, err := BuildSourceContext(ctx, svc.Queries, ws, util.MustParseUUID(anchor))
			if err != nil {
				t.Fatal(err)
			}
			capture, err := PrepareSourceContextCapture(build, dbid.NewV7(), ws, user, time.Now(), nil)
			if err != nil {
				t.Fatal(err)
			}
			captureTx, err := bindingtx.Begin(ctx, f.pool, bindingtx.Workspaces(f.workspaceID))
			if err != nil {
				t.Fatal(err)
			}
			defer captureTx.Rollback(context.Background())
			if _, err := db.New(captureTx).LockIssueForDescriptionUpdate(ctx, db.LockIssueForDescriptionUpdateParams{ID: issue, WorkspaceID: ws}); err != nil {
				t.Fatal(err)
			}
			retryConn, err := f.pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer retryConn.Release()
			retrySvc := NewTaskService(db.NewBinding(retryConn), retryConn, nil, svc.Bus)
			retryDone := make(chan error, 1)
			go func() {
				if mode == "failed_sweeper" {
					task, err := retrySvc.MaybeRetryFailedTask(ctx, parent)
					if err == nil && task == nil {
						err = fmt.Errorf("retry was not created")
					}
					retryDone <- err
				} else {
					_, err := retrySvc.FailTask(ctx, parentID, "execution timed out", "", "", "", "timeout", false, "", "")
					retryDone <- err
				}
			}()
			if err := waitBindingPrefix(ctx, captureTx, retryConn.Conn().PgConn().PID()); err != nil {
				t.Fatal(err)
			}
			// Before the shared prefix, retry held this profile while waiting for
			// capture's issue. It must still be available to capture now.
			if _, err := captureTx.Exec(ctx, `SELECT id FROM agent WHERE id=$1 FOR UPDATE NOWAIT`, agent); err != nil {
				t.Fatal(err)
			}
			captureSvc := NewTaskService(svc.Queries, delegatedRecoveryLockedTxStarter{tx: captureTx}, nil, svc.Bus)
			task, captureErr := captureSvc.EnqueueQuickCreateTaskWithSourceContext(ctx, ws, user, agent, pgtype.UUID{}, "Create the follow-up", "medium", "", pgtype.UUID{}, pgtype.UUID{}, nil, capture)
			retryErr := <-retryDone
			if captureErr != nil || retryErr != nil {
				t.Fatalf("capture=%v retry=%v; want both atomic commits", captureErr, retryErr)
			}
			if n := fx.Count(t, `SELECT count(*) FROM issue_source_context WHERE id=$1 AND origin_task_id=$2 AND source_issue_id=$3`, capture.ID, task.ID, issue); n != 1 {
				t.Fatal("captured context was not durable")
			}
			if n := fx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE parent_task_id=$1 AND status='queued' AND runtime_id=$2`, parentID, f.runtimeID); n != 1 {
				t.Fatalf("durable retry count=%d want 1", n)
			}
			if n := fx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE id=$1 AND status='failed'`, parentID); n != 1 {
				t.Fatal("parent failure was not committed atomically with retry")
			}
		})
	}
}

func TestBindingPrefixStandaloneWrites(t *testing.T) {
	for _, mode := range []string{"exec", "one", "many"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			f, _ := seedDelegatedFailureFixture(t)
			fx := dbfx.New(f.pool, f.workspaceID, f.userID)
			status := "queued"
			if mode == "exec" {
				status = "cancelled"
			}
			taskID := f.insertWorkerTask(t, status, "comment", 1, 2)
			holder, err := bindingtx.Begin(ctx, f.pool, bindingtx.Workspaces(f.workspaceID))
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Rollback(context.Background())
			writer, err := f.pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Release()
			q := db.NewBinding(writer)
			done := make(chan error, 1)
			go func() {
				var err error
				switch mode {
				case "exec":
					err = q.SetAgentTaskErrorIfEmpty(ctx, db.SetAgentTaskErrorIfEmptyParams{ID: taskID, Error: pgtype.Text{String: "retained work", Valid: true}})
				case "one":
					_, err = q.UpdateAgentStatus(ctx, db.UpdateAgentStatusParams{ID: util.MustParseUUID(f.worker), Status: "working"})
				case "many":
					_, err = q.CancelAgentTasksByAgent(ctx, util.MustParseUUID(f.worker))
				}
				done <- err
			}()
			if err := waitBindingPrefix(ctx, holder, writer.Conn().PgConn().PID()); err != nil {
				t.Fatal(err)
			}
			if _, err := holder.Exec(ctx, `SELECT id FROM agent WHERE id=$1 FOR UPDATE NOWAIT`, f.worker); err != nil {
				t.Fatal(err)
			}
			if _, err := holder.Exec(ctx, `SELECT id FROM agent_task_queue WHERE id=$1 FOR UPDATE NOWAIT`, taskID); err != nil {
				t.Fatal(err)
			}
			if err := holder.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "exec":
				if n := fx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE id=$1 AND error='retained work'`, taskID); n != 1 {
					t.Fatal("exec write did not commit")
				}
			case "one":
				if n := fx.Count(t, `SELECT count(*) FROM agent WHERE id=$1 AND status='working'`, f.worker); n != 1 {
					t.Fatal("one-row write did not commit")
				}
			case "many":
				if n := fx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE id=$1 AND status='cancelled'`, taskID); n != 1 {
					t.Fatal("multi-row write did not commit")
				}
			}
		})
	}
}
