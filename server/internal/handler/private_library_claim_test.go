package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/libraryimport"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestPrivateLibraryClaimPinsImmutableChildAndRefusesSuccessor(t *testing.T) {
	h, input := privateImportFixture(t)
	var receipt libraryimport.Receipt
	testutil.Call(t, h.ImportPrivateLibrary, newRequest(http.MethodPost, "/api/private-library/imports", input)).Want(201).JSON(&receipt)
	ctx := context.Background()
	task, err := h.Queries.GetAgentTask(ctx, parseUUID(input.Preview.Binding.TaskID))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := h.Queries.GetAgentRuntime(ctx, task.RuntimeID)
	if err != nil {
		t.Fatal(err)
	}
	response := AgentTaskResponse{WorkspaceID: testWorkspaceID, ProjectID: input.Preview.Binding.ProjectID}
	if _, err := h.pinClaimKnowledge(ctx, &task, runtime, &response); err != nil {
		t.Fatal(err)
	}
	pinned, err := h.pinClaimPrivateLibrary(ctx, &task, runtime, &response)
	if err != nil || pinned == nil || pinned.Validate() != nil || len(pinned.Imports) != 1 || pinned.Imports[0].ObjectID != receipt.ObjectID {
		t.Fatalf("private selection: %+v, %v", pinned, err)
	}
	// A retry chooses the retained child, including the original context digest.
	again, err := h.pinClaimPrivateLibrary(ctx, &task, runtime, &response)
	if err != nil || again.Digest != pinned.Digest {
		t.Fatalf("retry changed selection: %+v, %v", again, err)
	}
	request := httptest.NewRequest("POST", "/", nil)
	request.Header.Set("X-Client-Capabilities", protocol.DaemonCapabilityPrivateLibraryV1)
	task, err = h.Queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failure := h.prepareClaimPrivateLibrary(request, &task, runtime, &response); failure != nil {
		t.Fatalf("private claim refused: %+v", failure)
	}
	if response.PrivateLibraryManifest == nil || response.KnowledgeManifest == nil {
		t.Fatal("claim omitted pinned context association")
	}
	dbfx.Exec(t, `UPDATE agent_task_queue SET session_id='private-retained-session' WHERE id=$1`, task.ID)
	nextID := dbfx.Task(t, pinned.PersonaID, testutil.Cols{"runtime_id": input.Preview.Binding.RuntimeID, "issue_id": uuidToString(task.IssueID), "status": "dispatched"})
	next, err := h.Queries.GetAgentTask(ctx, parseUUID(nextID))
	if err != nil {
		t.Fatal(err)
	}
	response.PriorSessionID = "private-retained-session"
	if _, err := h.pinClaimPrivateLibrary(ctx, &next, runtime, &response); err == nil || err.Error() != "private_resume_requires_explicit_task_approval" {
		t.Fatalf("successor inherited invocation authority: %v", err)
	}
	var retained libraryimport.Selection
	if err := json.Unmarshal(task.PrivateLibraryManifest, &retained); err != nil || retained.Digest != pinned.Digest {
		t.Fatal("retained snapshot changed")
	}
}

func TestPrivateLibraryClaimDoesNotAdoptImportsAfterEmptyPin(t *testing.T) {
	h, input := privateImportFixture(t)
	ctx := context.Background()
	task, err := h.Queries.GetAgentTask(ctx, parseUUID(input.Preview.Binding.TaskID))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := h.Queries.GetAgentRuntime(ctx, task.RuntimeID)
	if err != nil {
		t.Fatal(err)
	}
	response := AgentTaskResponse{WorkspaceID: testWorkspaceID, ProjectID: input.Preview.Binding.ProjectID}
	if _, err := h.pinClaimKnowledge(ctx, &task, runtime, &response); err != nil {
		t.Fatal(err)
	}
	selected, err := h.pinClaimPrivateLibrary(ctx, &task, runtime, &response)
	if err != nil || selected == nil || len(selected.Imports) != 0 {
		t.Fatalf("empty selection: %+v, %v", selected, err)
	}
	testutil.Call(t, h.ImportPrivateLibrary, newRequest(http.MethodPost, "/api/private-library/imports", input)).Want(404)
	dbfx.Exec(t, `UPDATE agent_runtime SET owner_id=NULL WHERE id=$1`, runtime.ID)
	runtime.OwnerID = pgtype.UUID{}
	task, err = h.Queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Empty selections disclose no private content, but remain unchanged in storage.
	again, err := h.pinClaimPrivateLibrary(ctx, &task, runtime, &response)
	if err != nil || again.Digest != selected.Digest {
		t.Fatalf("empty snapshot was rewritten: %+v, %v", again, err)
	}
}
