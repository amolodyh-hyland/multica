package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	publicapiv1 "github.com/multica-ai/multica/server/pkg/publicapi/v1"
)

var delegationScopes = []string{"delegations:read", "delegations:write"}

func delegationOperationsFixture(t *testing.T) (*Handler, http.Handler, publicapiv1.CreateDelegationRequest) {
	t.Helper()
	h, _, input := delegationFixture(t)
	router := chi.NewRouter()
	router.Use(middleware.RequirePublicAPIWorkspaceMember(h.Queries))
	router.Post("/delegations", h.CreatePublicDelegation)
	router.Get("/delegations", h.ListPublicDelegations)
	router.Get("/delegations/{delegation_id}", h.GetPublicDelegation)
	router.Post("/delegations/{delegation_id}/cancel", h.CancelPublicDelegation)
	h.TaskService = service.NewTaskService(h.Queries, testPool, h.Hub, h.Bus)
	return h, router, input
}

func delegationOperation(t *testing.T, router http.Handler, method, path, userID, workspaceID string, scopes []string, etag string, resultRevision ...string) *testutil.Response {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-Workspace-ID", workspaceID)
	if len(resultRevision) == 1 {
		req.Header.Set(publicapiv1.HeaderResultRevision, resultRevision[0])
	}
	if etag != "" {
		req.Header.Set("If-Match", etag)
	}
	ctx := middleware.WithPublicAPIUserCredential(req.Context(), middleware.PublicAPIUserCredential{
		UserID: userID, WorkspaceID: workspaceID, Scopes: scopes, Credential: publicapiv1.CredentialPersonalAccess,
	})
	return testutil.Call(t, router.ServeHTTP, req.WithContext(ctx))
}

func TestPublicDelegationStatusAndReconcileAfterResponseLoss(t *testing.T) {
	_, router, input := delegationOperationsFixture(t)
	key := uuid.NewString()
	var accepted publicapiv1.Delegation
	submitDelegation(t, router, input, key, testUserID, testWorkspaceID, testWorkspaceID, delegationScopes).Want(202).JSON(&accepted)
	dbfx.Exec(t, "UPDATE agent SET runtime_id=NULL WHERE id=$1", input.AgentID)
	path := "/delegations/" + accepted.DelegationID
	for _, status := range []string{"queued", "deferred", "dispatched", "waiting_local_directory", "running", "completed", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			dbfx.Exec(t, "UPDATE agent_task_queue SET status=$2, result='{}', error='secret', failure_reason='secret', work_dir='/secret' WHERE id=$1", accepted.TaskID, status)
			var out publicapiv1.Delegation
			response := delegationOperation(t, router, "GET", path, testUserID, testWorkspaceID, delegationScopes, "").Want(200).JSON(&out)
			if out.Status != status || out.TaskID != accepted.TaskID || out.DelegationID != accepted.DelegationID || out.Revision == "" {
				t.Fatalf("wrong status projection: %+v", out)
			}
			if strings.Contains(response.Body.String(), "secret") || strings.Contains(response.Body.String(), "work_dir") || strings.Contains(response.Body.String(), "instructions") {
				t.Fatal("private execution data escaped")
			}
			var recovered publicapiv1.DelegationListResponse
			delegationOperation(t, router, "GET", "/delegations?idempotency_key="+url.QueryEscape(key), testUserID, testWorkspaceID, delegationScopes, "").Want(200).JSON(&recovered)
			if len(recovered.Delegations) != 1 || recovered.Delegations[0] != out {
				t.Fatalf("reconcile differs: %+v", recovered)
			}
		})
	}
	var beforeDeletion publicapiv1.Delegation
	before := delegationOperation(t, router, "GET", path, testUserID, testWorkspaceID, delegationScopes, "").Want(200).JSON(&beforeDeletion)
	dbfx.Exec(t, "DELETE FROM agent_task_queue WHERE id=$1", accepted.TaskID)
	delegationOperation(t, router, "POST", path+"/cancel", testUserID, testWorkspaceID, delegationScopes, before.Header().Get("ETag")).Want(409)
	for _, method := range []string{"GET", "POST"} {
		target := path
		if method == "POST" {
			target += "/cancel"
		}
		var out publicapiv1.Delegation
		delegationOperation(t, router, method, target, testUserID, testWorkspaceID, delegationScopes, "").Want(200).JSON(&out)
		if out.Status != "unavailable" || out.TaskID != accepted.TaskID || out.Terminal {
			t.Fatalf("missing history: %+v", out)
		}
	}
	if dbfx.Count(t, "SELECT count(*) FROM agent_task_queue WHERE agent_id=$1", input.AgentID) != 0 {
		t.Fatal("reconciliation recreated execution")
	}
}

