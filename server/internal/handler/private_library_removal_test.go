package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/scheduler"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/libraryimport"
)

func privateRemovalRequest(t *testing.T, input libraryimport.Request, objectID string) libraryimport.RemovalRequest {
	t.Helper()
	preview := libraryimport.RemovalPreview{Operation: "private_import_remove", ObjectID: objectID, Import: input.Preview}
	digest, err := preview.Digest()
	if err != nil {
		t.Fatal(err)
	}
	consent, err := libraryimport.ConfirmRemoval(preview, digest, preview.Import.Destination.OwnerID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return libraryimport.RemovalRequest{Preview: preview, Consent: consent}
}

func TestPrivateLibraryRemovalRequiresOwnerAndSeparateExactConsent(t *testing.T) {
	h, input := privateImportFixture(t)
	var receipt libraryimport.Receipt
	testutil.Call(t, h.ImportPrivateLibrary, newRequest(http.MethodPost, "/api/private-library/imports", input)).Want(201).JSON(&receipt)
	removal := privateRemovalRequest(t, input, receipt.ObjectID)
	request := func(userID string, body any) *http.Request {
		return withURLParam(newRequestAsUser(userID, http.MethodDelete, "/api/private-library/imports/"+receipt.ObjectID, body), "id", receipt.ObjectID)
	}
	for _, role := range []string{"member", "admin", "owner"} {
		other := dbfx.User(t, "Private removal other", "remove-"+randomID()+"@example.test")
		dbfx.Member(t, testWorkspaceID, other, role)
		testutil.Call(t, h.RemovePrivateLibraryImport, request(other, removal)).Want(404)
	}
	machine := request(testUserID, removal)
	machine.Header.Set("X-Actor-Source", "task_token")
	machine = machine.WithContext(middleware.WithAuthCredential(machine.Context(), middleware.AuthCredential{Kind: middleware.AuthCredentialTaskToken}))
	testutil.Call(t, h.RemovePrivateLibraryImport, machine).Want(403)
	testutil.Call(t, h.RemovePrivateLibraryImport, request(testUserID, nil)).Want(400)
	importedConsent := removal
	importedConsent.Consent = input.Consent
	testutil.Call(t, h.RemovePrivateLibraryImport, request(testUserID, importedConsent)).Want(403)
	expired := removal
	expired.Consent.ApprovedAt = time.Now().Add(-time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	testutil.Call(t, h.RemovePrivateLibraryImport, request(testUserID, expired)).Want(403)
	changed := input
	// DisplayName is not a payload field, but still changes the approved preview.
	changed.Preview.Entry.DisplayName = "Different preview"
	testutil.Call(t, h.RemovePrivateLibraryImport, request(testUserID, privateRemovalRequest(t, changed, receipt.ObjectID))).Want(409)
	wrongObject := privateRemovalRequest(t, input, randomID())
	testutil.Call(t, h.RemovePrivateLibraryImport, request(testUserID, wrongObject)).Want(409)
	record, err := h.Queries.GetPrivateLibraryImportForOwner(context.Background(), db.GetPrivateLibraryImportForOwnerParams{ID: parseUUID(receipt.ObjectID), WorkspaceID: parseUUID(testWorkspaceID), OwnerID: parseUUID(testUserID)})
	if err != nil || len(record.Payload) == 0 {
		t.Fatalf("refused removal changed storage: %v", err)
	}
}

func TestPrivateLibraryRemovalRevokesPinnedAccessAndPreventsResurrection(t *testing.T) {
	h, input := privateImportFixture(t)
	ctx := context.Background()
	var receipt libraryimport.Receipt
	testutil.Call(t, h.ImportPrivateLibrary, newRequest(http.MethodPost, "/api/private-library/imports", input)).Want(201).JSON(&receipt)
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
	if err != nil {
		t.Fatal(err)
	}
	dbfx.Exec(t, `UPDATE agent_task_queue SET status='dispatched',dispatched_at=now() WHERE id=$1`, task.ID)
	removal := privateRemovalRequest(t, input, receipt.ObjectID)
	req := withURLParam(newRequest(http.MethodDelete, "/api/private-library/imports/"+receipt.ObjectID, removal), "id", receipt.ObjectID)
	testutil.Call(t, h.RemovePrivateLibraryImport, req).Want(204)
	testutil.Call(t, h.GetPrivateLibraryImport, withURLParam(newRequest(http.MethodGet, "/api/private-library/imports/"+receipt.ObjectID, nil), "id", receipt.ObjectID)).Want(404)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		req := newRequest(method, "/api/private-library/tasks/"+input.Preview.Binding.TaskID+"/imports/"+receipt.ObjectID, nil)
		req.Header.Set("X-Task-ID", input.Preview.Binding.TaskID)
		req.Header.Set("X-Agent-ID", input.Preview.Binding.PersonaRef.PersonaID)
		req.Header.Set("X-Actor-Source", "task_token")
		req = req.WithContext(middleware.WithAuthCredential(req.Context(), middleware.AuthCredential{Kind: middleware.AuthCredentialTaskToken}))
		testutil.Call(t, h.GetTaskPrivateLibraryImport, withURLParams(req, "id", receipt.ObjectID, "taskId", input.Preview.Binding.TaskID)).Want(404)
	}
	testutil.Call(t, h.ImportPrivateLibrary, newRequest(http.MethodPost, "/api/private-library/imports", input)).Want(410)
	tombstone, err := h.Queries.GetPrivateLibraryImportByAttempt(ctx, db.GetPrivateLibraryImportByAttemptParams{WorkspaceID: parseUUID(testWorkspaceID), OwnerID: parseUUID(testUserID), AttemptID: input.Preview.AttemptID})
	if err != nil || !tombstone.RemovedAt.Valid || string(tombstone.Payload) != "{}" {
		t.Fatalf("payload-free removal record: %+v %v", tombstone, err)
	}
	task, err = h.Queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var retained libraryimport.Selection
	if err := json.Unmarshal(task.PrivateLibraryManifest, &retained); err != nil || retained.Digest != pinned.Digest {
		t.Fatalf("removal rewrote snapshot: %v", err)
	}
	if records, err := h.Queries.ListPrivateLibraryImportsForTask(ctx, db.ListPrivateLibraryImportsForTaskParams{WorkspaceID: runtime.WorkspaceID, TaskID: task.ID, OwnerID: runtime.OwnerID}); err != nil || len(records) != 0 {
		t.Fatalf("removed object retained claim access: %v", err)
	}
	request := newRequest(http.MethodPost, "/", nil)
	request.Header.Set("X-Client-Capabilities", "private-library-v1")
	if failure := h.prepareClaimPrivateLibrary(request, &task, runtime, &response); failure == nil {
		t.Fatal("retained snapshot bypassed removed object")
	}
}

func TestPrivateLibraryCleanupRemovesOrphansAndKeepsRetainedSnapshots(t *testing.T) {
	h, input := privateImportFixture(t)
	ctx := context.Background()
	var receipt libraryimport.Receipt
	testutil.Call(t, h.ImportPrivateLibrary, newRequest(http.MethodPost, "/api/private-library/imports", input)).Want(201).JSON(&receipt)
	retainedHandler, retainedInput := privateImportFixture(t)
	var retainedReceipt libraryimport.Receipt
	testutil.Call(t, retainedHandler.ImportPrivateLibrary, newRequest(http.MethodPost, "/api/private-library/imports", retainedInput)).Want(201).JSON(&retainedReceipt)
	retainedTask, err := h.Queries.GetAgentTask(ctx, parseUUID(retainedInput.Preview.Binding.TaskID))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := h.Queries.GetAgentRuntime(ctx, retainedTask.RuntimeID)
	if err != nil {
		t.Fatal(err)
	}
	response := AgentTaskResponse{WorkspaceID: testWorkspaceID, ProjectID: retainedInput.Preview.Binding.ProjectID}
	if _, err := h.pinClaimKnowledge(ctx, &retainedTask, runtime, &response); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pinClaimPrivateLibrary(ctx, &retainedTask, runtime, &response); err != nil {
		t.Fatal(err)
	}
	dbfx.Exec(t, `UPDATE agent_task_queue SET status='completed',completed_at=now() WHERE id=$1`, retainedTask.ID)
	issueIDTask, err := h.Queries.GetAgentTask(ctx, parseUUID(input.Preview.Binding.TaskID))
	if err != nil {
		t.Fatal(err)
	}
	issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issueIDTask.IssueID, WorkspaceID: parseUUID(testWorkspaceID)})
	if err != nil {
		t.Fatal(err)
	}
	h.TxStarter = interruptedPrivateStarter{}
	if _, err := h.deleteIssueAndCollectAttachmentURLs(ctx, issue, nil); err == nil {
		t.Fatal("interrupted issue deletion committed")
	}
	if _, err := h.Queries.GetPrivateLibraryImportForOwner(ctx, db.GetPrivateLibraryImportForOwnerParams{ID: parseUUID(receipt.ObjectID), WorkspaceID: parseUUID(testWorkspaceID), OwnerID: parseUUID(testUserID)}); err != nil {
		t.Fatalf("rolled back issue deletion lost content: %v", err)
	}
	h.TxStarter = testPool
	if _, err := h.deleteIssueAndCollectAttachmentURLs(ctx, issue, nil); err != nil {
		t.Fatal(err)
	}
	var deletedCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM private_library_import WHERE id=$1`, receipt.ObjectID).Scan(&deletedCount); err != nil || deletedCount != 0 {
		t.Fatalf("issue deletion left content: count=%d err=%v", deletedCount, err)
	}
	// Simulate a pre-cleanup cascade for the periodic reaper.
	orphanHandler, orphanInput := privateImportFixture(t)
	var orphan libraryimport.Receipt
	testutil.Call(t, orphanHandler.ImportPrivateLibrary, newRequest(http.MethodPost, "/api/private-library/imports", orphanInput)).Want(201).JSON(&orphan)
	dbfx.Exec(t, `DELETE FROM agent_task_queue WHERE id=$1`, orphanInput.Preview.Binding.TaskID)
	job := scheduler.PrivateLibraryImportPruneJob(h.Queries)
	if _, err := job.Handler(ctx, scheduler.HandlerInput{Job: &job, Scope: scheduler.ScopeGlobal}); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM private_library_import WHERE id=$1`, orphan.ObjectID).Scan(&deletedCount); err != nil || deletedCount != 0 {
		t.Fatalf("reaper left orphan: count=%d err=%v", deletedCount, err)
	}
	if _, err := h.Queries.GetPrivateLibraryImportForOwner(ctx, db.GetPrivateLibraryImportForOwnerParams{ID: parseUUID(retainedReceipt.ObjectID), WorkspaceID: parseUUID(testWorkspaceID), OwnerID: parseUUID(testUserID)}); err != nil {
		t.Fatalf("reaper removed retained terminal snapshot content: %v", err)
	}
}

