package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestProjectInstructionsAPI(t *testing.T) {
	const instructions = "Use the release checklist. $(touch /tmp/must-not-execute)"
	var created ProjectResponse
	testutil.Call(t, testHandler.CreateProject, newRequest("POST", "/api/projects", map[string]any{
		"title": "Research without a repository", "description": "Research summary", "instructions": instructions,
	})).Want(http.StatusCreated).JSON(&created)
	if created.Instructions == nil || *created.Instructions != instructions || created.Description == nil || *created.Description != "Research summary" {
		t.Fatalf("create response = %+v", created)
	}
	t.Cleanup(func() { dbfx.Exec(t, `DELETE FROM project WHERE id = $1`, created.ID) })
	var got ProjectResponse
	testutil.Call(t, testHandler.GetProject, withURLParam(newRequest("GET", "/api/projects/"+created.ID, nil), "id", created.ID)).Want(http.StatusOK).JSON(&got)
	if got.Instructions == nil || *got.Instructions != instructions {
		t.Fatalf("get instructions = %v", got.Instructions)
	}
	var listed struct {
		Projects []ProjectResponse `json:"projects"`
	}
	testutil.Call(t, testHandler.ListProjects, newRequest("GET", "/api/projects", nil)).Want(http.StatusOK).JSON(&listed)
	found := false
	for _, p := range listed.Projects {
		if p.ID == created.ID {
			found = p.Instructions != nil && *p.Instructions == instructions
		}
	}
	if !found {
		t.Fatal("list did not preserve instructions")
	}
	var searched struct {
		Projects []ProjectResponse `json:"projects"`
	}
	testutil.Call(t, testHandler.SearchProjects, newRequest("GET", "/api/projects/search?q=Research", nil)).Want(http.StatusOK).JSON(&searched)
	found = false
	for _, p := range searched.Projects {
		if p.ID == created.ID {
			found = p.Instructions != nil && *p.Instructions == instructions
		}
	}
	if !found {
		t.Fatal("search did not preserve instructions")
	}
	for _, value := range []string{strings.Repeat("x", maxProjectInstructionsBytes+1), strings.Repeat("界", maxProjectInstructionsBytes/3+1)} {
		testutil.Call(t, testHandler.CreateProject, newRequest("POST", "/api/projects", map[string]any{"title": "Oversized", "instructions": value})).Want(http.StatusBadRequest)
		testutil.Call(t, testHandler.UpdateProject, withURLParam(newRequest("PUT", "/api/projects/"+created.ID, map[string]any{"instructions": value}), "id", created.ID)).Want(http.StatusBadRequest)
	}
	var legacy ProjectResponse
	testutil.Call(t, testHandler.CreateProject, newRequest("POST", "/api/projects", map[string]any{"title": "Legacy client"})).Want(http.StatusCreated).JSON(&legacy)
	t.Cleanup(func() { dbfx.Exec(t, `DELETE FROM project WHERE id = $1`, legacy.ID) })
	if legacy.Instructions != nil {
		t.Fatalf("legacy instructions = %v", legacy.Instructions)
	}
}

func TestProjectInstructionsForeignWorkspaceDenied(t *testing.T) {
	_, foreignID := foreignWorkspaceWithProject(t, "foreign-instructions-api", "FIA")
	for _, tc := range []struct {
		handler http.HandlerFunc
		method  string
		body    any
	}{
		{testHandler.GetProject, "GET", nil},
		{testHandler.UpdateProject, "PUT", map[string]any{"instructions": "Overwrite foreign instructions"}},
		{testHandler.DeleteProject, "DELETE", nil},
	} {
		response := testutil.Call(t, tc.handler, withURLParam(newRequest(tc.method, "/api/projects/"+foreignID, tc.body), "id", foreignID)).Want(http.StatusNotFound)
		assertNoForeignContext(t, response.Text())
	}
	var instructions string
	dbfx.QueryRow(t, `SELECT instructions FROM project WHERE id = $1`, foreignID).Scan(&instructions)
	if instructions != foreignProjectInstructions {
		t.Fatalf("foreign instructions changed: %q", instructions)
	}
	var listed struct {
		Projects []ProjectResponse `json:"projects"`
	}
	response := testutil.Call(t, testHandler.ListProjects, newRequest("GET", "/api/projects", nil)).Want(http.StatusOK)
	response.JSON(&listed)
	assertNoForeignContext(t, response.Text())
	for _, p := range listed.Projects {
		if p.ID == foreignID {
			t.Fatal("foreign project in list")
		}
	}
}

func TestDeletedProjectInstructionsDoNotReachClaims(t *testing.T) {
	projectID := dbfx.Project(t, "Deleted instructions", testutil.Cols{"instructions": "Deleted project guidance"})
	agentID := dbfx.Agent(t, "Instructions cleanup", testRuntimeID)
	sessionID := dbfx.ChatSession(t, agentID, testutil.Cols{"project_id": projectID})
	issueID := dbfx.Issue(t, "Instructions cleanup issue", testutil.Cols{"project_id": projectID})
	testutil.Call(t, testHandler.DeleteProject, withURLParam(newRequest("DELETE", "/api/projects/"+projectID, nil), "id", projectID)).Want(http.StatusNoContent)
	var chatProjectID *string
	dbfx.QueryRow(t, `SELECT project_id::text FROM chat_session WHERE id = $1`, sessionID).Scan(&chatProjectID)
	if chatProjectID != nil {
		t.Fatalf("chat retained project reference: %s", *chatProjectID)
	}
	var exists bool
	dbfx.QueryRow(t, `SELECT EXISTS(SELECT 1 FROM project WHERE id = $1)`, projectID).Scan(&exists)
	if exists {
		t.Fatal("instructions-bearing project survived deletion")
	}
	var issueProjectID *string
	dbfx.QueryRow(t, `SELECT project_id::text FROM issue WHERE id = $1`, issueID).Scan(&issueProjectID)
	if issueProjectID != nil {
		t.Fatalf("issue retained project reference: %s", *issueProjectID)
	}
	// The resolver must also handle stale soft references, including quick-create context.
	resolved, err := testHandler.resolveClaimProjectContext(context.Background(), parseUUID(projectID), parseUUID(testWorkspaceID))
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ProjectID != "" || resolved.Instructions != "" {
		t.Fatalf("deleted context = %+v", resolved)
	}
}

func TestProjectInstructionsWorkspaceWriteGuard(t *testing.T) {
	foreignWorkspaceID, foreignID := foreignWorkspaceWithProject(t, "foreign-instructions-sql", "FIS")
	_, err := testHandler.Queries.UpdateProject(context.Background(), db.UpdateProjectParams{
		ID: parseUUID(foreignID), WorkspaceID: parseUUID(testWorkspaceID), InstructionsSet: true,
	})
	if err != pgx.ErrNoRows {
		t.Fatalf("cross-workspace update error = %v", err)
	}
	resolved, err := testHandler.resolveClaimProjectContext(context.Background(), parseUUID(foreignID), parseUUID(foreignWorkspaceID))
	if err != nil || resolved.Instructions != foreignProjectInstructions {
		t.Fatalf("foreign context changed: %+v, %v", resolved, err)
	}
}