func TestPublicDelegationCancelIdempotentAndTerminal(t *testing.T) {
	for _, status := range []string{"queued", "deferred", "dispatched", "waiting_local_directory", "running", "completed", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			_, router, input := delegationOperationsFixture(t)
			var accepted publicapiv1.Delegation
			submitDelegation(t, router, input, uuid.NewString(), testUserID, testWorkspaceID, testWorkspaceID, delegationScopes).Want(202).JSON(&accepted)
			dbfx.Exec(t, "UPDATE agent_task_queue SET status=$2 WHERE id=$1", accepted.TaskID, status)
			path := "/delegations/" + accepted.DelegationID + "/cancel"
			want := "cancelled"
			if status == "completed" || status == "failed" {
				want = status
			}
			var first, second publicapiv1.Delegation
			delegationOperation(t, router, "POST", path, testUserID, testWorkspaceID, delegationScopes, "").Want(200).JSON(&first)
			delegationOperation(t, router, "POST", path, testUserID, testWorkspaceID, delegationScopes, "").Want(200).JSON(&second)
			if first != second || first.Status != want || !first.Terminal {
				t.Fatalf("cancel did not preserve terminal state: %+v %+v", first, second)
			}
			if dbfx.Count(t, "SELECT count(*) FROM agent_task_queue WHERE agent_id=$1", input.AgentID) != 1 {
				t.Fatal("cancel created a task")
			}
		})
	}
}

func TestPublicDelegationCancellationStaleRevision(t *testing.T) {
	_, router, input := delegationOperationsFixture(t)
	var accepted publicapiv1.Delegation
	submitDelegation(t, router, input, uuid.NewString(), testUserID, testWorkspaceID, testWorkspaceID, delegationScopes).Want(202).JSON(&accepted)
	path := "/delegations/" + accepted.DelegationID
	response := delegationOperation(t, router, "GET", path, testUserID, testWorkspaceID, delegationScopes, "").Want(200)
	etag := response.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	dbfx.Exec(t, "UPDATE agent_task_queue SET status='running' WHERE id=$1", accepted.TaskID)
	delegationOperation(t, router, "POST", path+"/cancel", testUserID, testWorkspaceID, delegationScopes, etag).Want(409)
	var current publicapiv1.Delegation
	response = delegationOperation(t, router, "GET", path, testUserID, testWorkspaceID, delegationScopes, "").Want(200).JSON(&current)
	if current.Status != "running" {
		t.Fatal("stale cancellation changed execution")
	}
	delegationOperation(t, router, "POST", path+"/cancel", testUserID, testWorkspaceID, delegationScopes, response.Header().Get("ETag")).Want(200)
}

