package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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

func TestIssueRuntimeAdmissionUsesOverrideBeforeDefault(t *testing.T) {
	for _, defaultState := range []string{"unbound", "unusable"} {
		for _, action := range []string{"comment", "mention", "reply", "continuation", "promotion", "assignment"} {
			t.Run(defaultState+"/"+action, func(t *testing.T) {
				old := dbfx.Runtime(t, "admission default")
				target := dbfx.Runtime(t, "admission override")
				profile := dbfx.Agent(t, "admission profile", old)
				issue := dbfx.Issue(t, "admission override issue", testutil.Cols{"assignee_type": "agent", "assignee_id": profile, "status": "backlog"})
				dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id=$1`, issue)
				dbfx.Cleanup(t, `DELETE FROM issue_runtime_override WHERE issue_id=$1`, issue)
				setIssueRuntime(t, testHandler, issue, profile, target, http.StatusOK)
				if defaultState == "unbound" {
					dbfx.Exec(t, `UPDATE agent SET runtime_id=NULL WHERE id=$1`, profile)
				} else {
					dbfx.Exec(t, `UPDATE agent_runtime SET status='offline', metadata='{"offline_reason":{"code":"not_executable","detail":"CLI cannot run"}}'::jsonb WHERE id=$1`, old)
				}
				if action == "assignment" {
					dbfx.Exec(t, `UPDATE issue SET status='todo', assignee_type=NULL, assignee_id=NULL WHERE id=$1`, issue)
					testutil.Call(t, testHandler.UpdateIssue, withURLParam(newRequest(http.MethodPut, "/api/issues/"+issue, map[string]any{"assignee_type": "agent", "assignee_id": profile}), "id", issue)).Want(http.StatusOK)
				} else if action == "promotion" {
					testutil.Call(t, testHandler.UpdateIssue, withURLParam(newRequest(http.MethodPut, "/api/issues/"+issue, map[string]any{"status": "todo"}), "id", issue)).Want(http.StatusOK)
				} else {
					content := "Please continue this issue"
					if action == "mention" {
						content = "[@Agent](mention://agent/" + profile + ") please continue"
					}
					body := map[string]any{"content": content}
					if action == "reply" {
						body["parent_id"] = dbfx.Comment(t, issue, "Prior agent message", testutil.Cols{"author_type": "agent", "author_id": profile})
					} else if action == "continuation" {
						body["parent_id"] = dbfx.Comment(t, issue, "[@Agent](mention://agent/"+profile+") original thread")
					}
					testutil.Call(t, testHandler.CreateComment, withURLParam(newRequest(http.MethodPost, "/api/issues/"+issue+"/comments", body), "id", issue)).Want(http.StatusCreated)
				}
				if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id=$1 AND agent_id=$2 AND status='queued' AND runtime_id=$3 AND runtime_override_id=$3`, issue, profile, target); n != 1 {
					t.Fatalf("healthy override received %d queued runs with %s shared default", n, defaultState)
				}
			})
		}
	}
}

