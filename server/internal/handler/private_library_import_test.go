package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/knowledge"
	"github.com/multica-ai/multica/server/pkg/libraryimport"
)

func privateImportFixture(t *testing.T) (*Handler, libraryimport.Request) {
	t.Helper()
	if testHandler == nil {
		t.Skip("database not available")
	}
	runtimeID := createClaimReclaimRuntime(t, context.Background(), "private library runtime")
	agentID, issueID := createClaimReclaimAgentAndIssue(t, context.Background(), runtimeID, "private library persona")
	projectID := dbfx.Project(t, "Private library project")
	dbfx.Exec(t, `UPDATE agent_runtime SET owner_id=$2,daemon_id='private-library-daemon',status='offline' WHERE id=$1`, runtimeID, testUserID)
	dbfx.Exec(t, `UPDATE agent SET owner_id=$2 WHERE id=$1`, agentID, testUserID)
	dbfx.Exec(t, `UPDATE issue SET project_id=$2 WHERE id=$1`, issueID, projectID)
	taskID := dbfx.Task(t, agentID, testutil.Cols{"runtime_id": runtimeID, "issue_id": issueID, "status": "queued", "originator_user_id": testUserID, "accountable_user_id": testUserID, "originator_source": "direct_human"})
	entry := libraryimport.Entry{SchemaVersion: 1, EntryID: "entry1", SourceID: "source1", Kind: "rule", DisplayName: "Review", Format: "plain-rule-v1", EntryPoint: "rules/review.md", Files: []libraryimport.File{{Path: "rules/review.md", Mode: "text", SizeBytes: 24, SHA256: knowledge.Sum([]byte("Review selected changes.\n"))}}, Requirements: []string{}, ContentHash: "sha256:26eb5fd1696af1e0c83b85a2933f699fb93c286ba0ca4e0056f528e174cbb94d", Provenance: libraryimport.Provenance{OriginAuthorityID: "source_authority", OriginSourceID: "source1", OriginEntryID: "entry1", OriginKind: "machine_local", ResolverID: "resolver1", SourceRevision: 1, AdapterVersion: "1", Verification: "observed"}}
	entry.Files[0].SizeBytes = int64(len("Review selected changes.\n"))
	preview := libraryimport.Preview{SchemaVersion: 1, AttemptID: randomID(), Operation: "private_import", SourceResolverID: "resolver1", Destination: libraryimport.Destination{ServerID: "multica_test", Origin: "https://multica.example.test", WorkspaceID: testWorkspaceID, OwnerID: testUserID, Audience: "owner_and_explicit_task"}, Binding: libraryimport.Binding{ProjectID: projectID, RuntimeID: runtimeID, TaskID: taskID, PersonaRef: &libraryimport.PersonaRef{ServerID: "multica_test", WorkspaceID: testWorkspaceID, PersonaID: agentID}}, Entry: entry, TotalBytes: entry.Files[0].SizeBytes, Excluded: []string{}}
	input := libraryimport.Request{Preview: preview, Content: map[string]string{"rules/review.md": "Review selected changes.\n"}}
	confirmPrivateImport(t, &input)
	h := New(testHandler.Queries, testPool, testHandler.Hub, testHandler.Bus, testHandler.EmailService, nil, nil, analytics.NoopClient{}, Config{PrivateImportAuthorityID: "multica_test", PublicURL: preview.Destination.Origin})
	t.Cleanup(func() { dbfx.Exec(t, `DELETE FROM private_library_import WHERE task_id=$1`, taskID) })
	return h, input
}