func TestPublicDelegationOperationsAuthorization(t *testing.T) {
	h, router, input := delegationOperationsFixture(t)
	caller := dbfx.User(t, "Requester", uuid.NewString()+"@multica.test")
	dbfx.Member(t, testWorkspaceID, caller, "member")
	other := dbfx.User(t, "Other member", uuid.NewString()+"@multica.test")
	dbfx.Member(t, testWorkspaceID, other, "member")
	dbfx.Exec(t, "UPDATE agent SET permission_mode='public_to' WHERE id=$1", input.AgentID)
	target := dbfx.Insert(t, "agent_invocation_target", testutil.Cols{"agent_id": input.AgentID, "target_type": "member", "target_id": caller})
	var accepted publicapiv1.Delegation
	key := uuid.NewString()
	submitDelegation(t, router, input, key, caller, testWorkspaceID, testWorkspaceID, delegationScopes).Want(202).JSON(&accepted)
	path := "/delegations/" + accepted.DelegationID
	for _, method := range []string{"GET", "POST"} {
		suffix := ""
		if method == "POST" {
			suffix = "/cancel"
		}
		for _, id := range []string{accepted.DelegationID, uuid.NewString(), "invalid"} {
			var problem publicapiv1.Problem
			delegationOperation(t, router, method, "/delegations/"+id+suffix, other, testWorkspaceID, delegationScopes, "").Want(404).JSON(&problem)
			if problem.Code != "not_found" || problem.Detail != "resource not found" {
				t.Fatalf("existence oracle: %+v", problem)
			}
		}
		delegationOperation(t, router, method, path+suffix, caller, uuid.NewString(), delegationScopes, "").Want(404)
		delegationOperation(t, router, method, path+suffix, caller, testWorkspaceID, nil, "").Want(403)
	}
	otherWorkspace := dbfx.Workspace(t, "Other delegation workspace", "delegations-"+uuid.NewString())
	dbfx.Member(t, otherWorkspace, caller, "admin")
	delegationOperation(t, router, "GET", path, caller, otherWorkspace, delegationScopes, "").Want(404)
	delegationOperation(t, router, "POST", path+"/cancel", caller, otherWorkspace, delegationScopes, "").Want(404)
	var foreign publicapiv1.DelegationListResponse
	delegationOperation(t, router, "GET", "/delegations?idempotency_key="+key, caller, otherWorkspace, delegationScopes, "").Want(200).JSON(&foreign)
	if len(foreign.Delegations) != 0 {
		t.Fatal("reconciliation crossed workspace boundary")
	}
	var hidden publicapiv1.DelegationListResponse
	delegationOperation(t, router, "GET", "/delegations?idempotency_key="+key, other, testWorkspaceID, delegationScopes, "").Want(200).JSON(&hidden)
	if len(hidden.Delegations) != 0 {
		t.Fatal("other caller reconciled private key")
	}
	// Workspace owner can govern by ID, but key reconciliation remains caller-scoped.
	delegationOperation(t, router, "GET", path, testUserID, testWorkspaceID, delegationScopes, "").Want(200)
	dbfx.Exec(t, "DELETE FROM agent_invocation_target WHERE id=$1", target)
	delegationOperation(t, router, "POST", path+"/cancel", caller, testWorkspaceID, delegationScopes, "").Want(200)
	dbfx.Exec(t, "DELETE FROM member WHERE workspace_id=$1 AND user_id=$2", testWorkspaceID, caller)
	for _, operation := range []struct{ method, path string }{{"GET", path}, {"GET", "/delegations"}, {"POST", path + "/cancel"}} {
		delegationOperation(t, router, operation.method, operation.path, caller, testWorkspaceID, delegationScopes, "").Want(404)
	}
	h.FeatureFlags = nil
	delegationOperation(t, router, "GET", path, testUserID, testWorkspaceID, delegationScopes, "").Want(404)
	delegationOperation(t, router, "GET", "/delegations", testUserID, testWorkspaceID, delegationScopes, "").Want(404)
	delegationOperation(t, router, "POST", path+"/cancel", testUserID, testWorkspaceID, delegationScopes, "").Want(404)
}

