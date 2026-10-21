package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	publicapiv1 "github.com/multica-ai/multica/server/pkg/publicapi/v1"
)

func delegationFixture(t *testing.T) (*Handler, http.Handler, publicapiv1.CreateDelegationRequest) {
	t.Helper()
	runtimeID := dbfx.Runtime(t, "Delegation runtime", testutil.Cols{"metadata": testutil.Raw(`'{"capabilities":["public-delegation-v1"]}'::jsonb`)})
	agentID := dbfx.Agent(t, "Delegation profile "+uuid.NewString(), runtimeID)
	dbfx.Cleanup(t, "DELETE FROM public_delegation WHERE agent_id=$1", agentID)
	dbfx.Cleanup(t, "DELETE FROM agent_task_queue WHERE agent_id=$1", agentID)
	h := *testHandler
	h.TaskService = nil
	withFeatureFlag(t, &h, featureflags.PublicDelegationsV1, true)
	router := chi.NewRouter()
	router.Use(middleware.RequirePublicAPIWorkspaceMember(h.Queries))
	router.With(middleware.RequirePublicAPIScope("delegations:write")).Post("/delegations", h.CreatePublicDelegation)
	return &h, router, publicapiv1.CreateDelegationRequest{
		AgentID: agentID, ExpectedRuntimeID: runtimeID,
		Source: publicapiv1.DelegationSource{System: "ao", ProjectID: "project", TaskID: "source-task"},
		Task:   publicapiv1.DelegationTask{Title: "Delegated work", Instructions: "Inspect the transaction."},
	}
}

func submitDelegation(t *testing.T, router http.Handler, input publicapiv1.CreateDelegationRequest, key, userID, boundWorkspace, requestedWorkspace string, scopes []string) *testutil.Response {
	t.Helper()
	req := testutil.JSONRequest(http.MethodPost, "/delegations", input)
	req.Header.Set("X-Workspace-ID", requestedWorkspace)
	req.Header.Set(publicapiv1.HeaderIdempotencyKey, key)
	ctx := middleware.WithPublicAPIUserCredential(req.Context(), middleware.PublicAPIUserCredential{
		UserID: userID, WorkspaceID: boundWorkspace, Scopes: scopes, Credential: publicapiv1.CredentialPersonalAccess,
	})
	return testutil.Call(t, router.ServeHTTP, req.WithContext(ctx))
}