func confirmPrivateImport(t *testing.T, input *libraryimport.Request) {
	t.Helper()
	digest, err := input.Preview.Digest()
	if err != nil {
		t.Fatal(err)
	}
	input.Consent, err = libraryimport.Confirm(input.Preview, digest, input.Preview.Destination.OwnerID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
}

func TestPrivateLibraryImportOwnerTaskIsolationAndImmutableRetry(t *testing.T) {
	h, input := privateImportFixture(t)
	var skillsBefore int64
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM skill WHERE workspace_id=$1`, testWorkspaceID).Scan(&skillsBefore); err != nil {
		t.Fatal(err)
	}
	var first libraryimport.Receipt
	testutil.Call(t, h.ImportPrivateLibrary, newRequest(http.MethodPost, "/api/private-library/imports", input)).Want(201).JSON(&first)
	if first.Status != "stored" || first.ReasonCode != "unsupported_provider" || first.ContentHash == first.TransportHash {
		t.Fatalf("receipt=%+v", first)
	}
	var replay libraryimport.Receipt
	testutil.Call(t, h.ImportPrivateLibrary, newRequest(http.MethodPost, "/api/private-library/imports", input)).Want(200).JSON(&replay)
	if replay != first {
		t.Fatalf("retry=%+v first=%+v", replay, first)
	}
	get := func(userID string) *http.Request {
		return withURLParam(newRequestAsUser(userID, http.MethodGet, "/api/private-library/imports/"+first.ObjectID, nil), "id", first.ObjectID)
	}
	var object struct {
		Receipt   libraryimport.Receipt `json:"receipt"`
		Selection libraryimport.Request `json:"selection"`
		Bundle    libraryimport.Bundle  `json:"bundle"`
	}
	testutil.Call(t, h.GetPrivateLibraryImport, get(testUserID)).Want(200).JSON(&object)
	if object.Receipt != first || object.Selection.Content["rules/review.md"] != input.Content["rules/review.md"] || object.Bundle.Hash != first.TransportHash {
		t.Fatal("stored bytes or identity changed")
	}
	for _, role := range []string{"member", "admin", "owner"} {
		userID := dbfx.User(t, "Private import other "+role, fmt.Sprintf("private-import-%s-%s@example.test", role, randomID()))
		dbfx.Member(t, testWorkspaceID, userID, role)
		testutil.Call(t, h.GetPrivateLibraryImport, get(userID)).Want(404)
	}
	machine := get(testUserID)
	machine.Header.Set("X-Actor-Source", "task_token")
	testutil.Call(t, h.GetPrivateLibraryImport, machine).Want(403)
	dbfx.Exec(t, `UPDATE agent_task_queue SET status='dispatched',dispatched_at=now() WHERE id=$1`, input.Preview.Binding.TaskID)
	taskGet := func(workspace, agent, task, tokenTask string) *http.Request {
		req := newRequest(http.MethodGet, "/api/private-library/tasks/"+task+"/imports/"+first.ObjectID, nil)
		req.Header.Set("X-Workspace-ID", workspace)
		req.Header.Set("X-Agent-ID", agent)
		req.Header.Set("X-Task-ID", tokenTask)
		req.Header.Set("X-Actor-Source", "task_token")
		req = req.WithContext(middleware.WithAuthCredential(req.Context(), middleware.AuthCredential{Kind: middleware.AuthCredentialTaskToken}))
		return withURLParams(req, "id", first.ObjectID, "taskId", task)
	}
	binding := input.Preview.Binding
	testutil.Call(t, h.GetTaskPrivateLibraryImport, taskGet(testWorkspaceID, binding.PersonaRef.PersonaID, binding.TaskID, binding.TaskID)).Want(200).JSON(&object)
	testutil.Call(t, h.GetTaskPrivateLibraryImport, taskGet(testWorkspaceID, testRuntimeID, binding.TaskID, binding.TaskID)).Want(404)
	otherTask := dbfx.Task(t, binding.PersonaRef.PersonaID, testutil.Cols{"runtime_id": binding.RuntimeID, "issue_id": testutil.Raw("NULL"), "status": "dispatched"})
	testutil.Call(t, h.GetTaskPrivateLibraryImport, taskGet(testWorkspaceID, binding.PersonaRef.PersonaID, binding.TaskID, otherTask)).Want(403)
	testutil.Call(t, h.GetTaskPrivateLibraryImport, taskGet(testWorkspaceID, binding.PersonaRef.PersonaID, otherTask, otherTask)).Want(404)
	foreignWorkspace, _ := foreignWorkspaceWithProject(t, "private-library-foreign-"+randomID()[:8], "PRI")
	testutil.Call(t, h.GetTaskPrivateLibraryImport, taskGet(foreignWorkspace, binding.PersonaRef.PersonaID, binding.TaskID, binding.TaskID)).Want(404)
	daemonReq := newDaemonTokenRequest(http.MethodGet, "/api/private-library/tasks/"+binding.TaskID+"/imports/"+first.ObjectID, nil, testWorkspaceID, "private-library-daemon")
	testutil.Call(t, h.GetTaskPrivateLibraryImport, withURLParams(daemonReq, "id", first.ObjectID, "taskId", binding.TaskID)).Want(403)
	testutil.Call(t, h.ImportPrivateLibrary, newRequest(http.MethodPost, "/api/private-library/imports", input)).Want(200).JSON(&replay)
	if replay != first {
		t.Fatal("resume re-resolved selection")
	}
	input.Preview.Entry.DisplayName = "Changed name"
	confirmPrivateImport(t, &input)
	testutil.Call(t, h.ImportPrivateLibrary, newRequest(http.MethodPost, "/api/private-library/imports", input)).Want(409)
	var skillsAfter int64
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM skill WHERE workspace_id=$1`, testWorkspaceID).Scan(&skillsAfter); err != nil {
		t.Fatal(err)
	}
	if skillsBefore != skillsAfter {
		t.Fatal("private import published a catalog entry")
	}
}