func TestPrivateLibraryIssueDeletionCleansImportCommittedDuringCascadeWait(t *testing.T) {
	h, input := privateImportFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	b := input.Preview.Binding
	task, err := h.Queries.GetAgentTask(ctx, parseUUID(b.TaskID))
	if err != nil {
		t.Fatal(err)
	}
	issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: task.IssueID, WorkspaceID: parseUUID(testWorkspaceID)})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var taskID string
	if err := tx.QueryRow(ctx, `SELECT id FROM agent_task_queue WHERE id=$1 FOR UPDATE`, b.TaskID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := h.deleteIssueAndCollectAttachmentURLs(ctx, issue, nil); done <- err }()
	finished := false
	defer func() {
		cancel()
		tx.Rollback(context.Background())
		if !finished {
			<-done
		}
	}()
	if !waitForWaiterBlockedBy(t, int(tx.Conn().PgConn().PID()), 5*time.Second) {
		t.Fatal("issue cascade did not wait for task")
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	objectID := randomID()
	_, transport := (libraryimport.Payload{Entry: input.Preview.Entry, Content: input.Content}).Transport(objectID)
	if _, err := h.Queries.WithTx(tx).CreatePrivateLibraryImport(ctx, db.CreatePrivateLibraryImportParams{ID: parseUUID(objectID), WorkspaceID: parseUUID(testWorkspaceID), OwnerID: parseUUID(testUserID), AttemptID: input.Preview.AttemptID, TaskID: task.ID, RuntimeID: task.RuntimeID, ProjectID: parseUUID(b.ProjectID), PreviewDigest: input.Consent.PreviewDigest, ContentHash: input.Preview.Entry.ContentHash, TransportHash: transport.Hash, Payload: raw}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	err = <-done
	finished = true
	if err != nil {
		t.Fatalf("delete after import commit: %v", err)
	}
	var count int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM private_library_import WHERE id=$1`, objectID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("import committed during cascade was orphaned: count=%d err=%v", count, err)
	}
}