func TestPublicDelegationPaginationAndFilterScope(t *testing.T) {
	_, router, input := delegationOperationsFixture(t)
	own := make(map[string]bool)
	for i := 0; i < 3; i++ {
		var accepted publicapiv1.Delegation
		submitDelegation(t, router, input, uuid.NewString(), testUserID, testWorkspaceID, testWorkspaceID, delegationScopes).Want(202).JSON(&accepted)
		own[accepted.DelegationID] = true
	}
	var page publicapiv1.DelegationListResponse
	delegationOperation(t, router, "GET", "/delegations?limit=1", testUserID, testWorkspaceID, delegationScopes, "").Want(200).JSON(&page)
	if len(page.Delegations) != 1 || page.NextCursor == "" {
		t.Fatalf("first page: %+v", page)
	}
	firstCursor := page.NextCursor
	seen := map[string]bool{}
	previous := ""
	for {
		if len(page.Delegations) != 1 || page.Delegations[0].DelegationID <= previous {
			t.Fatalf("unordered/repeated page: %+v", page)
		}
		previous = page.Delegations[0].DelegationID
		seen[previous] = true
		if page.NextCursor == "" {
			break
		}
		nextCursor := page.NextCursor
		page = publicapiv1.DelegationListResponse{}
		delegationOperation(t, router, "GET", "/delegations?limit=1&cursor="+url.QueryEscape(nextCursor), testUserID, testWorkspaceID, delegationScopes, "").Want(200).JSON(&page)
		if len(seen) > 1000 {
			t.Fatal("pagination did not terminate")
		}
	}
	for id := range own {
		if !seen[id] {
			t.Fatalf("delegation %s lost between pages", id)
		}
	}
	for _, path := range []string{"/delegations?idempotency_key=", "/delegations?limit=0", "/delegations?limit=201", "/delegations?cursor=garbage", "/delegations?idempotency_key=changed&cursor=" + firstCursor} {
		delegationOperation(t, router, "GET", path, testUserID, testWorkspaceID, delegationScopes, "").Want(400)
	}
	other := dbfx.User(t, "Other caller", uuid.NewString()+"@multica.test")
	dbfx.Member(t, testWorkspaceID, other, "member")
	delegationOperation(t, router, "GET", "/delegations?cursor="+firstCursor, other, testWorkspaceID, delegationScopes, "").Want(400)
	for id := range own {
		page = publicapiv1.DelegationListResponse{}
		delegationOperation(t, router, "GET", "/delegations?delegation_id="+id, testUserID, testWorkspaceID, delegationScopes, "").Want(200).JSON(&page)
		if len(page.Delegations) != 1 || page.Delegations[0].DelegationID != id || page.NextCursor != "" {
			t.Fatalf("ID reconciliation: %+v", page)
		}
		break
	}
}

func TestPublicDelegationCancelRaces(t *testing.T) {
	for _, mode := range []string{"dispatch", "complete"} {
		t.Run(mode, func(t *testing.T) {
			_, router, input := delegationOperationsFixture(t)
			var accepted publicapiv1.Delegation
			submitDelegation(t, router, input, uuid.NewString(), testUserID, testWorkspaceID, testWorkspaceID, delegationScopes).Want(202).JSON(&accepted)
			if mode == "complete" {
				dbfx.Exec(t, "UPDATE agent_task_queue SET status='running' WHERE id=$1", accepted.TaskID)
			}
			start := make(chan struct{})
			progress := make(chan error, 1)
			go func() {
				<-start
				var err error
				if mode == "dispatch" {
					_, err = testHandler.Queries.ClaimAgentTask(context.Background(), db.ClaimAgentTaskParams{AgentID: parseUUID(input.AgentID), RuntimeID: parseUUID(input.ExpectedRuntimeID), PrepareLeaseSecs: 60, RuntimeStaleSecs: 300})
				} else {
					_, err = testHandler.Queries.CompleteAgentTask(context.Background(), db.CompleteAgentTaskParams{ID: parseUUID(accepted.TaskID), Result: []byte(`{}`)})
				}
				progress <- err
			}()
			close(start)
			path := "/delegations/" + accepted.DelegationID
			var cancelled publicapiv1.Delegation
			delegationOperation(t, router, "POST", path+"/cancel", testUserID, testWorkspaceID, delegationScopes, "").Want(200).JSON(&cancelled)
			select {
			case err := <-progress:
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("race did not settle")
			}
			var final publicapiv1.Delegation
			delegationOperation(t, router, "GET", path, testUserID, testWorkspaceID, delegationScopes, "").Want(200).JSON(&final)
			if final != cancelled || !final.Terminal || (mode == "dispatch" && final.Status != "cancelled") || (mode == "complete" && final.Status != "completed" && final.Status != "cancelled") {
				t.Fatalf("terminal winner changed: %+v %+v", cancelled, final)
			}
		})
	}
}