func TestPublicDelegationConcurrentRetryAndResponseLoss(t *testing.T) {
	_, router, input := delegationFixture(t)
	key := uuid.NewString()
	const parallel = 12
	start := make(chan struct{})
	responses := make(chan *testutil.Response, parallel)
	var workers sync.WaitGroup
	for i := 0; i < parallel; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			responses <- submitDelegation(t, router, input, key, testUserID, testWorkspaceID, testWorkspaceID, []string{"delegations:write"})
		}()
	}
	close(start)
	workers.Wait()
	close(responses)
	var identity publicapiv1.Delegation
	created := 0
	for response := range responses {
		if response.Code == http.StatusAccepted {
			created++
		} else if response.Code != http.StatusOK {
			t.Fatalf("submit: %d %s", response.Code, response.Body.String())
		}
		var accepted publicapiv1.Delegation
		if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
			t.Fatal(err)
		}
		if identity.DelegationID == "" {
			identity = accepted
		}
		if accepted != identity || accepted.Status != "queued" {
			t.Fatalf("different admissions: %+v vs %+v", accepted, identity)
		}
	}
	if created != 1 {
		t.Fatalf("new admissions = %d, want 1", created)
	}
	if n := dbfx.Count(t, "SELECT count(*) FROM public_delegation WHERE agent_id=$1", input.AgentID); n != 1 {
		t.Fatalf("delegations = %d", n)
	}
	if n := dbfx.Count(t, "SELECT count(*) FROM agent_task_queue WHERE agent_id=$1", input.AgentID); n != 1 {
		t.Fatalf("tasks = %d", n)
	}
	task, err := testHandler.Queries.GetAgentTask(context.Background(), parseUUID(identity.TaskID))
	if err != nil {
		t.Fatal(err)
	}
	if task.IssueID.Valid || task.ChatSessionID.Valid || task.AutopilotRunID.Valid || task.OriginatorUserID != parseUUID(testUserID) || task.AccountableUserID != task.OriginatorUserID || task.OriginatorSource.String != "direct_human" {
		t.Fatalf("wrong execution semantics: %+v", task)
	}
	workspaceID, err := testHandler.TaskService.ResolveTaskWorkspaceIDChecked(context.Background(), task)
	if err != nil || workspaceID != testWorkspaceID {
		t.Fatalf("workspace: %s %v", workspaceID, err)
	}
	if _, ok := service.ParsePublicDelegationContext(task); !ok {
		t.Fatal("missing delegation context")
	}
	// The client lost its accepted response; execution progressed before retry.
	dbfx.Exec(t, "UPDATE agent_task_queue SET status='running' WHERE id=$1", identity.TaskID)
	dbfx.Exec(t, "UPDATE agent SET runtime_id=NULL WHERE id=$1", input.AgentID)
	replay := submitDelegation(t, router, input, key, testUserID, testWorkspaceID, testWorkspaceID, []string{"delegations:write"})
	var recovered publicapiv1.Delegation
	replay.Want(http.StatusOK).JSON(&recovered)
	if recovered.TaskID != identity.TaskID || recovered.DelegationID != identity.DelegationID || recovered.Status != "running" {
		t.Fatalf("retry changed identity/status: %+v", recovered)
	}
	original := input
	input.Task.Instructions += " changed"
	conflict := submitDelegation(t, router, input, key, testUserID, testWorkspaceID, testWorkspaceID, []string{"delegations:write"})
	if conflict.Code != http.StatusConflict {
		t.Fatalf("hash conflict: %d %s", conflict.Code, conflict.Body.String())
	}
	dbfx.Exec(t, "DELETE FROM agent_task_queue WHERE id=$1", identity.TaskID)
	var deleted publicapiv1.Delegation
	submitDelegation(t, router, original, key, testUserID, testWorkspaceID, testWorkspaceID, []string{"delegations:write"}).Want(http.StatusOK).JSON(&deleted)
	if deleted.TaskID != identity.TaskID || deleted.DelegationID != identity.DelegationID || deleted.Status != "unavailable" {
		t.Fatalf("deleted execution was recreated: %+v", deleted)
	}
	if n := dbfx.Count(t, "SELECT count(*) FROM agent_task_queue WHERE agent_id=$1", input.AgentID); n != 0 {
		t.Fatalf("retry recreated %d tasks", n)
	}
}

func TestPublicDelegationValidationCreatesNothing(t *testing.T) {
	for _, name := range []string{"flag disabled", "scope", "workspace", "invocation", "archived", "rebound", "offline", "unsupported", "ownerless", "private runtime ownership", "invalid payload", "missing key"} {
		t.Run(name, func(t *testing.T) {
			h, router, input := delegationFixture(t)
			userID, workspaceID := testUserID, testWorkspaceID
			scopes, key, want := []string{"delegations:write"}, uuid.NewString(), http.StatusConflict
			switch name {
			case "flag disabled":
				h.FeatureFlags = nil
				want = http.StatusNotFound
			case "scope":
				scopes = nil
				want = http.StatusForbidden
			case "workspace":
				workspaceID = uuid.NewString()
				want = http.StatusNotFound
			case "invocation":
				ownerID := dbfx.User(t, "Another owner", uuid.NewString()+"@multica.test")
				dbfx.Exec(t, "UPDATE agent SET owner_id=$2 WHERE id=$1", input.AgentID, ownerID)
				want = http.StatusForbidden
			case "archived":
				dbfx.Exec(t, "UPDATE agent SET archived_at=now() WHERE id=$1", input.AgentID)
			case "rebound":
				dbfx.Exec(t, "UPDATE agent SET runtime_id=NULL WHERE id=$1", input.AgentID)
			case "offline":
				dbfx.Exec(t, "UPDATE agent_runtime SET status='offline' WHERE id=$1", input.ExpectedRuntimeID)
			case "unsupported":
				dbfx.Exec(t, "UPDATE agent_runtime SET metadata='{}' WHERE id=$1", input.ExpectedRuntimeID)
			case "ownerless":
				dbfx.Exec(t, "UPDATE agent_runtime SET owner_id=NULL WHERE id=$1", input.ExpectedRuntimeID)
			case "private runtime ownership":
				ownerID := dbfx.User(t, "Runtime owner", uuid.NewString()+"@multica.test")
				dbfx.Exec(t, "UPDATE agent_runtime SET owner_id=$2 WHERE id=$1", input.ExpectedRuntimeID, ownerID)
			case "invalid payload":
				input.Task.Instructions = ""
				want = http.StatusBadRequest
			case "missing key":
				key = ""
				want = http.StatusBadRequest
			}
			response := submitDelegation(t, router, input, key, userID, testWorkspaceID, workspaceID, scopes)
			if response.Code != want {
				t.Fatalf("status %d want %d: %s", response.Code, want, response.Body.String())
			}
			if n := dbfx.Count(t, "SELECT count(*) FROM public_delegation WHERE agent_id=$1", input.AgentID); n != 0 {
				t.Fatalf("invalid request left %d delegation(s)", n)
			}
			if n := dbfx.Count(t, "SELECT count(*) FROM agent_task_queue WHERE agent_id=$1", input.AgentID); n != 0 {
				t.Fatalf("invalid request left %d task(s)", n)
			}
		})
	}
}

