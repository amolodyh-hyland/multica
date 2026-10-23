package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/knowledge"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestKnowledgeStatusAuthorizationAndFallback(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	_, runtimeID, daemonID := createRuntimeGuardAgent(t, context.Background())
	projectID := dbfx.Project(t, "Knowledge status project")
	ref := knowledgeTestRef()
	ref.OwnerID = testUserID
	ref.DaemonID = daemonID
	ref.ResolverID = daemonID
	raw, _ := json.Marshal(ref)
	resourceID := dbfx.Insert(t, "project_resource", testutil.Cols{"project_id": projectID, "workspace_id": testWorkspaceID, "resource_type": knowledge.File, "resource_ref": string(raw)})
	call := func() *http.Request {
		return withURLParam(newRequest("POST", "/", map[string]any{"resource_ids": []string{resourceID}}), "id", projectID)
	}
	dbfx.Exec(t, `UPDATE agent_runtime SET status='offline' WHERE id=$1`, runtimeID)
	var offline struct {
		Results []knowledge.SourceStatus `json:"results"`
	}
	testutil.Call(t, testHandler.CheckProjectKnowledgeStatus, call()).Want(200).JSON(&offline)
	if len(offline.Results) != 1 || offline.Results[0].Status != "unverified" || offline.Results[0].Reason != "owner_offline" {
		t.Fatalf("offline: %+v", offline)
	}
	dbfx.Exec(t, `UPDATE agent_runtime SET status='online',metadata='{}' WHERE id=$1`, runtimeID)
	var old struct {
		Results []knowledge.SourceStatus `json:"results"`
	}
	testutil.Call(t, testHandler.CheckProjectKnowledgeStatus, call()).Want(200).JSON(&old)
	if len(old.Results) != 1 || old.Results[0].Status != "incompatible" || old.Results[0].Reason != "needs_newer_runner" {
		t.Fatalf("old runner: %+v", old)
	}
	_, foreignProject := foreignWorkspaceWithProject(t, "status-foreign", "KSF")
	denied := withURLParam(newRequest("POST", "/", map[string]any{"resource_ids": []string{resourceID}}), "id", foreignProject)
	testutil.Call(t, testHandler.CheckProjectKnowledgeStatus, denied).Want(404)
	foreignRef := ref
	foreignRef.OwnerID = "00000000-0000-0000-0000-000000000001"
	foreignRaw, _ := json.Marshal(foreignRef)
	dbfx.Exec(t, `UPDATE project_resource SET resource_ref=$2 WHERE id=$1`, resourceID, string(foreignRaw))
	testutil.Call(t, testHandler.CheckProjectKnowledgeStatus, call()).Want(403)
	dbfx.Exec(t, `UPDATE project_resource SET resource_ref=$2 WHERE id=$1`, resourceID, string(raw))
	machine := withURLParam(newDaemonTokenRequest("POST", "/", map[string]any{"resource_ids": []string{resourceID}}, testWorkspaceID, daemonID), "id", projectID)
	machine.Header.Set("X-Actor-Source", "daemon_token")
	testutil.Call(t, testHandler.CheckProjectKnowledgeStatus, machine).Want(403)
}

func TestKnowledgeStatusQueueDispatchAndReportScope(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	_, runtimeID, daemonID := createRuntimeGuardAgent(t, context.Background())
	metadata := `{"capabilities":["knowledge-context-v1","knowledge-status-v1"]}`
	dbfx.Exec(t, `UPDATE agent_runtime SET metadata=$2 WHERE id=$1`, runtimeID, metadata)
	ref := knowledgeTestRef()
	ref.OwnerID = testUserID
	ref.DaemonID = daemonID
	ref.ResolverID = daemonID
	request := knowledge.StatusRequest{ID: "status1", WorkspaceID: testWorkspaceID, OwnerID: testUserID, DaemonID: daemonID, Bindings: []knowledge.Binding{{ID: "resource1", Kind: knowledge.File, Reference: ref}}, Deadline: time.Now().Add(30 * time.Second)}
	created, err := testHandler.KnowledgeStatusStore.create(context.Background(), runtimeID, request)
	if err != nil || !created {
		t.Fatal("could not queue status", err)
	}
	defer testHandler.KnowledgeStatusStore.remove(context.Background(), runtimeID, request.ID)
	if testHandler.takeKnowledgeStatus(context.Background(), runtimeID, "foreign-machine", "") != nil {
		t.Fatal("another daemon received the source metadata")
	}
	if testHandler.takeKnowledgeStatus(context.Background(), runtimeID, daemonID, "") == nil {
		t.Fatal("owner daemon did not receive queued request")
	}
	report := func(daemon string, body any) *http.Request {
		req := withURLParams(newDaemonTokenRequest("POST", "/", body, testWorkspaceID, daemon), "runtimeId", runtimeID, "requestId", request.ID)
		req.Header.Set("X-Client-Capabilities", protocol.DaemonCapabilityKnowledgeStatusV1)
		return req
	}
	results := knowledge.UnverifiedStatuses(request.Bindings, "unreadable")
	testutil.Call(t, testHandler.ReportKnowledgeStatus, report("foreign-machine", map[string]any{"results": results})).Want(403)
	invalid := map[string]any{"results": []map[string]any{{"resource_id": "resource1", "reference_hash": ref.ContentHash, "status": "available", "reason": "checked", "content": "PRIVATE FILE CONTENT"}}}
	testutil.Call(t, testHandler.ReportKnowledgeStatus, report(daemonID, invalid)).Want(400)
	testutil.Call(t, testHandler.ReportKnowledgeStatus, report(daemonID, map[string]any{"results": results})).Want(200)
	saved, _, err := testHandler.KnowledgeStatusStore.read(context.Background(), runtimeID)
	if err != nil || saved.State != "completed" {
		t.Fatal("status result not retained", err)
	}
}

func TestKnowledgeStatusRequestCancellationRemovesPendingWork(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	store := NewKnowledgeStatusStore(nil)
	request := knowledge.StatusRequest{ID: "status1", Deadline: time.Now().Add(time.Second)}
	if ok, err := store.create(context.Background(), "runtime1", request); err != nil || !ok {
		t.Fatal(err)
	}
	store.remove(context.Background(), "runtime1", request.ID)
	if work, err := store.take(context.Background(), "runtime1"); err != nil || work != nil {
		t.Fatal("cancelled work remained dispatchable")
	}
}
