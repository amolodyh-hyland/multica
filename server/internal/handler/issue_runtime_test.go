package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

func setIssueRuntime(t *testing.T, h *Handler, issueID, agentID string, runtimeID any, want int) {
	t.Helper()
	testutil.Call(t, h.SetIssueRuntime, testutil.WithURLParams(newRequest(http.MethodPut, "/api/issues/"+issueID+"/runtime", map[string]any{"agent_id": agentID, "runtime_id": runtimeID}), "id", issueID)).Want(want)
}

func TestIssueRuntimeSwitch(t *testing.T) {
	for _, status := range []string{"queued", "deferred", "dispatched", "running", "waiting_local_directory", "completed", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			old := dbfx.Runtime(t, "issue source", testutil.Cols{"provider": "claude"})
			next := dbfx.Runtime(t, "issue target", testutil.Cols{"provider": "codex"})
			agent := dbfx.Agent(t, "issue switch "+status, old)
			other := dbfx.Agent(t, "issue participant "+status, old)
			issue := dbfx.Issue(t, "switch issue", testutil.Cols{"assignee_type": "agent", "assignee_id": agent})
			task := dbfx.Task(t, agent, testutil.Cols{"runtime_id": old, "issue_id": issue, "status": status, "session_id": "native-session", "work_dir": "/tmp/native-dir"})
			participant := dbfx.Task(t, other, testutil.Cols{"runtime_id": old, "issue_id": issue})
			unrelated := dbfx.Task(t, agent, testutil.Cols{"runtime_id": old})
			dbfx.Cleanup(t, `DELETE FROM issue_runtime_override WHERE issue_id=$1`, issue)
			active := status == "running" || status == "dispatched" || status == "waiting_local_directory"
			want := http.StatusOK
			if active {
				want = http.StatusConflict
			}
			setIssueRuntime(t, testHandler, issue, agent, next, want)
			var profile, actual, otherRuntime, unrelatedRuntime string
			var snapshot, session, workDir *string
			var fresh bool
			dbfx.QueryRow(t, `SELECT runtime_id::text FROM agent WHERE id=$1`, agent).Scan(&profile)
			dbfx.QueryRow(t, `SELECT runtime_id::text,runtime_override_id::text,session_id,work_dir,force_fresh_session FROM agent_task_queue WHERE id=$1`, task).Scan(&actual, &snapshot, &session, &workDir, &fresh)
			dbfx.QueryRow(t, `SELECT runtime_id::text FROM agent_task_queue WHERE id=$1`, participant).Scan(&otherRuntime)
			dbfx.QueryRow(t, `SELECT runtime_id::text FROM agent_task_queue WHERE id=$1`, unrelated).Scan(&unrelatedRuntime)
			if profile != old || otherRuntime != old || unrelatedRuntime != old {
				t.Fatal("issue switch changed shared defaults or another participant")
			}
			if status == "queued" || status == "deferred" {
				if actual != next || snapshot == nil || *snapshot != next || session != nil || workDir != nil || !fresh {
					t.Fatalf("pending attempt not reset: %s %v %v %v %v", actual, snapshot, session, workDir, fresh)
				}
			} else if actual != old || snapshot != nil || session == nil || *session != "native-session" {
				t.Fatal("executing or historical attempt changed")
			}
			count := dbfx.Count(t, `SELECT count(*) FROM activity_log WHERE issue_id=$1 AND action='runtime_changed' AND details->>'from_provider'='claude' AND details->>'to_provider'='codex'`, issue)
			if (!active && count != 1) || (active && count != 0) {
				t.Fatalf("audit count=%d", count)
			}
			if !active {
				setIssueRuntime(t, testHandler, issue, agent, next, 200)
				if n := dbfx.Count(t, `SELECT count(*) FROM activity_log WHERE issue_id=$1 AND action='runtime_changed'`, issue); n != 1 {
					t.Fatalf("noop audit count=%d", n)
				}
			}
		})
	}
}

