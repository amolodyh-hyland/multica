package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/knowledge"
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

func TestPrivateLibraryDisabledConfigurationLeavesOrdinaryClaimAvailable(t *testing.T) {
	h, input := privateImportFixture(t)
	h.cfg.PublicURL = "https://example.test/invalid-private-origin"
	ctx := context.Background()
	task, err := h.Queries.GetAgentTask(ctx, parseUUID(input.Preview.Binding.TaskID))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := h.Queries.GetAgentRuntime(ctx, task.RuntimeID)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := h.pinClaimPrivateLibrary(ctx, &task, runtime, &AgentTaskResponse{WorkspaceID: testWorkspaceID, ProjectID: input.Preview.Binding.ProjectID})
	if err != nil || selected != nil {
		t.Fatalf("disabled private feature blocked ordinary claim: %+v, %v", selected, err)
	}
}

func TestPrivateLibraryClaimChecksAggregateBudgetBeforeLoadingPayloads(t *testing.T) {
	h, input := privateImportFixture(t)
	var receipt libraryimport.Receipt
	testutil.Call(t, h.ImportPrivateLibrary, newRequest(http.MethodPost, "/api/private-library/imports", input)).Want(201).JSON(&receipt)
	dbfx.Exec(t, `UPDATE private_library_import SET payload=jsonb_set(payload,'{preview,totalBytes}',to_jsonb(8388609::bigint)) WHERE id=$1`, receipt.ObjectID)
	ctx := context.Background()
	task, err := h.Queries.GetAgentTask(ctx, parseUUID(input.Preview.Binding.TaskID))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := h.Queries.GetAgentRuntime(ctx, task.RuntimeID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.pinClaimPrivateLibrary(ctx, &task, runtime, &AgentTaskResponse{WorkspaceID: testWorkspaceID, ProjectID: input.Preview.Binding.ProjectID}); err == nil || err.Error() != "runtime_limit" {
		t.Fatalf("oversize private task was read: %v", err)
	}
}

func TestPrivateLibraryCapabilityDoesNotAdvertiseActivationOrReload(t *testing.T) {
	h, _ := privateImportFixture(t)
	var capability libraryimport.Capability
	testutil.Call(t, h.PrivateLibraryCapabilities, newRequest(http.MethodGet, "/api/private-library/capabilities", nil)).Want(200).JSON(&capability)
	if !capability.Supported || capability.TaskActivation != "unsupported_provider" || capability.TaskActivationReason != "native_conformance_missing" || capability.Reload != "unsupported" {
		t.Fatalf("capabilities overstated activation: %+v", capability)
	}
}

func TestPrivateLibraryManifestIsNotInPublicTaskProjection(t *testing.T) {
	task := db.AgentTaskQueue{PrivateLibraryManifest: []byte(`{"private":"must-not-leak"}`)}
	raw, err := json.Marshal(taskToResponse(task, testWorkspaceID))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("private_library_manifest")) || bytes.Contains(raw, []byte("must-not-leak")) {
		t.Fatal("public task history disclosed private selection")
	}
}

func TestPrivateLibraryImportEnforcesAggregateTransactionBudget(t *testing.T) {
	h, input := privateImportFixture(t)
	input.Content = map[string]string{}
	input.Preview.Entry.Files = nil
	var files []knowledge.TextFile
	for i := 0; i < 5; i++ {
		path := fmt.Sprintf("rules/file-%d.md", i)
		text := strings.Repeat("x", knowledge.MaxFileBytes)
		file := libraryimport.File{Path: path, Mode: "text", SizeBytes: int64(len(text)), SHA256: knowledge.Sum([]byte(text))}
		input.Content[path] = text
		input.Preview.Entry.Files = append(input.Preview.Entry.Files, file)
		files = append(files, knowledge.TextFile{Path: path, Mode: "text", SizeBytes: len(text), SHA256: file.SHA256, Content: text})
	}
	input.Preview.Entry.EntryPoint = files[0].Path
	hash, err := knowledge.ContentHash(input.Preview.Entry.Kind, input.Preview.Entry.Format, input.Preview.Entry.EntryPoint, files)
	if err != nil {
		t.Fatal(err)
	}
	input.Preview.Entry.ContentHash = hash
	input.Preview.TotalBytes = 5 * knowledge.MaxFileBytes
	confirmPrivateImport(t, &input)
	testutil.Call(t, h.ImportPrivateLibrary, newRequest(http.MethodPost, "/api/private-library/imports", input)).Want(201)
	input.Preview.AttemptID = randomID()
	confirmPrivateImport(t, &input)
	testutil.Call(t, h.ImportPrivateLibrary, newRequest(http.MethodPost, "/api/private-library/imports", input)).Want(422)
	budget, err := h.Queries.PrivateLibraryTaskBudget(context.Background(), db.PrivateLibraryTaskBudgetParams{WorkspaceID: parseUUID(testWorkspaceID), TaskID: parseUUID(input.Preview.Binding.TaskID)})
	if err != nil || budget.ImportCount != 1 || budget.TotalBytes != input.Preview.TotalBytes {
		t.Fatalf("oversize import escaped rollback: %+v, %v", budget, err)
	}
}
