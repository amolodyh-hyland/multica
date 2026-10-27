package handler

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestAgentRuntimeSwitch(t *testing.T) {
	for _, status := range []string{"queued", "deferred", "dispatched", "running", "waiting_local_directory", "completed", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			oldRuntime := dbfx.Runtime(t, "switch source", testutil.Cols{"provider": "claude"})
			nextRuntime := dbfx.Runtime(t, "switch destination", testutil.Cols{"provider": "codex"})
			agentID := dbfx.Agent(t, "switch profile "+status, oldRuntime)
			taskID := dbfx.Task(t, agentID, testutil.Cols{"runtime_id": oldRuntime, "status": status, "session_id": "original-session", "work_dir": "/tmp/original-directory"})
			dbfx.Cleanup(t, `DELETE FROM activity_log WHERE details->>'agent_id' = $1`, agentID)
			active := status == "dispatched" || status == "running" || status == "waiting_local_directory"
			want := http.StatusOK
			if active {
				want = http.StatusConflict
			}
			testutil.Call(t, testHandler.UpdateAgent, testutil.WithURLParams(newRequest(http.MethodPatch, "/api/agents/"+agentID, map[string]any{
				"runtime_id": nextRuntime,
			}), "id", agentID)).Want(want)

			var bound, taskRuntime, taskStatus string
			dbfx.QueryRow(t, `SELECT runtime_id::text FROM agent WHERE id=$1`, agentID).Scan(&bound)
			dbfx.QueryRow(t, `SELECT runtime_id::text, status FROM agent_task_queue WHERE id=$1`, taskID).Scan(&taskRuntime, &taskStatus)
			wantBound, wantTaskRuntime := nextRuntime, oldRuntime
			if active {
				wantBound = oldRuntime
			} else if status == "queued" || status == "deferred" {
				wantTaskRuntime = nextRuntime
			}
			if bound != wantBound || taskRuntime != wantTaskRuntime || taskStatus != status {
				t.Fatalf("binding=%s task runtime=%s status=%s; want %s %s %s", bound, taskRuntime, taskStatus, wantBound, wantTaskRuntime, status)
			}
			var session, workDir *string
			var fresh bool
			dbfx.QueryRow(t, `SELECT session_id,work_dir,force_fresh_session FROM agent_task_queue WHERE id=$1`, taskID).Scan(&session, &workDir, &fresh)
			if status == "queued" || status == "deferred" {
				if session != nil || workDir != nil || !fresh {
					t.Fatalf("moved task retained session=%v workdir=%v fresh=%v", session, workDir, fresh)
				}
			} else if session == nil || *session != "original-session" || workDir == nil || *workDir != "/tmp/original-directory" {
				t.Fatal("unchanged task lost its native history")
			}
			count := dbfx.Count(t, `SELECT count(*) FROM activity_log WHERE action='agent_runtime_changed' AND details->>'agent_id'=$1`, agentID)
			if (active && count != 0) || (!active && count != 1) {
				t.Fatalf("audit rows=%d, active=%v", count, active)
			}
		})
	}
}

func TestAgentRuntimeSwitchNoopDuringRun(t *testing.T) {
	runtimeID := dbfx.Runtime(t, "unchanged runtime")
	agentID := dbfx.Agent(t, "unchanged profile", runtimeID)
	dbfx.Task(t, agentID, testutil.Cols{"runtime_id": runtimeID, "status": "running"})
	testutil.Call(t, testHandler.UpdateAgent, testutil.WithURLParams(newRequest(http.MethodPatch, "/api/agents/"+agentID,
		map[string]any{"runtime_id": runtimeID}), "id", agentID)).Want(http.StatusOK)
	if n := dbfx.Count(t, `SELECT count(*) FROM activity_log WHERE action='agent_runtime_changed' AND details->>'agent_id'=$1`, agentID); n != 0 {
		t.Fatalf("no-op wrote %d runtime-change activities", n)
	}
}

type runtimeSwitchAuditFailureStarter struct{}
type runtimeSwitchAuditFailureTx struct{ pgx.Tx }
type runtimeSwitchAuditFailureRow struct{}

func (runtimeSwitchAuditFailureStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := testPool.Begin(ctx)
	return runtimeSwitchAuditFailureTx{tx}, err
}

func (tx runtimeSwitchAuditFailureTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	if strings.Contains(query, "-- name: CreateActivity") {
		return runtimeSwitchAuditFailureRow{}
	}
	return tx.Tx.QueryRow(ctx, query, args...)
}

