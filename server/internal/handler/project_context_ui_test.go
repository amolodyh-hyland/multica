package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestProjectContextWithoutResourcesKeepsNativeIssueAndChatGrouping(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	const instructions = "Summarize the research and cite the evidence."
	var project ProjectResponse
	testutil.Call(t, testHandler.CreateProject, newRequest(http.MethodPost, "/api/projects", map[string]any{
		"title": "Research context without a repository", "instructions": instructions,
	})).Want(http.StatusCreated).JSON(&project)
	t.Cleanup(func() { dbfx.Exec(t, `DELETE FROM project WHERE id = $1`, project.ID) })
	var count int
	dbfx.QueryRow(t, `SELECT count(*) FROM project_resource WHERE project_id = $1`, project.ID).Scan(&count)
	if count != 0 {
		t.Fatal("project acquired an implicit resource")
	}
	for _, source := range []string{"issue", "chat"} {
		t.Run(source, func(t *testing.T) {
			agentID, runtimeID, daemonID := createRuntimeGuardAgent(t, context.Background())
			cols := testutil.Cols{"runtime_id": runtimeID}
			if source == "issue" {
				var issue IssueResponse
				testutil.Call(t, testHandler.CreateIssue, newRequest(http.MethodPost, "/api/issues", map[string]any{
					"title": "Research task", "project_id": project.ID,
				})).Want(http.StatusCreated).JSON(&issue)
				t.Cleanup(func() { dbfx.Exec(t, `DELETE FROM issue WHERE id = $1`, issue.ID) })
				cols["issue_id"] = issue.ID
				var groupedProjectID string
				dbfx.QueryRow(t, `SELECT project_id::text FROM issue WHERE id = $1`, issue.ID).Scan(&groupedProjectID)
				if groupedProjectID != project.ID {
					t.Fatal("issue lost native project grouping")
				}
			} else {
				var session ChatSessionResponse
				testutil.Call(t, testHandler.CreateChatSession, newRequest(http.MethodPost, "/api/chat/sessions", map[string]any{
					"agent_id": agentID, "title": "Research chat", "project_id": project.ID,
				})).Want(http.StatusCreated).JSON(&session)
				t.Cleanup(func() { dbfx.Exec(t, `DELETE FROM chat_session WHERE id = $1`, session.ID) })
				cols["chat_session_id"] = session.ID
				var groupedProjectID string
				dbfx.QueryRow(t, `SELECT project_id::text FROM chat_session WHERE id = $1`, session.ID).Scan(&groupedProjectID)
				if groupedProjectID != project.ID {
					t.Fatal("chat lost native project grouping")
				}
			}
			taskID := dbfx.Task(t, agentID, cols)
			req := withURLParam(newDaemonTokenRequest(http.MethodPost, "/api/daemon/runtimes/"+runtimeID+"/claim", nil, testWorkspaceID, daemonID), "runtimeId", runtimeID)
			var response struct {
				Task *AgentTaskResponse `json:"task"`
			}
			testutil.Call(t, testHandler.ClaimTaskByRuntime, req).Want(http.StatusOK).JSON(&response)
			if response.Task == nil || response.Task.ID != taskID || response.Task.ProjectID != project.ID || response.Task.ProjectInstructions != instructions {
				t.Fatalf("project context missing from %s claim: %+v", source, response.Task)
			}
			if len(response.Task.ProjectResources) != 0 || len(response.Task.ProjectKnowledge) != 0 {
				t.Fatal("resource-free project gained execution or knowledge bindings")
			}
		})
	}
}
