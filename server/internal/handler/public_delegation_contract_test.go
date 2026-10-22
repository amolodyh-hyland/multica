package handler

import (
	"encoding/json"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
	publicapiv1 "github.com/multica-ai/multica/server/pkg/publicapi/v1"
)

func TestDelegationSharedContractHandlers(t *testing.T) {
	if os.Getenv("MULTICA_RUN_DELEGATION_CONTRACT") != "1" {
		t.Skip("opt-in: requires an explicitly configured isolated migrated database")
	}
	raw, err := os.ReadFile("../../pkg/publicapi/v1/testdata/multica-delegation-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Payload   publicapiv1.CreateDelegationRequest `json:"payload"`
		Profile   publicapiv1.Agent                   `json:"profile"`
		States    map[string]publicapiv1.Delegation   `json:"states"`
		Scenarios []struct {
			ID         string `json:"id"`
			HTTPStatus int    `json:"http_status"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	_, router, input := delegationOperationsFixture(t)
	input.Task, input.Source = fixture.Payload.Task, fixture.Payload.Source
	dbfx.Exec(t, "UPDATE agent SET name=$2 WHERE id=$1", input.AgentID, fixture.Profile.Name)
	key := uuid.NewString()
	var accepted publicapiv1.Delegation
	assertProjection := func(t *testing.T, response *testutil.Response, state string) {
		t.Helper()
		want := fixture.States[state]
		want.DelegationID, want.TaskID, want.CreatedAt = accepted.DelegationID, accepted.TaskID, accepted.CreatedAt
		want.WorkspaceID, want.AgentID, want.RuntimeID = testWorkspaceID, input.AgentID, input.ExpectedRuntimeID
		want.Revision = publicapiv1.DelegationRevision(want.TaskID, want.Status, want.FailureReason)
		encoded, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		var actual, expected map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, &expected); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("projection differs: got %s want %s", response.Body.Bytes(), encoded)
		}
	}
	for _, scenario := range fixture.Scenarios {
		if !t.Run(scenario.ID, func(t *testing.T) {
			switch scenario.ID {
			case "profile_discovery":
				profiles := publicProfilesRouter()
				var list publicapiv1.AgentListResponse
				callPublicProfiles(t, profiles, testUserID, testWorkspaceID, testWorkspaceID, "/agents", []string{"agents:read"}).Want(scenario.HTTPStatus).JSON(&list)
				found := false
				for _, profile := range list.Agents {
					if profile.AgentID == input.AgentID {
						found = true
					}
				}
				if !found {
					t.Fatal("accepted target missing from discovery")
				}
				var profile publicapiv1.Agent
				callPublicProfiles(t, profiles, testUserID, testWorkspaceID, testWorkspaceID, "/agents/"+input.AgentID, []string{"agents:read"}).Want(200).JSON(&profile)
				want := fixture.Profile
				want.AgentID, want.RuntimeID, want.WorkspaceID = input.AgentID, input.ExpectedRuntimeID, testWorkspaceID
				if !reflect.DeepEqual(profile, want) {
					t.Fatalf("profile differs: %+v want %+v", profile, want)
				}
			case "confirmed_submission":
				response := submitDelegation(t, router, input, key, testUserID, testWorkspaceID, testWorkspaceID, delegationScopes).Want(scenario.HTTPStatus).JSON(&accepted)
				assertProjection(t, response, "queued")
			case "idempotent_retry":
				assertProjection(t, submitDelegation(t, router, input, key, testUserID, testWorkspaceID, testWorkspaceID, delegationScopes).Want(scenario.HTTPStatus), "queued")
				changed := input
				changed.Task.Instructions += " changed"
				submitDelegation(t, router, changed, key, testUserID, testWorkspaceID, testWorkspaceID, delegationScopes).Want(409)
			case "status_reconciliation":
				for _, state := range []string{"queued", "running", "completed"} {
					dbfx.Exec(t, "UPDATE agent_task_queue SET status=$2, error='PRIVATE_SENTINEL', work_dir='/PRIVATE_SENTINEL' WHERE id=$1", accepted.TaskID, state)
					assertProjection(t, delegationOperation(t, router, "GET", "/delegations/"+accepted.DelegationID, testUserID, testWorkspaceID, delegationScopes, "").Want(200), state)
					var out publicapiv1.DelegationListResponse
					response := delegationOperation(t, router, "GET", "/delegations?idempotency_key="+url.QueryEscape(key), testUserID, testWorkspaceID, delegationScopes, "").Want(scenario.HTTPStatus).JSON(&out)
					if len(out.Delegations) != 1 || out.Delegations[0].DelegationID != accepted.DelegationID || out.Delegations[0].Status != state || strings.Contains(response.Body.String(), "PRIVATE_SENTINEL") {
						t.Fatal("unknown-response recovery changed identity or exposed execution data")
					}
				}
			case "policy_denial":
				submitDelegation(t, router, input, uuid.NewString(), testUserID, testWorkspaceID, testWorkspaceID, []string{"delegations:read"}).Want(403)
			case "cancellation":
				path := "/delegations/" + accepted.DelegationID + "/cancel"
				assertProjection(t, delegationOperation(t, router, "POST", path, testUserID, testWorkspaceID, delegationScopes, "").Want(scenario.HTTPStatus), "completed")
				dbfx.Exec(t, "UPDATE agent_task_queue SET status='running' WHERE id=$1", accepted.TaskID)
				stale := `"` + accepted.Revision + `"`
				delegationOperation(t, router, "POST", path, testUserID, testWorkspaceID, delegationScopes, stale).Want(409)
				for i := 0; i < 2; i++ {
					assertProjection(t, delegationOperation(t, router, "POST", path, testUserID, testWorkspaceID, delegationScopes, "").Want(scenario.HTTPStatus), "cancelled")
				}
			default:
				t.Fatal("unverified scenario")
			}
		}) {
			t.FailNow()
		}
	}
	if dbfx.Count(t, "SELECT count(*) FROM public_delegation WHERE agent_id=$1", input.AgentID) != 1 || dbfx.Count(t, "SELECT count(*) FROM agent_task_queue WHERE agent_id=$1", input.AgentID) != 1 {
		t.Fatal("retry, denial, reconciliation or cancellation duplicated admission")
	}
}