func TestPrivateLibraryImportRejectsUnapprovedAndChangedBindings(t *testing.T) {
	for _, scenario := range []string{"expiry", "destination", "content", "runtime owner", "persona", "foreign project", "already dispatched", "machine"} {
		t.Run(scenario, func(t *testing.T) {
			h, input := privateImportFixture(t)
			want := 404
			switch scenario {
			case "expiry":
				input.Consent.ApprovedAt = time.Now().Add(-time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
				want = 403
			case "destination":
				input.Preview.Destination.ServerID = "other"
				want = 409
			case "content":
				input.Content["rules/review.md"] = "other"
				want = 422
			case "runtime owner":
				dbfx.Exec(t, `UPDATE agent_runtime SET owner_id=NULL WHERE id=$1`, input.Preview.Binding.RuntimeID)
			case "persona":
				input.Preview.Binding.PersonaRef.PersonaID = testRuntimeID
				confirmPrivateImport(t, &input)
				want = 409
			case "foreign project":
				_, project := foreignWorkspaceWithProject(t, "private-import-project-"+randomID()[:8], "PIP")
				input.Preview.Binding.ProjectID = project
				confirmPrivateImport(t, &input)
			case "already dispatched":
				dbfx.Exec(t, `UPDATE agent_task_queue SET status='dispatched',dispatched_at=now() WHERE id=$1`, input.Preview.Binding.TaskID)
			case "machine":
				want = 403
			}
			req := newRequest(http.MethodPost, "/api/private-library/imports", input)
			if scenario == "machine" {
				req.Header.Set("X-Actor-Source", "cloud_pat")
			}
			testutil.Call(t, h.ImportPrivateLibrary, req).Want(want)
			var count int64
			if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM private_library_import WHERE workspace_id=$1 AND attempt_id=$2`, testWorkspaceID, input.Preview.AttemptID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("failed import committed bytes")
			}
		})
	}
}

type unreadPrivateBody struct{ reads int }

func (b *unreadPrivateBody) Read([]byte) (int, error) {
	b.reads++
	return 0, errors.New("must not read")
}
func (b *unreadPrivateBody) Close() error { return nil }

func TestPrivateLibraryImportUnsupportedBeforeBodyRead(t *testing.T) {
	h, _ := privateImportFixture(t)
	h.cfg.PrivateImportAuthorityID = ""
	body := &unreadPrivateBody{}
	req := newRequest(http.MethodPost, "/api/private-library/imports", nil)
	req.Body = body
	testutil.Call(t, h.ImportPrivateLibrary, req).Want(501)
	if body.reads != 0 {
		t.Fatal("unsupported path read private files")
	}
}

type interruptedPrivateTx struct{ pgx.Tx }

func (tx interruptedPrivateTx) Commit(context.Context) error { return errors.New("interrupted commit") }

type interruptedPrivateStarter struct{}

func (interruptedPrivateStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := testPool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return interruptedPrivateTx{tx}, nil
}

func TestPrivateLibraryImportInterruptedCommitAndConcurrentRetry(t *testing.T) {
	h, input := privateImportFixture(t)
	h.TxStarter = interruptedPrivateStarter{}
	testutil.Call(t, h.ImportPrivateLibrary, newRequest(http.MethodPost, "/api/private-library/imports", input)).Want(500)
	if _, err := h.Queries.GetPrivateLibraryImportByAttempt(context.Background(), db.GetPrivateLibraryImportByAttemptParams{WorkspaceID: parseUUID(testWorkspaceID), OwnerID: parseUUID(testUserID), AttemptID: input.Preview.AttemptID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("failed commit persisted: %v", err)
	}
	h.TxStarter = testPool
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			h.ImportPrivateLibrary(w, newRequest(http.MethodPost, "/api/private-library/imports", input))
			results <- w
		}()
	}
	wg.Wait()
	close(results)
	id := ""
	created := 0
	for w := range results {
		if w.Code != 200 && w.Code != 201 {
			t.Fatalf("concurrent status=%d body=%s", w.Code, w.Body.String())
		}
		if w.Code == 201 {
			created++
		}
		var receipt libraryimport.Receipt
		if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
			t.Fatal(err)
		}
		if id != "" && id != receipt.ObjectID {
			t.Fatal("retry allocated multiple objects")
		}
		id = receipt.ObjectID
	}
	if created != 1 {
		t.Fatalf("created=%d", created)
	}
}

func TestPrivateLibraryImportRejectsPostImportProjectChange(t *testing.T) {
	for _, origin := range []string{"issue", "chat"} {
		t.Run(origin, func(t *testing.T) {
			h, input := privateImportFixture(t)
			b := input.Preview.Binding
			var originID string
			if origin == "chat" {
				originID = dbfx.ChatSession(t, b.PersonaRef.PersonaID, testutil.Cols{"project_id": b.ProjectID})
				dbfx.Exec(t, `UPDATE agent_task_queue SET issue_id=NULL,chat_session_id=$2 WHERE id=$1`, b.TaskID, originID)
			} else {
				if err := testPool.QueryRow(context.Background(), `SELECT issue_id FROM agent_task_queue WHERE id=$1`, b.TaskID).Scan(&originID); err != nil {
					t.Fatal(err)
				}
			}
			var receipt libraryimport.Receipt
			testutil.Call(t, h.ImportPrivateLibrary, newRequest(http.MethodPost, "/api/private-library/imports", input)).Want(201).JSON(&receipt)
			dbfx.Exec(t, `UPDATE agent_task_queue SET status='dispatched',dispatched_at=now() WHERE id=$1`, b.TaskID)
			get := func(method string) *http.Request {
				req := newRequest(method, "/api/private-library/tasks/"+b.TaskID+"/imports/"+receipt.ObjectID, nil)
				req.Header.Set("X-Workspace-ID", testWorkspaceID)
				req.Header.Set("X-Agent-ID", b.PersonaRef.PersonaID)
				req.Header.Set("X-Task-ID", b.TaskID)
				req.Header.Set("X-Actor-Source", "task_token")
				req = req.WithContext(middleware.WithAuthCredential(req.Context(), middleware.AuthCredential{Kind: middleware.AuthCredentialTaskToken}))
				return withURLParams(req, "id", receipt.ObjectID, "taskId", b.TaskID)
			}
			testutil.Call(t, h.GetTaskPrivateLibraryImport, get(http.MethodGet)).Want(200)
			nextProject := dbfx.Project(t, "Moved private task")
			if origin == "chat" {
				dbfx.Exec(t, `UPDATE chat_session SET project_id=$2 WHERE id=$1`, originID, nextProject)
			} else {
				dbfx.Exec(t, `UPDATE issue SET project_id=$2 WHERE id=$1`, originID, nextProject)
			}
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				testutil.Call(t, h.GetTaskPrivateLibraryImport, get(method)).Want(404)
			}
			records, err := h.Queries.ListPrivateLibraryImportsForTask(context.Background(), db.ListPrivateLibraryImportsForTaskParams{WorkspaceID: parseUUID(testWorkspaceID), TaskID: parseUUID(b.TaskID), OwnerID: parseUUID(testUserID)})
			if err != nil || len(records) != 0 {
				t.Fatalf("changed Project retained claim access: count=%d err=%v", len(records), err)
			}
			testutil.Call(t, h.GetPrivateLibraryImport, withURLParam(newRequest(http.MethodGet, "/api/private-library/imports/"+receipt.ObjectID, nil), "id", receipt.ObjectID)).Want(200)
		})
	}
}