func TestIssueRuntimeRetryRacingProfileSwitchUsesCurrentBinding(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	old := dbfx.Runtime(t, "retry race default")
	target := dbfx.Runtime(t, "retry race new default")
	previousOverride := dbfx.Runtime(t, "retry race prior override")
	profile := dbfx.Agent(t, "retry race profile", old)
	issue := dbfx.Issue(t, "retry race issue", testutil.Cols{"assignee_type": "agent", "assignee_id": profile})
	parentID := dbfx.Task(t, profile, testutil.Cols{"runtime_id": previousOverride, "runtime_override_id": previousOverride, "issue_id": issue, "status": "failed", "attempt": 1, "max_attempts": 2, "failure_reason": "timeout"})
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id=$1`, issue)
	// The previous issue override has already been cleared. The retry must
	// resolve the default after waiting for the concurrent profile switch.
	parent, err := testHandler.Queries.GetAgentTask(ctx, parseUUID(parentID))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `UPDATE agent SET runtime_id=$1 WHERE id=$2`, target, profile); err != nil {
		t.Fatal(err)
	}
	if _, err := db.New(tx).MovePendingAgentTasksToRuntime(ctx, db.MovePendingAgentTasksToRuntimeParams{AgentID: parseUUID(profile), RuntimeID: parseUUID(target)}); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, testPool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	svc := *testHandler.TaskService
	svc.Queries, svc.TxStarter = db.New(conn), conn
	type result struct {
		task *db.AgentTaskQueue
		err  error
	}
	done := make(chan result, 1)
	go func() { task, err := svc.MaybeRetryFailedTask(ctx, parent); done <- result{task, err} }()
	waitForBlockedWriter(t, ctx, int32(conn.PgConn().PID()))
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	out := <-done
	if out.err != nil {
		t.Fatal(out.err)
	}
	if out.task == nil || out.task.RuntimeID != parseUUID(target) || out.task.RuntimeOverrideID.Valid {
		t.Fatalf("retry=%+v; want current default %s without override authority", out.task, target)
	}
	claimed, err := testHandler.TaskService.ClaimTaskForRuntime(ctx, parseUUID(target))
	if err != nil || claimed == nil || claimed.ID != out.task.ID {
		t.Fatalf("retry not claimable: task=%v error=%v", claimed, err)
	}
}

func TestRuntimeSwitchDisablesManualRerunSourceResume(t *testing.T) {
	for _, scope := range []string{"issue", "profile"} {
		for _, sameSource := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/source-runtime-%v", scope, sameSource), func(t *testing.T) {
				sourceRuntime := dbfx.Runtime(t, "rerun source", testutil.Cols{"daemon_id": "rerun-source"})
				current := dbfx.Runtime(t, "rerun current", testutil.Cols{"daemon_id": "rerun-current"})
				target := sourceRuntime
				if !sameSource {
					target = dbfx.Runtime(t, "rerun third runtime", testutil.Cols{"daemon_id": "rerun-target"})
				}
				profile := dbfx.Agent(t, "retarget rerun profile", current)
				issue := dbfx.Issue(t, "retarget rerun issue", testutil.Cols{"assignee_type": "agent", "assignee_id": profile})
				source := dbfx.Task(t, profile, testutil.Cols{"runtime_id": sourceRuntime, "issue_id": issue, "status": "completed", "session_id": "historical-native-session", "work_dir": "/tmp/historical-rerun-directory"})
				dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id=$1`, issue)
				dbfx.Cleanup(t, `DELETE FROM issue_runtime_override WHERE issue_id=$1`, issue)
				rerun, err := testHandler.TaskService.RerunIssue(context.Background(), parseUUID(issue), parseUUID(source), pgtype.UUID{}, parseUUID(testUserID), nil)
				if err != nil || rerun == nil {
					t.Fatalf("rerun=%v error=%v", rerun, err)
				}
				if scope == "issue" {
					setIssueRuntime(t, testHandler, issue, profile, target, http.StatusOK)
				} else {
					dbfx.Cleanup(t, `DELETE FROM activity_log WHERE details->>'agent_id'=$1`, profile)
					testutil.Call(t, testHandler.UpdateAgent, withURLParam(newRequest(http.MethodPatch, "/api/agents/"+profile, map[string]any{"runtime_id": target}), "id", profile)).Want(http.StatusOK)
				}
				targetRuntime, err := testHandler.Queries.GetAgentRuntime(context.Background(), parseUUID(target))
				if err != nil {
					t.Fatal(err)
				}
				request := withURLParam(newDaemonTokenRequest(http.MethodPost, "/api/daemon/runtimes/"+target+"/tasks/claim", nil, testWorkspaceID, targetRuntime.DaemonID.String), "runtimeId", target)
				response := testutil.Call(t, testHandler.ClaimTaskByRuntime, request).Want(http.StatusOK)
				var payload struct {
					Task *AgentTaskResponse `json:"task"`
				}
				response.JSON(&payload)
				if payload.Task == nil || payload.Task.ID != uuidToString(rerun.ID) {
					t.Fatalf("no retargeted rerun: %s", response.Body.String())
				}
				if payload.Task.PriorSessionID != "" || payload.Task.PriorWorkDir != "" {
					t.Fatalf("retargeted rerun restored source state: session=%q directory=%q", payload.Task.PriorSessionID, payload.Task.PriorWorkDir)
				}
				saved, err := testHandler.Queries.GetAgentTask(context.Background(), rerun.ID)
				if err != nil {
					t.Fatal(err)
				}
				if saved.RerunOfTaskID != parseUUID(source) {
					t.Fatal("retargeting lost rerun provenance")
				}
				// A failure before launch must not revive the source via the
				// retry's ordinary most-recent-session fallback either.
				dbfx.Exec(t, `UPDATE agent_task_queue SET status='failed', completed_at=now() WHERE id=$1`, rerun.ID)
				retry, err := testHandler.Queries.CreateRetryTask(context.Background(), db.CreateRetryTaskParams{ID: rerun.ID})
				if err != nil {
					t.Fatal(err)
				}
				retryResponse := testutil.Call(t, testHandler.ClaimTaskByRuntime, request.Clone(request.Context())).Want(http.StatusOK)
				var retryPayload struct {
					Task *AgentTaskResponse `json:"task"`
				}
				retryResponse.JSON(&retryPayload)
				if retryPayload.Task == nil || retryPayload.Task.ID != uuidToString(retry.ID) {
					t.Fatalf("no retry: %s", retryResponse.Body.String())
				}
				if retryPayload.Task.PriorSessionID != "" || retryPayload.Task.PriorWorkDir != "" {
					t.Fatal("retry revived native state cleared by retargeting")
				}

			})
		}
	}
}