func TestPublicDelegationCancelRechecksRevokedMemberInTransaction(t *testing.T) {
	for _, mode := range []string{"membership", "admin role"} {
		t.Run(mode, func(t *testing.T) {
			_, router, input := delegationOperationsFixture(t)
			caller := dbfx.User(t, "Cancel actor", uuid.NewString()+"@multica.test")
			dbfx.Member(t, testWorkspaceID, caller, "admin")
			var accepted publicapiv1.Delegation
			submitDelegation(t, router, input, uuid.NewString(), testUserID, testWorkspaceID, testWorkspaceID, delegationScopes).Want(202).JSON(&accepted)
			tx, err := testPool.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			statement := "DELETE FROM member WHERE workspace_id=$1 AND user_id=$2"
			if mode == "admin role" {
				statement = "UPDATE member SET role='member' WHERE workspace_id=$1 AND user_id=$2"
			}
			if _, err := tx.Exec(context.Background(), statement, testWorkspaceID, caller); err != nil {
				t.Fatal(err)
			}
			responses := make(chan *testutil.Response, 1)
			go func() {
				responses <- delegationOperation(t, router, "POST", "/delegations/"+accepted.DelegationID+"/cancel", caller, testWorkspaceID, delegationScopes, "")
			}()
			// The initial middleware read still sees the old role; the mutation must wait for the writer.
			waitForDelegationLockWait(t, tx.Conn().PgConn().PID())
			if err := tx.Commit(context.Background()); err != nil {
				t.Fatal(err)
			}
			select {
			case response := <-responses:
				response.Want(404)
			case <-time.After(5 * time.Second):
				t.Fatal("cancellation did not finish after revocation")
			}
			var current publicapiv1.Delegation
			delegationOperation(t, router, "GET", "/delegations/"+accepted.DelegationID, testUserID, testWorkspaceID, delegationScopes, "").Want(200).JSON(&current)
			if current.Status != "queued" {
				t.Fatalf("revoked actor cancelled work: %+v", current)
			}
		})
	}
}

func TestPublicDelegationAdminCanCancelByID(t *testing.T) {
	_, router, input := delegationOperationsFixture(t)
	admin := dbfx.User(t, "Workspace admin", uuid.NewString()+"@multica.test")
	dbfx.Member(t, testWorkspaceID, admin, "admin")
	var accepted publicapiv1.Delegation
	submitDelegation(t, router, input, uuid.NewString(), testUserID, testWorkspaceID, testWorkspaceID, delegationScopes).Want(202).JSON(&accepted)
	var out publicapiv1.Delegation
	delegationOperation(t, router, "POST", "/delegations/"+accepted.DelegationID+"/cancel", admin, testWorkspaceID, delegationScopes, "").Want(200).JSON(&out)
	if out.Status != "cancelled" || out.TaskID != accepted.TaskID {
		t.Fatalf("admin cancel: %+v", out)
	}
	task, err := testHandler.Queries.GetAgentTask(context.Background(), parseUUID(accepted.TaskID))
	if err != nil || task.CancelledByID != parseUUID(admin) || task.CancelledByType.String != "member" {
		t.Fatalf("cancellation actor: %v", err)
	}
}

