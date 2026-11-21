package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// taskRuntimes reads each task's runtime_id, keyed by task id.
func taskRuntimes(t *testing.T, taskIDs ...string) map[string]string {
	t.Helper()
	out := make(map[string]string, len(taskIDs))
	for _, id := range taskIDs {
		var runtimeID string
		if err := testPool.QueryRow(context.Background(),
			`SELECT runtime_id::text FROM agent_task_queue WHERE id = $1`, id).Scan(&runtimeID); err != nil {
			t.Fatalf("read task %s runtime: %v", id, err)
		}
		out[id] = runtimeID
	}
	return out
}

func patchAgentRuntime(t *testing.T, agentID, runtimeID string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodPatch, "/api/agents/"+agentID, map[string]any{
		"runtime_id": runtimeID,
	}), "id", agentID)
	testHandler.UpdateAgent(w, req)
	return w
}

func TestUpdateAgent_RuntimeSwitch_MovesPendingTasks(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	oldRuntimeID := dbfx.Runtime(t, "runtime-switch-tasks-old")
	newRuntimeID := dbfx.Runtime(t, "runtime-switch-tasks-new")
	agentID := dbfx.Agent(t, "runtime-switch-tasks-agent", oldRuntimeID)
	issueID := dbfx.Issue(t, "runtime switch pending tasks")
	queuedID := dbfx.Task(t, agentID, testutil.Cols{"runtime_id": oldRuntimeID, "issue_id": issueID})
	deferredID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": oldRuntimeID, "issue_id": issueID, "status": "deferred",
	})
	runningID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": oldRuntimeID, "issue_id": issueID, "status": "running",
		"dispatched_at": testutil.Raw("now()"), "started_at": testutil.Raw("now()"),
	})
	dispatchedID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": oldRuntimeID, "issue_id": dbfx.Issue(t, "dispatched during runtime switch"),
		"status": "dispatched", "dispatched_at": testutil.Raw("now()"),
	})
	waitingID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": oldRuntimeID, "issue_id": issueID, "status": "waiting_local_directory",
		"dispatched_at": testutil.Raw("now()"),
	})
	finishedID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": oldRuntimeID, "issue_id": issueID, "status": "completed",
		"completed_at": testutil.Raw("now()"),
	})

	w := patchAgentRuntime(t, agentID, newRuntimeID)
	if w.Code != http.StatusConflict {
		t.Fatalf("switch runtime with active tasks: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	agent, err := testHandler.Queries.GetAgent(context.Background(), parseUUID(agentID))
	if err != nil {
		t.Fatalf("read agent after rejected switch: %v", err)
	}
	if got := uuidToString(agent.RuntimeID); got != oldRuntimeID {
		t.Fatalf("rejected switch changed agent runtime to %s, want %s", got, oldRuntimeID)
	}
	for id, runtimeID := range taskRuntimes(t, queuedID, deferredID, runningID, dispatchedID, waitingID, finishedID) {
		if runtimeID != oldRuntimeID {
			t.Errorf("rejected switch changed task %s runtime to %s, want %s", id, runtimeID, oldRuntimeID)
		}
	}
	for _, id := range []string{runningID, dispatchedID, waitingID} {
		dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, id)
	}

	w = patchAgentRuntime(t, agentID, newRuntimeID)
	if w.Code != http.StatusOK {
		t.Fatalf("switch runtime: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	got := taskRuntimes(t, queuedID, deferredID, runningID, dispatchedID, waitingID, finishedID)
	want := map[string]string{
		queuedID:     newRuntimeID,
		deferredID:   newRuntimeID,
		runningID:    oldRuntimeID,
		dispatchedID: oldRuntimeID,
		waitingID:    oldRuntimeID,
		finishedID:   oldRuntimeID,
	}
	for id, runtimeID := range want {
		if got[id] != runtimeID {
			t.Errorf("task %s runtime = %s, want %s", id, got[id], runtimeID)
		}
	}
}

func TestUpdateAgent_RuntimeSwitch_ForbiddenLeavesTasks(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	suffix := time.Now().UnixNano()
	strangerID := dbfx.User(t,
		fmt.Sprintf("runtime-switch-stranger-%d", suffix),
		fmt.Sprintf("runtime-switch-stranger-%d@example.com", suffix),
	)
	dbfx.Member(t, testWorkspaceID, strangerID, "member")
	oldRuntimeID := dbfx.Runtime(t, "runtime-switch-forbidden-old")
	strangerRuntimeID := dbfx.Runtime(t, "runtime-switch-forbidden-private", testutil.Cols{
		"visibility": "private", "owner_id": strangerID,
	})
	agentID := dbfx.Agent(t, "runtime-switch-forbidden-agent", oldRuntimeID)
	queuedID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": oldRuntimeID, "issue_id": dbfx.Issue(t, "runtime switch forbidden"),
	})

	w := patchAgentRuntime(t, agentID, strangerRuntimeID)
	if w.Code != http.StatusForbidden {
		t.Fatalf("switch to another member's private runtime: expected 403, got %d: %s", w.Code, w.Body.String())
	}
	if got := taskRuntimes(t, queuedID)[queuedID]; got != oldRuntimeID {
		t.Fatalf("queued task runtime = %s, want unchanged %s", got, oldRuntimeID)
	}
}

