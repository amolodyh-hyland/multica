package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/middleware"
	publicapiv1 "github.com/multica-ai/multica/server/pkg/publicapi/v1"
)

func resultFixture(t *testing.T) (http.Handler, publicapiv1.Delegation) {
	t.Helper()
	h, _, input := delegationOperationsFixture(t)
	router := chi.NewRouter()
	router.Use(middleware.RequirePublicAPIWorkspaceMember(h.Queries))
	router.Post("/delegations", h.CreatePublicDelegation)
	router.Get("/delegations/{delegation_id}/result", h.GetPublicDelegationResult)
	var accepted publicapiv1.Delegation
	submitDelegation(t, router, input, uuid.NewString(), testUserID, testWorkspaceID, testWorkspaceID, delegationScopes).Want(202).JSON(&accepted)
	dbfx.Cleanup(t, "DELETE FROM public_delegation_result WHERE delegation_id=$1", accepted.DelegationID)
	dbfx.Exec(t, "UPDATE agent_task_queue SET status='completed', completed_at='2026-10-24T00:00:00Z', result=$2 WHERE id=$1", accepted.TaskID, `{"output":"A bounded answer.","work_dir":"/private","pr_url":"https://private.test","session_id":"private"}`)
	accepted.Status, accepted.Terminal = "completed", true
	accepted.Revision = publicapiv1.DelegationRevision(accepted.TaskID, "completed", "")
	return router, accepted
}

func TestPublicDelegationResultSnapshotAndChanges(t *testing.T) {
	router, d := resultFixture(t)
	path := "/delegations/" + d.DelegationID + "/result"
	scopes := []string{"delegations:results:read"}
	read := func() publicapiv1.DelegationResult {
		var out publicapiv1.DelegationResult
		delegationOperation(t, router, "GET", path, testUserID, testWorkspaceID, scopes, "\""+d.Revision+"\"").Want(200).JSON(&out)
		return out
	}
	a := read()
	if a.Text != "A bounded answer." || a.TaskID != d.TaskID || a.RuntimeID != d.RuntimeID {
		t.Fatalf("wrong projection: %+v", a)
	}
	if b := read(); a != b {
		t.Fatal("same result changed snapshot")
	}
	if dbfx.Count(t, "SELECT count(*) FROM public_delegation_result WHERE delegation_id=$1", d.DelegationID) != 1 {
		t.Fatal("retry duplicated ledger")
	}
	dbfx.Exec(t, "UPDATE agent_task_queue SET result=$2 WHERE id=$1", d.TaskID, `{"output":"Changed answer."}`)
	delegationOperation(t, router, "GET", path, testUserID, testWorkspaceID, scopes, "\""+d.Revision+"\"", a.Revision).Want(409)
	b := read()
	if a.Digest == b.Digest || a.Revision == b.Revision {
		t.Fatal("mutation reused snapshot")
	}
	if dbfx.Count(t, "SELECT count(*) FROM public_delegation_result WHERE delegation_id=$1", d.DelegationID) != 2 {
		t.Fatal("changed result did not create receipt")
	}
}

func TestPublicDelegationResultUnavailableAndAuthorization(t *testing.T) {
	router, d := resultFixture(t)
	path := "/delegations/" + d.DelegationID + "/result"
	scopes := []string{"delegations:results:read"}
	other := dbfx.User(t, "Other", uuid.NewString()+"@multica.test")
	dbfx.Member(t, testWorkspaceID, other, "admin")
	delegationOperation(t, router, "GET", path, other, testWorkspaceID, scopes, "\""+d.Revision+"\"").Want(404)
	delegationOperation(t, router, "GET", path, testUserID, testWorkspaceID, delegationScopes, "\""+d.Revision+"\"").Want(403)
	delegationOperation(t, router, "GET", path, testUserID, uuid.NewString(), scopes, "\""+d.Revision+"\"").Want(404)
	delegationOperation(t, router, "GET", path, testUserID, testWorkspaceID, scopes, "").Want(409)
	delegationOperation(t, router, "GET", path, testUserID, testWorkspaceID, scopes, "\"stale\"").Want(409)
	for _, item := range []struct{ status, output string }{{"running", "answer"}, {"cancelled", ""}, {"completed", ""}, {"completed", string(make([]byte, 32769))}, {"completed", "https://private.test"}, {"completed", "mul_private"}} {
		payload, _ := json.Marshal(map[string]string{"output": item.output})
		dbfx.Exec(t, "UPDATE agent_task_queue SET status=$2,result=$3 WHERE id=$1", d.TaskID, item.status, string(payload))
		revision := publicapiv1.DelegationRevision(d.TaskID, item.status, "")
		delegationOperation(t, router, "GET", path, testUserID, testWorkspaceID, scopes, "\""+revision+"\"").Want(409)
	}
	if dbfx.Count(t, "SELECT count(*) FROM public_delegation_result WHERE delegation_id=$1", d.DelegationID) != 0 {
		t.Fatal("unapproved result entered ledger")
	}
	dbfx.Exec(t, "DELETE FROM agent_task_queue WHERE id=$1", d.TaskID)
	delegationOperation(t, router, "GET", path, testUserID, testWorkspaceID, scopes, "\""+d.Revision+"\"").Want(404)
}