func TestPublicDelegationCompletionWinnerIsPreserved(t *testing.T) {
	for _, winner := range []string{"complete", "cancel"} {
		t.Run(winner, func(t *testing.T) {
			h, router, input := delegationOperationsFixture(t)
			var accepted publicapiv1.Delegation
			submitDelegation(t, router, input, uuid.NewString(), testUserID, testWorkspaceID, testWorkspaceID, delegationScopes).Want(202).JSON(&accepted)
			dbfx.Exec(t, "UPDATE agent_task_queue SET status='running' WHERE id=$1", accepted.TaskID)
			responses := make(chan *testutil.Response, 1)
			path := "/delegations/" + accepted.DelegationID
			var completionErr error
			if winner == "complete" {
				tx, err := testPool.Begin(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(context.Background())
				_, err = testHandler.Queries.WithTx(tx).CompleteAgentTask(context.Background(), db.CompleteAgentTaskParams{ID: parseUUID(accepted.TaskID), Result: []byte(`{}`)})
				if err != nil {
					t.Fatal(err)
				}
				go func() {
					responses <- delegationOperation(t, router, "POST", path+"/cancel", testUserID, testWorkspaceID, delegationScopes, "")
				}()
				waitForDelegationLockWait(t, tx.Conn().PgConn().PID())
				if err := tx.Commit(context.Background()); err != nil {
					t.Fatal(err)
				}
			} else {
				ready, resume := make(chan uint32, 1), make(chan struct{})
				h.TaskService.TxStarter = delegationCommitGateStarter{h.TaskService.TxStarter, ready, resume}
				var once sync.Once
				release := func() { once.Do(func() { close(resume) }) }
				defer release()
				go func() {
					responses <- delegationOperation(t, router, "POST", path+"/cancel", testUserID, testWorkspaceID, delegationScopes, "")
				}()
				var pid uint32
				select {
				case pid = <-ready:
				case <-time.After(5 * time.Second):
					t.Fatal("cancel did not reach commit")
				}
				completed := make(chan error, 1)
				go func() {
					_, err := testHandler.Queries.CompleteAgentTask(context.Background(), db.CompleteAgentTaskParams{ID: parseUUID(accepted.TaskID), Result: []byte(`{}`)})
					completed <- err
				}()
				waitForDelegationLockWait(t, pid)
				release()
				select {
				case completionErr = <-completed:
				case <-time.After(5 * time.Second):
					t.Fatal("completion did not settle")
				}
				if !errors.Is(completionErr, pgx.ErrNoRows) {
					t.Fatalf("late completion changed cancelled task: %v", completionErr)
				}
			}
			var out publicapiv1.Delegation
			select {
			case response := <-responses:
				response.Want(200).JSON(&out)
			case <-time.After(5 * time.Second):
				t.Fatal("cancel did not settle")
			}
			want := "completed"
			if winner == "cancel" {
				want = "cancelled"
			}
			if out.Status != want {
				t.Fatalf("winner lost: %+v", out)
			}
			delegationOperation(t, router, "POST", path+"/cancel", testUserID, testWorkspaceID, delegationScopes, "").Want(200)
		})
	}
}

func TestPublicDelegationCancelCommitFailureRollsBack(t *testing.T) {
	h, router, input := delegationOperationsFixture(t)
	var accepted publicapiv1.Delegation
	submitDelegation(t, router, input, uuid.NewString(), testUserID, testWorkspaceID, testWorkspaceID, delegationScopes).Want(202).JSON(&accepted)
	h.TaskService.TxStarter = failedDelegationCommitStarter{h.TaskService.TxStarter}
	path := "/delegations/" + accepted.DelegationID
	delegationOperation(t, router, "POST", path+"/cancel", testUserID, testWorkspaceID, delegationScopes, "").Want(500)
	var out publicapiv1.Delegation
	delegationOperation(t, router, "GET", path, testUserID, testWorkspaceID, delegationScopes, "").Want(200).JSON(&out)
	if out.Status != "queued" {
		t.Fatalf("failed commit leaked cancellation: %+v", out)
	}
}

func TestPublicDelegationExecutionTransitions(t *testing.T) {
	_, router, input := delegationOperationsFixture(t)
	var accepted publicapiv1.Delegation
	submitDelegation(t, router, input, uuid.NewString(), testUserID, testWorkspaceID, testWorkspaceID, delegationScopes).Want(202).JSON(&accepted)
	path := "/delegations/" + accepted.DelegationID
	assertStatus := func(want string) {
		t.Helper()
		var out publicapiv1.Delegation
		delegationOperation(t, router, "GET", path, testUserID, testWorkspaceID, delegationScopes, "").Want(200).JSON(&out)
		if out.Status != want || out.TaskID != accepted.TaskID {
			t.Fatalf("transition to %s: %+v", want, out)
		}
	}
	assertStatus("queued")
	task, err := testHandler.Queries.ClaimAgentTask(context.Background(), db.ClaimAgentTaskParams{AgentID: parseUUID(input.AgentID), RuntimeID: parseUUID(input.ExpectedRuntimeID), PrepareLeaseSecs: 60, RuntimeStaleSecs: 300})
	if err != nil || task.ID != parseUUID(accepted.TaskID) {
		t.Fatalf("claim: %v", err)
	}
	assertStatus("dispatched")
	if _, err := testHandler.Queries.StartAgentTask(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	assertStatus("running")
	if _, err := testHandler.Queries.CompleteAgentTask(context.Background(), db.CompleteAgentTaskParams{ID: task.ID, Result: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	assertStatus("completed")
	delegationOperation(t, router, "POST", path+"/cancel", testUserID, testWorkspaceID, delegationScopes, "").Want(200)
	assertStatus("completed")
}