// A provider session belongs to the runtime that ran it. A retry that moves to
// the agent's new runtime must not carry the old one: if that attempt ends
// before it reports a session of its own, the old session would be filed under
// the new runtime and the next claim there would be told to resume it.
func TestUpdateAgent_RuntimeSwitch_RetryLeavesSessionOnOldRuntime(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	oldRuntimeID := dbfx.Runtime(t, "runtime-switch-session-old")
	newRuntimeID := dbfx.Runtime(t, "runtime-switch-session-new")
	agentID := dbfx.Agent(t, "runtime-switch-session-agent", oldRuntimeID)
	issueID := dbfx.Issue(t, "runtime switch retry session", testutil.Cols{
		"assignee_type": "agent",
		"assignee_id":   agentID,
	})
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
	})
	const oldSession = "session-from-old-runtime"
	runningID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": oldRuntimeID, "issue_id": issueID, "status": "running",
		"dispatched_at": testutil.Raw("now()"), "started_at": testutil.Raw("now()"),
		"attempt": 1, "max_attempts": 2, "session_id": oldSession,
	})

	if w := patchAgentRuntime(t, agentID, newRuntimeID); w.Code != http.StatusConflict {
		t.Fatalf("switch runtime with active run: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	agent, err := testHandler.Queries.GetAgent(ctx, parseUUID(agentID))
	if err != nil {
		t.Fatalf("read agent after rejected switch: %v", err)
	}
	if got := uuidToString(agent.RuntimeID); got != oldRuntimeID {
		t.Fatalf("rejected switch changed agent runtime to %s, want %s", got, oldRuntimeID)
	}
	if got := taskRuntimes(t, runningID)[runningID]; got != oldRuntimeID {
		t.Fatalf("rejected switch changed parent runtime to %s, want %s", got, oldRuntimeID)
	}
	if _, err := testHandler.TaskService.FailTask(ctx, parseUUID(runningID),
		"run died", "", "", "", "timeout", false, "", ""); err != nil {
		t.Fatalf("fail the run on the old runtime: %v", err)
	}
	var retryID, retryRuntime, retryStatus string
	if err := testPool.QueryRow(ctx, `
		SELECT id::text, runtime_id::text, status FROM agent_task_queue
		WHERE retry_of_task_id = $1`, runningID,
	).Scan(&retryID, &retryRuntime, &retryStatus); err != nil {
		t.Fatalf("read queued retry child: %v", err)
	}
	if retryRuntime != oldRuntimeID || retryStatus != "queued" {
		t.Fatalf("retry before switch = runtime %s status %s, want runtime %s queued", retryRuntime, retryStatus, oldRuntimeID)
	}
	if w := patchAgentRuntime(t, agentID, newRuntimeID); w.Code != http.StatusOK {
		t.Fatalf("switch idle runtime: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var hasSession bool
	if err := testPool.QueryRow(ctx, `SELECT session_id IS NOT NULL FROM agent_task_queue WHERE id = $1`, retryID).Scan(&hasSession); err != nil {
		t.Fatalf("read retry session after switch: %v", err)
	}
	if hasSession {
		t.Fatal("retry carried the old runtime's session after switching runtime")
	}

	// The retry starts on the new runtime and dies before it reports a session.
	if _, err := testPool.Exec(ctx, `
		UPDATE agent_task_queue SET status = 'running', dispatched_at = now(), started_at = now()
		WHERE id = $1`, retryID,
	); err != nil {
		t.Fatalf("start retry child: %v", err)
	}
	if got := taskRuntimes(t, retryID)[retryID]; got != newRuntimeID {
		t.Fatalf("retry runtime = %s, want the agent's new runtime %s", got, newRuntimeID)
	}
	if _, err := testHandler.TaskService.FailTask(ctx, parseUUID(retryID),
		"runtime went away", "", "", "", "timeout", false, "", ""); err != nil {
		t.Fatalf("fail the retry on the new runtime: %v", err)
	}

	prior, err := testHandler.Queries.GetLastTaskSession(ctx, db.GetLastTaskSessionParams{
		AgentID: parseUUID(agentID),
		IssueID: parseUUID(issueID),
	})
	if err != nil {
		t.Fatalf("GetLastTaskSession: %v", err)
	}
	if prior.SessionID.String != oldSession || uuidToString(prior.RuntimeID) != oldRuntimeID {
		t.Fatalf("resume lookup = session %q on runtime %s, want %q still on the old runtime %s (new %s)",
			prior.SessionID.String, uuidToString(prior.RuntimeID), oldSession, oldRuntimeID, newRuntimeID)
	}
}