func TestIssueRuntimeEnqueueClaimAndReset(t *testing.T) {
	ctx := context.Background()
	old := dbfx.Runtime(t, "default claude", testutil.Cols{"provider": "claude"})
	next := dbfx.Runtime(t, "override codex", testutil.Cols{"provider": "codex"})
	agent := dbfx.Agent(t, "override enqueue profile", old)
	participant := dbfx.Agent(t, "override enqueue participant", old)
	issueID := dbfx.Issue(t, "override enqueue", testutil.Cols{"assignee_type": "agent", "assignee_id": agent})
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id=$1`, issueID)
	dbfx.Cleanup(t, `DELETE FROM issue_runtime_override WHERE issue_id=$1`, issueID)
	setIssueRuntime(t, testHandler, issueID, agent, next, 200)
	issue, err := testHandler.Queries.GetIssue(ctx, parseUUID(issueID))
	if err != nil {
		t.Fatal(err)
	}
	task, err := testHandler.TaskService.EnqueueTaskForIssue(ctx, issue, pgtype.UUID{})
	if err != nil {
		t.Fatal(err)
	}
	if task.RuntimeID != parseUUID(next) || task.RuntimeOverrideID != parseUUID(next) {
		t.Fatal("enqueue did not snapshot override")
	}
	other, err := testHandler.TaskService.EnqueueTaskForMention(ctx, issue, parseUUID(participant), pgtype.UUID{}, service.OriginNamed)
	if err != nil {
		t.Fatal(err)
	}
	if other.RuntimeID != parseUUID(old) || other.RuntimeOverrideID.Valid {
		t.Fatal("participant inherited assignee override")
	}
	// Changing the shared default leaves the explicit issue selection intact.
	testutil.Call(t, testHandler.UpdateAgent, testutil.WithURLParams(newRequest(http.MethodPatch, "/api/agents/"+agent, map[string]any{"runtime_id": next}), "id", agent)).Want(200)
	testutil.Call(t, testHandler.UpdateAgent, testutil.WithURLParams(newRequest(http.MethodPatch, "/api/agents/"+agent, map[string]any{"runtime_id": old}), "id", agent)).Want(200)
	claimed, err := testHandler.TaskService.ClaimTaskForRuntime(ctx, parseUUID(next))
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != task.ID {
		t.Fatal("override could not be claimed on its runtime")
	}
	setIssueRuntime(t, testHandler, issueID, agent, nil, 409)
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status='completed',completed_at=now() WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	setIssueRuntime(t, testHandler, issueID, agent, nil, 200)
	again, err := testHandler.TaskService.EnqueueTaskForIssue(ctx, issue, pgtype.UUID{})
	if err != nil {
		t.Fatal(err)
	}
	if again.RuntimeID != parseUUID(old) || again.RuntimeOverrideID.Valid {
		t.Fatal("reset did not restore profile default")
	}
}

func TestIssueRuntimeValidationAndRollback(t *testing.T) {
	old := dbfx.Runtime(t, "validation default")
	next := dbfx.Runtime(t, "validation target", testutil.Cols{"provider": "codex"})
	agent := dbfx.Agent(t, "validation profile", old)
	issue := dbfx.Issue(t, "validation issue", testutil.Cols{"assignee_type": "agent", "assignee_id": agent})
	task := dbfx.Task(t, agent, testutil.Cols{"runtime_id": old, "issue_id": issue})
	dbfx.Cleanup(t, `DELETE FROM issue_runtime_override WHERE issue_id=$1`, issue)
	setIssueRuntime(t, testHandler, issue, agent, "bad-id", 400)
	setIssueRuntime(t, testHandler, issue, agent, "00000000-0000-0000-0000-000000000000", 400)
	setIssueRuntime(t, testHandler, issue, "00000000-0000-0000-0000-000000000001", next, 409)
	otherUser := dbfx.User(t, "Other runtime owner", "runtime-override-other@example.test")
	dbfx.Member(t, testWorkspaceID, otherUser, "member")
	testutil.Call(t, testHandler.SetIssueRuntime, testutil.WithURLParams(newRequestAs(otherUser, http.MethodPut, "/api/issues/"+issue+"/runtime", map[string]any{"agent_id": agent, "runtime_id": next}), "id", issue)).Want(403)
	foreignWorkspace := dbfx.Workspace(t, "Other runtime workspace", "issue-runtime-other-workspace")
	foreignRuntime := dbfx.Runtime(t, "cross workspace target", testutil.Cols{"workspace_id": foreignWorkspace})
	setIssueRuntime(t, testHandler, issue, agent, foreignRuntime, 400)
	private := dbfx.Runtime(t, "other private runtime", testutil.Cols{"owner_id": otherUser})
	setIssueRuntime(t, testHandler, issue, agent, private, 403)
	h := *testHandler
	h.TxStarter = runtimeSwitchAuditFailureStarter{}
	setIssueRuntime(t, &h, issue, agent, next, 500)
	if n := dbfx.Count(t, `SELECT count(*) FROM issue_runtime_override WHERE issue_id=$1`, issue); n != 0 {
		t.Fatal("failed audit left setting")
	}
	var actual string
	dbfx.QueryRow(t, `SELECT runtime_id::text FROM agent_task_queue WHERE id=$1`, task).Scan(&actual)
	if actual != old {
		t.Fatal("failed audit moved task")
	}
}

func TestIssueRuntimeReassignmentAndTeardown(t *testing.T) {
	ctx := context.Background()
	old := dbfx.Runtime(t, "reassignment default")
	next := dbfx.Runtime(t, "reassignment override")
	agent := dbfx.Agent(t, "reassignment profile", old)
	other := dbfx.Agent(t, "next assignee", old)
	issue := dbfx.Issue(t, "reassignment issue", testutil.Cols{"assignee_type": "agent", "assignee_id": agent})
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id=$1`, issue)
	dbfx.Cleanup(t, `DELETE FROM issue_runtime_override WHERE issue_id=$1`, issue)
	setIssueRuntime(t, testHandler, issue, agent, next, 200)
	queued, err := testHandler.Queries.CreateAgentTask(ctx, db.CreateAgentTaskParams{AgentID: parseUUID(agent), IssueID: parseUUID(issue), RuntimeID: parseUUID(old)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE issue SET assignee_id=$1 WHERE id=$2`, other, issue); err != nil {
		t.Fatal(err)
	}
	subsequent, err := testHandler.Queries.CreateAgentTask(ctx, db.CreateAgentTaskParams{AgentID: parseUUID(other), IssueID: parseUUID(issue), RuntimeID: parseUUID(old)})
	if err != nil {
		t.Fatal(err)
	}
	if subsequent.RuntimeOverrideID.Valid || subsequent.RuntimeID != parseUUID(old) {
		t.Fatal("new assignee inherited another profile's override")
	}
	claimed, err := testHandler.TaskService.ClaimTaskForRuntime(ctx, parseUUID(next))
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != queued.ID {
		t.Fatal("reassignment lost previous attempt's runtime snapshot")
	}
	// Removing the default runtime must not cancel an explicit override elsewhere.
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	_, err = service.TeardownRuntime(ctx, db.New(tx), parseUUID(old), service.RuntimeTeardownOptions{CancelNonTerminalTasks: true})
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.New(tx).GetAgentTask(ctx, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != "dispatched" || row.RuntimeID != parseUUID(next) {
		t.Fatal("default teardown cancelled issue override")
	}
}

func TestIssueRuntimeDeliveryClearsProviderNativeDefaults(t *testing.T) {
	old := dbfx.Runtime(t, "delivery default", testutil.Cols{"provider": "claude"})
	next := dbfx.Runtime(t, "delivery target", testutil.Cols{"provider": "codex", "daemon_id": "issue-runtime-delivery"})
	agent := dbfx.Agent(t, "delivery override profile", old, testutil.Cols{"model": "claude-sonnet-4-6", "thinking_level": "high", "service_tier": "fast", "custom_args": `["--settings","incompatible.json"]`})
	issue := dbfx.Issue(t, "delivery override issue", testutil.Cols{"assignee_type": "agent", "assignee_id": agent})
	task := dbfx.Task(t, agent, testutil.Cols{"runtime_id": next, "runtime_override_id": next, "issue_id": issue, "force_fresh_session": true})
	request := withURLParam(newDaemonTokenRequest(http.MethodPost, "/api/daemon/runtimes/"+next+"/tasks/claim", nil, testWorkspaceID, "issue-runtime-delivery"), "runtimeId", next)
	response := testutil.Call(t, testHandler.ClaimTaskByRuntime, request).Want(200)
	var payload struct {
		Task *AgentTaskResponse `json:"task"`
	}
	response.JSON(&payload)
	if payload.Task == nil || payload.Task.ID != task || payload.Task.Agent == nil {
		t.Fatalf("no override delivery: %s", response.Body.String())
	}
	native := payload.Task.Agent
	if native.Model != "" || native.ThinkingLevel != "" || native.ServiceTier != "" || len(native.CustomArgs) != 0 {
		t.Fatalf("inherited incompatible profile options: %+v", native)
	}
	var model string
	dbfx.QueryRow(t, `SELECT model FROM agent WHERE id=$1`, agent).Scan(&model)
	if model != "claude-sonnet-4-6" {
		t.Fatal("delivery mutated shared profile options")
	}
}

func TestIssueRuntimeBusyIssueDoesNotDeadlock(t *testing.T) {
	ctx := context.Background()
	old := dbfx.Runtime(t, "busy issue source")
	next := dbfx.Runtime(t, "busy issue destination")
	agent := dbfx.Agent(t, "busy issue profile", old)
	issue := dbfx.Issue(t, "busy runtime issue", testutil.Cols{"assignee_type": "agent", "assignee_id": agent})
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT 1 FROM issue WHERE id=$1 FOR UPDATE`, issue); err != nil {
		t.Fatal(err)
	}
	setIssueRuntime(t, testHandler, issue, agent, next, 409)
}