type failedDelegationCommit struct{ pgx.Tx }

func (t failedDelegationCommit) Commit(context.Context) error {
	return errors.New("injected commit failure")
}

type failedDelegationCommitStarter struct{ txStarter }

func (s failedDelegationCommitStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.txStarter.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return failedDelegationCommit{tx}, nil
}

func TestPublicDelegationTransactionRollback(t *testing.T) {
	h, router, input := delegationFixture(t)
	h.TxStarter = failedDelegationCommitStarter{h.TxStarter}
	response := submitDelegation(t, router, input, uuid.NewString(), testUserID, testWorkspaceID, testWorkspaceID, []string{"delegations:write"})
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("commit failure: %d %s", response.Code, response.Body.String())
	}
	for _, table := range []string{"public_delegation", "agent_task_queue"} {
		if n := dbfx.Count(t, "SELECT count(*) FROM "+table+" WHERE agent_id=$1", input.AgentID); n != 0 {
			t.Fatalf("rollback left %d rows in %s", n, table)
		}
	}
}

func TestPublicDelegationRuntimeRebindingRace(t *testing.T) {
	_, router, input := delegationFixture(t)
	tx, err := testPool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(context.Background(), "UPDATE agent SET runtime_id=NULL WHERE id=$1", input.AgentID); err != nil {
		t.Fatal(err)
	}
	responses := make(chan *testutil.Response, 1)
	go func() {
		responses <- submitDelegation(t, router, input, uuid.NewString(), testUserID, testWorkspaceID, testWorkspaceID, []string{"delegations:write"})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var blocked bool
		if err := testPool.QueryRow(context.Background(), "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))", tx.Conn().PgConn().PID()).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("admission did not wait for binding writer")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-responses:
		if response.Code != http.StatusConflict {
			t.Fatalf("rebind race: %d %s", response.Code, response.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("admission did not finish")
	}
	if n := dbfx.Count(t, "SELECT count(*) FROM public_delegation WHERE agent_id=$1", input.AgentID); n != 0 {
		t.Fatalf("rebind race left %d admissions", n)
	}
	if n := dbfx.Count(t, "SELECT count(*) FROM agent_task_queue WHERE agent_id=$1", input.AgentID); n != 0 {
		t.Fatalf("rebind race left %d tasks", n)
	}
}

func TestPublicDelegationAdmissionDoesNotReserveCapacity(t *testing.T) {
	_, router, input := delegationFixture(t)
	dbfx.Task(t, input.AgentID, testutil.Cols{"status": "running", "runtime_id": input.ExpectedRuntimeID})
	response := submitDelegation(t, router, input, uuid.NewString(), testUserID, testWorkspaceID, testWorkspaceID, []string{"delegations:write"})
	var accepted publicapiv1.Delegation
	response.Want(http.StatusAccepted).JSON(&accepted)
	if accepted.Status != "queued" {
		t.Fatalf("busy profile promised %s", accepted.Status)
	}
	if n := dbfx.Count(t, "SELECT count(*) FROM agent_task_queue WHERE agent_id=$1 AND status='running'", input.AgentID); n != 1 {
		t.Fatalf("admission changed running work: %d", n)
	}
}

func TestPublicDelegationKeyIsScopedToMember(t *testing.T) {
	callerID := dbfx.User(t, "Delegation caller", uuid.NewString()+"@multica.test")
	dbfx.Member(t, testWorkspaceID, callerID, "member")
	_, router, input := delegationFixture(t)
	dbfx.Exec(t, "UPDATE agent SET permission_mode='public_to' WHERE id=$1", input.AgentID)
	dbfx.Insert(t, "agent_invocation_target", testutil.Cols{"agent_id": input.AgentID, "target_type": "member", "target_id": callerID})
	key := uuid.NewString()
	var owner, member publicapiv1.Delegation
	submitDelegation(t, router, input, key, testUserID, testWorkspaceID, testWorkspaceID, []string{"delegations:write"}).Want(http.StatusAccepted).JSON(&owner)
	submitDelegation(t, router, input, key, callerID, testWorkspaceID, testWorkspaceID, []string{"delegations:write"}).Want(http.StatusAccepted).JSON(&member)
	if owner.DelegationID == member.DelegationID || owner.TaskID == member.TaskID {
		t.Fatal("members share idempotency identity")
	}
	submitDelegation(t, router, input, key, callerID, testWorkspaceID, testWorkspaceID, []string{"delegations:write"}).Want(http.StatusOK)
}

func TestPublicDelegationClaimPreservesExecutionSemantics(t *testing.T) {
	_, router, input := delegationFixture(t)
	agentID, runtimeID, daemonID := createRuntimeGuardAgent(t, context.Background())
	dbfx.Cleanup(t, "DELETE FROM public_delegation WHERE agent_id=$1", agentID)
	dbfx.Cleanup(t, "DELETE FROM agent_task_queue WHERE agent_id=$1", agentID)
	dbfx.Exec(t, `UPDATE agent_runtime SET metadata=jsonb_set(metadata,'{capabilities}','["public-delegation-v1"]'::jsonb) WHERE id=$1`, runtimeID)
	input.AgentID, input.ExpectedRuntimeID = agentID, runtimeID
	var accepted publicapiv1.Delegation
	submitDelegation(t, router, input, uuid.NewString(), testUserID, testWorkspaceID, testWorkspaceID, []string{"delegations:write"}).Want(http.StatusAccepted).JSON(&accepted)
	req := newDaemonTokenRequest(http.MethodPost, "/api/daemon/runtimes/"+runtimeID+"/claim", nil, testWorkspaceID, daemonID)
	req.Header.Set("X-Client-Capabilities", "public-delegation-v1")
	req = testutil.WithURLParams(req, "runtimeId", runtimeID)
	var response struct {
		Task *AgentTaskResponse `json:"task"`
	}
	testutil.Call(t, testHandler.ClaimTaskByRuntime, req).Want(http.StatusOK).JSON(&response)
	if response.Task == nil {
		t.Fatal("accepted delegation was not claimable")
	}
	task := response.Task
	if task.ID != accepted.TaskID || task.DelegationID != accepted.DelegationID || task.WorkspaceID != testWorkspaceID ||
		task.DelegationTitle != input.Task.Title || task.DelegationInstructions != input.Task.Instructions ||
		task.QuickCreatePrompt != "" || task.IssueID != "" {
		t.Fatalf("claim did not preserve delegation semantics: task=%s delegation=%s", task.ID, task.DelegationID)
	}
}

type delegationCommitGateStarter struct {
	txStarter
	ready  chan uint32
	resume chan struct{}
}

type delegationCommitGate struct {
	pgx.Tx
	ready  chan uint32
	resume chan struct{}
}

func (s delegationCommitGateStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.txStarter.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return delegationCommitGate{tx, s.ready, s.resume}, nil
}

func (tx delegationCommitGate) Commit(ctx context.Context) error {
	tx.ready <- tx.Conn().PgConn().PID()
	select {
	case <-tx.resume:
		return tx.Tx.Commit(ctx)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func waitForDelegationLockWait(t *testing.T, pid uint32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var blocked bool
		if err := testPool.QueryRow(context.Background(), "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))", pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("revocation did not wait for admission transaction")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPublicDelegationRevocationWaitsForAdmission(t *testing.T) {
	for _, mode := range []string{"profile update", "target deletion"} {
		t.Run(mode, func(t *testing.T) {
			callerID := dbfx.User(t, "Delegation caller", uuid.NewString()+"@multica.test")
			dbfx.Member(t, testWorkspaceID, callerID, "member")
			h, router, input := delegationFixture(t)
			dbfx.Exec(t, "UPDATE agent SET permission_mode='public_to' WHERE id=$1", input.AgentID)
			dbfx.Insert(t, "agent_invocation_target", testutil.Cols{"agent_id": input.AgentID, "target_type": "member", "target_id": callerID})
			ready, resume := make(chan uint32, 1), make(chan struct{})
			h.TxStarter = delegationCommitGateStarter{h.TxStarter, ready, resume}
			var once sync.Once
			release := func() { once.Do(func() { close(resume) }) }
			defer release()
			admitted := make(chan *testutil.Response, 1)
			go func() {
				admitted <- submitDelegation(t, router, input, uuid.NewString(), callerID, testWorkspaceID, testWorkspaceID, []string{"delegations:write"})
			}()
			var pid uint32
			select {
			case pid = <-ready:
			case response := <-admitted:
				t.Fatalf("admission stopped before commit: %d %s", response.Code, response.Text())
			case <-time.After(5 * time.Second):
				t.Fatal("admission did not reach commit")
			}
			revoked := make(chan error, 1)
			go func() {
				if mode == "profile update" {
					req := withURLParam(newRequest(http.MethodPut, "/api/agents/"+input.AgentID, map[string]any{"permission_mode": "private"}), "id", input.AgentID)
					response := testutil.Call(t, testHandler.UpdateAgent, req)
					if response.Code != http.StatusOK {
						revoked <- fmt.Errorf("revoke: %d %s", response.Code, response.Text())
						return
					}
					revoked <- nil
				} else {
					_, err := testPool.Exec(context.Background(), "DELETE FROM agent_invocation_target WHERE agent_id=$1", input.AgentID)
					revoked <- err
				}
			}()
			waitForDelegationLockWait(t, pid)
			select {
			case err := <-revoked:
				t.Fatalf("revocation completed before admission commit: %v", err)
			default:
			}
			release()
			select {
			case response := <-admitted:
				response.Want(http.StatusAccepted)
			case <-time.After(5 * time.Second):
				t.Fatal("admission did not commit")
			}
			select {
			case err := <-revoked:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("revocation did not commit")
			}
			submitDelegation(t, router, input, uuid.NewString(), callerID, testWorkspaceID, testWorkspaceID, []string{"delegations:write"}).Want(http.StatusNotFound)
			if n := dbfx.Count(t, "SELECT count(*) FROM public_delegation WHERE agent_id=$1", input.AgentID); n != 1 {
				t.Fatalf("expected exactly one pre-revocation admission, got %d", n)
			}
		})
	}
}

func TestUpdateAgentPermissionReplacementRollsBackTogether(t *testing.T) {
	_, _, input := delegationFixture(t)
	dbfx.Exec(t, "UPDATE agent SET permission_mode='public_to' WHERE id=$1", input.AgentID)
	dbfx.Insert(t, "agent_invocation_target", testutil.Cols{"agent_id": input.AgentID, "target_type": "workspace", "target_id": testWorkspaceID})
	h := *testHandler
	h.TxStarter = failedDelegationCommitStarter{h.TxStarter}
	req := withURLParam(newRequest(http.MethodPut, "/api/agents/"+input.AgentID, map[string]any{"permission_mode": "private"}), "id", input.AgentID)
	testutil.Call(t, h.UpdateAgent, req).Want(http.StatusInternalServerError)
	var mode string
	dbfx.QueryRow(t, "SELECT permission_mode FROM agent WHERE id=$1", input.AgentID).Scan(&mode)
	if mode != "public_to" {
		t.Fatalf("failed update committed permission mode %s", mode)
	}
	if n := dbfx.Count(t, "SELECT count(*) FROM agent_invocation_target WHERE agent_id=$1", input.AgentID); n != 1 {
		t.Fatalf("failed update left %d targets, want original target", n)
	}
}

func TestPublicDelegationRuntimeMismatchIsUniform(t *testing.T) {
	otherOwner := dbfx.User(t, "Private runtime owner", uuid.NewString()+"@multica.test")
	unrelated := dbfx.Runtime(t, "Unrelated private runtime", testutil.Cols{"owner_id": otherOwner})
	foreignWorkspace := dbfx.Workspace(t, "Foreign workspace", "delegation-foreign-"+uuid.NewString())
	foreign := dbfx.Runtime(t, "Foreign private runtime", testutil.Cols{"workspace_id": foreignWorkspace, "owner_id": otherOwner})
	_, router, input := delegationFixture(t)
	bound := input.ExpectedRuntimeID
	var reference publicapiv1.Problem
	for _, id := range []string{uuid.NewString(), unrelated, foreign} {
		input.ExpectedRuntimeID = id
		var problem publicapiv1.Problem
		submitDelegation(t, router, input, uuid.NewString(), testUserID, testWorkspaceID, testWorkspaceID, []string{"delegations:write"}).Want(http.StatusConflict).JSON(&problem)
		if reference.Code == "" {
			reference = problem
		}
		if problem.Code != "runtime_binding_changed" || problem.Code != reference.Code || problem.Detail != reference.Detail {
			t.Fatalf("runtime existence changed problem: %+v vs %+v", problem, reference)
		}
	}
	input.ExpectedRuntimeID = bound
	for _, owner := range []any{nil, otherOwner} {
		dbfx.Exec(t, "UPDATE agent_runtime SET owner_id=$2 WHERE id=$1", bound, owner)
		var problem publicapiv1.Problem
		submitDelegation(t, router, input, uuid.NewString(), testUserID, testWorkspaceID, testWorkspaceID, []string{"delegations:write"}).Want(http.StatusConflict).JSON(&problem)
		if problem.Code != reference.Code || problem.Detail != reference.Detail {
			t.Fatalf("inaccessible bound runtime changed problem: %+v", problem)
		}
	}
	if n := dbfx.Count(t, "SELECT count(*) FROM public_delegation WHERE agent_id=$1", input.AgentID); n != 0 {
		t.Fatalf("runtime mismatch left %d admissions", n)
	}
}

func TestPublicDelegationDeliveryRechecksInvocation(t *testing.T) {
	for _, revoke := range []string{"permission", "membership"} {
		t.Run(revoke, func(t *testing.T) {
			callerID := dbfx.User(t, "Delegation caller", uuid.NewString()+"@multica.test")
			memberID := dbfx.Member(t, testWorkspaceID, callerID, "member")
			_, router, input := delegationFixture(t)
			agentID, runtimeID, daemonID := createRuntimeGuardAgent(t, context.Background())
			dbfx.Cleanup(t, "DELETE FROM public_delegation WHERE agent_id=$1", agentID)
			dbfx.Cleanup(t, "DELETE FROM agent_task_queue WHERE agent_id=$1", agentID)
			dbfx.Exec(t, `UPDATE agent_runtime SET metadata=jsonb_set(metadata,'{capabilities}','["public-delegation-v1"]'::jsonb) WHERE id=$1`, runtimeID)
			dbfx.Exec(t, "UPDATE agent SET permission_mode='public_to' WHERE id=$1", agentID)
			dbfx.Insert(t, "agent_invocation_target", testutil.Cols{"agent_id": agentID, "target_type": "member", "target_id": callerID})
			input.AgentID, input.ExpectedRuntimeID = agentID, runtimeID
			var accepted publicapiv1.Delegation
			submitDelegation(t, router, input, uuid.NewString(), callerID, testWorkspaceID, testWorkspaceID, []string{"delegations:write"}).Want(http.StatusAccepted).JSON(&accepted)
			task, err := testHandler.TaskService.ClaimTaskForRuntime(context.Background(), parseUUID(runtimeID))
			if err != nil || task == nil {
				t.Fatalf("claim: %v task=%v", err, task)
			}
			if revoke == "permission" {
				req := withURLParam(newRequest(http.MethodPut, "/api/agents/"+agentID, map[string]any{"permission_mode": "private"}), "id", agentID)
				testutil.Call(t, testHandler.UpdateAgent, req).Want(http.StatusOK)
			} else {
				dbfx.Exec(t, "DELETE FROM member WHERE id=$1", memberID)
			}
			req := newDaemonTokenRequest(http.MethodPost, "/api/daemon/runtimes/"+runtimeID+"/claim", nil, testWorkspaceID, daemonID)
			req.Header.Set("X-Client-Capabilities", "public-delegation-v1")
			response, _, _, _, failure, err := testHandler.finalizeClaimDeliveryForTest(req, task, runtimeID, testWorkspaceID)
			if err != nil {
				t.Fatal(err)
			}
			if failure == nil || failure.outcome != "error_delegation_invocation_not_allowed" || !failure.settled {
				t.Fatalf("revoked delegation was not rejected: %+v", failure)
			}
			if response.AuthToken != "" {
				t.Fatal("revoked delegation received task credentials")
			}
			if n := dbfx.Count(t, "SELECT count(*) FROM task_token WHERE task_id=$1", accepted.TaskID); n != 0 {
				t.Fatalf("revoked delegation minted %d tokens", n)
			}
		})
	}
}