func (runtimeSwitchAuditFailureRow) Scan(...any) error {
	return errors.New("injected runtime audit failure")
}

func TestAgentRuntimeSwitchAuditFailureRollsBack(t *testing.T) {
	oldRuntime := dbfx.Runtime(t, "audit source")
	nextRuntime := dbfx.Runtime(t, "audit destination")
	agentID := dbfx.Agent(t, "audit failure profile", oldRuntime)
	taskID := dbfx.Task(t, agentID, testutil.Cols{"runtime_id": oldRuntime})
	h := *testHandler
	h.TxStarter = runtimeSwitchAuditFailureStarter{}
	testutil.Call(t, h.UpdateAgent, testutil.WithURLParams(newRequest(http.MethodPatch, "/api/agents/"+agentID,
		map[string]any{"runtime_id": nextRuntime}), "id", agentID)).Want(http.StatusInternalServerError)
	var bound, queued string
	dbfx.QueryRow(t, `SELECT runtime_id::text FROM agent WHERE id=$1`, agentID).Scan(&bound)
	dbfx.QueryRow(t, `SELECT runtime_id::text FROM agent_task_queue WHERE id=$1`, taskID).Scan(&queued)
	if bound != oldRuntime || queued != oldRuntime {
		t.Fatalf("failed audit changed profile=%s task=%s; want %s", bound, queued, oldRuntime)
	}
}

func TestAgentRuntimeSwitchPreservesConfirmedDelegation(t *testing.T) {
	oldRuntime := dbfx.Runtime(t, "delegation source")
	nextRuntime := dbfx.Runtime(t, "delegation destination")
	agentID := dbfx.Agent(t, "confirmed delegation profile", oldRuntime)
	taskID := dbfx.Task(t, agentID, testutil.Cols{"runtime_id": oldRuntime})
	dbfx.Insert(t, "public_delegation", testutil.Cols{
		"id":           dbid.NewV7(),
		"workspace_id": testWorkspaceID, "requester_id": testUserID,
		"idempotency_key": "runtime-switch-" + taskID, "payload_hash": strings.Repeat("a", 64),
		"agent_id": agentID, "runtime_id": oldRuntime, "task_id": taskID, "source": "{}",
	})
	testutil.Call(t, testHandler.UpdateAgent, testutil.WithURLParams(newRequest(http.MethodPatch, "/api/agents/"+agentID,
		map[string]any{"runtime_id": nextRuntime}), "id", agentID)).Want(http.StatusConflict)
	var bound string
	dbfx.QueryRow(t, `SELECT runtime_id::text FROM agent WHERE id=$1`, agentID).Scan(&bound)
	if bound != oldRuntime {
		t.Fatalf("confirmed delegation profile moved to %s", bound)
	}
}

func TestTaskEnqueueRacingRuntimeSwitchUsesLockedBinding(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	oldRuntime := dbfx.Runtime(t, "enqueue source")
	nextRuntime := dbfx.Runtime(t, "enqueue destination")
	agentID := dbfx.Agent(t, "racing enqueue profile", oldRuntime)
	issueID := dbfx.Issue(t, "racing enqueue issue")
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE agent_id=$1`, agentID)
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `UPDATE agent SET runtime_id=$1 WHERE id=$2`, nextRuntime, agentID); err != nil {
		t.Fatal(err)
	}
	enqueueConn, err := testPool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer enqueueConn.Release()
	applicationName := "runtime-switch-" + agentID
	if _, err := enqueueConn.Exec(ctx, `SELECT set_config('application_name',$1,false)`, applicationName); err != nil {
		t.Fatal(err)
	}
	defer enqueueConn.Exec(context.Background(), `RESET application_name`)
	type result struct {
		task db.AgentTaskQueue
		err  error
	}
	done := make(chan result, 1)
	go func() {
		task, err := db.New(enqueueConn).CreateAgentTask(ctx, db.CreateAgentTaskParams{
			AgentID: parseUUID(agentID), RuntimeID: parseUUID(oldRuntime), IssueID: parseUUID(issueID),
		})
		done <- result{task, err}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := testPool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND application_name=$1)`, applicationName).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("enqueue did not wait for the profile binding lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	out := <-done
	if out.err != nil {
		t.Fatal(out.err)
	}
	if out.task.RuntimeID != parseUUID(nextRuntime) {
		t.Fatalf("enqueued runtime=%s, want committed binding %s", uuidToString(out.task.RuntimeID), nextRuntime)
	}
}