func TestIssueRuntimeRetryUsesCurrentIssueSelection(t *testing.T) {
	ctx := context.Background()
	old := dbfx.Runtime(t, "retry default", testutil.Cols{"provider": "claude"})
	next := dbfx.Runtime(t, "retry target", testutil.Cols{"provider": "codex"})
	agent := dbfx.Agent(t, "retry override profile", old)
	issue := dbfx.Issue(t, "retry override issue", testutil.Cols{"assignee_type": "agent", "assignee_id": agent})
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id=$1`, issue)
	dbfx.Cleanup(t, `DELETE FROM issue_runtime_override WHERE issue_id=$1`, issue)
	parent := dbfx.Task(t, agent, testutil.Cols{"runtime_id": old, "issue_id": issue, "status": "failed", "session_id": "claude-session", "work_dir": "/tmp/old-provider", "attempt": 1})
	setIssueRuntime(t, testHandler, issue, agent, next, 200)
	child, err := testHandler.Queries.CreateRetryTask(ctx, db.CreateRetryTaskParams{ID: parseUUID(parent)})
	if err != nil {
		t.Fatal(err)
	}
	if child.RuntimeID != parseUUID(next) || child.RuntimeOverrideID != parseUUID(next) || child.SessionID.Valid || child.WorkDir.Valid || !child.ForceFreshSession {
		t.Fatalf("retry did not use fresh override: %+v", child)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status='failed',completed_at=now(),session_id='codex-session' WHERE id=$1`, child.ID); err != nil {
		t.Fatal(err)
	}
	setIssueRuntime(t, testHandler, issue, agent, nil, 200)
	retry, err := testHandler.Queries.CreateRetryTask(ctx, db.CreateRetryTaskParams{ID: child.ID})
	if err != nil {
		t.Fatal(err)
	}
	if retry.RuntimeID != parseUUID(old) || retry.RuntimeOverrideID.Valid || retry.SessionID.Valid || !retry.ForceFreshSession {
		t.Fatal("retry ignored reset to profile default")
	}
}

func TestIssueRuntimeUnboundProfileCanUseOverride(t *testing.T) {
	ctx := context.Background()
	next := dbfx.Runtime(t, "unbound override")
	agent := dbfx.Agent(t, "unbound override profile", next, testutil.Cols{"runtime_id": nil})
	issueID := dbfx.Issue(t, "unbound override issue", testutil.Cols{"assignee_type": "agent", "assignee_id": agent})
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id=$1`, issueID)
	dbfx.Cleanup(t, `DELETE FROM issue_runtime_override WHERE issue_id=$1`, issueID)
	setIssueRuntime(t, testHandler, issueID, agent, next, 200)
	issue, err := testHandler.Queries.GetIssue(ctx, parseUUID(issueID))
	if err != nil {
		t.Fatal(err)
	}
	task, err := testHandler.TaskService.EnqueueTaskForIssue(ctx, issue)
	if err != nil {
		t.Fatal(err)
	}
	if task.RuntimeID != parseUUID(next) || task.RuntimeOverrideID != parseUUID(next) {
		t.Fatal("unbound profile did not use issue override")
	}
	claimed, err := testHandler.TaskService.ClaimTaskForRuntime(ctx, parseUUID(next))
	if err != nil || claimed == nil {
		t.Fatalf("unbound override claim=%v, error=%v", claimed, err)
	}
}

func TestIssueRuntimePreservesConfirmedDelegation(t *testing.T) {
	old := dbfx.Runtime(t, "confirmed issue runtime")
	next := dbfx.Runtime(t, "replacement issue runtime")
	agent := dbfx.Agent(t, "confirmed issue profile", old)
	issue := dbfx.Issue(t, "confirmed issue", testutil.Cols{"assignee_type": "agent", "assignee_id": agent})
	task := dbfx.Task(t, agent, testutil.Cols{"runtime_id": old, "issue_id": issue})
	dbfx.Insert(t, "public_delegation", testutil.Cols{"id": dbid.NewV7(), "workspace_id": testWorkspaceID, "requester_id": testUserID, "idempotency_key": "issue-switch-" + task, "payload_hash": strings.Repeat("b", 64), "agent_id": agent, "runtime_id": old, "task_id": task, "source": "{}"})
	setIssueRuntime(t, testHandler, issue, agent, next, 409)
	if n := dbfx.Count(t, `SELECT count(*) FROM issue_runtime_override WHERE issue_id=$1`, issue); n != 0 {
		t.Fatal("confirmed delegation's runtime changed")
	}
}
