package publicapiv1_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/middleware"
	publicapiv1 "github.com/multica-ai/multica/server/pkg/publicapi/v1"
)

type sharedDelegationContract struct {
	Version        int                                 `json:"version"`
	WorkspaceID    string                              `json:"workspace_id"`
	IdempotencyKey string                              `json:"idempotency_key"`
	Profile        publicapiv1.Agent                   `json:"profile"`
	Runtime        publicapiv1.Runtime                 `json:"runtime"`
	Payload        publicapiv1.CreateDelegationRequest `json:"payload"`
	States         map[string]publicapiv1.Delegation   `json:"states"`
	Scenarios      []struct {
		ID            string `json:"id"`
		Method        string `json:"method"`
		Path          string `json:"path"`
		Scope         string `json:"scope"`
		HTTPStatus    int    `json:"http_status"`
		PolicyCode    string `json:"policy_code"`
		ExpectedPosts int    `json:"expected_posts"`
	} `json:"scenarios"`
}

func TestDelegationSharedContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/multica-delegation-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture sharedDelegationContract
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Version != 1 || len(fixture.Scenarios) != 6 {
		t.Fatal("unsupported or incomplete fixture")
	}
	if peer := os.Getenv("AO_CONTRACT_FIXTURE"); peer != "" {
		other, err := os.ReadFile(peer)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(raw, other) {
			t.Fatal("peer contract fixture differs; update both repositories together")
		}
	}
	assertWire := func(value any, field string) {
		t.Helper()
		var original map[string]json.RawMessage
		if err := json.Unmarshal(raw, &original); err != nil {
			t.Fatal(err)
		}
		var want, got any
		if err := json.Unmarshal(original[field], &want); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s wire contract changed: %s", field, encoded)
		}
	}
	assertWire(fixture.Profile, "profile")
	assertWire(fixture.Runtime, "runtime")
	assertWire(fixture.Payload, "payload")
	assertWire(fixture.States, "states")
	for _, scenario := range fixture.Scenarios {
		t.Run(scenario.ID, func(t *testing.T) {
			if scenario.ID != "policy_denial" {
				found := false
				for _, operation := range publicapiv1.Operations {
					if operation.Path == strings.TrimPrefix(scenario.Path, "/api/v1") && operation.Method == scenario.Method {
						found = true
						if operation.Policy.Scope != scenario.Scope || !reflect.DeepEqual(operation.Policy.Credentials, []publicapiv1.CredentialKind{publicapiv1.CredentialPersonalAccess}) {
							t.Fatal("member scope/credential boundary drifted")
						}
					}
				}
				if !found {
					t.Fatalf("operation missing: %s %s", scenario.Method, scenario.Path)
				}
			}
			switch scenario.ID {
			case "profile_discovery":
				if !fixture.Profile.CanInvoke || !fixture.Profile.RuntimeBound || fixture.Profile.RuntimeID != fixture.Runtime.RuntimeID || fixture.Profile.WorkspaceID != fixture.WorkspaceID {
					t.Fatal("picker target is not invocable/bound")
				}
			case "confirmed_submission", "idempotent_retry":
				input := fixture.Payload
				hash, err := input.PayloadHash()
				if err != nil {
					t.Fatal(err)
				}
				retry := fixture.Payload
				same, err := retry.PayloadHash()
				if err != nil || hash != same {
					t.Fatal("exact retry changed payload identity")
				}
				retry.Task.Instructions += " changed"
				changed, err := retry.PayloadHash()
				if err != nil || hash == changed {
					t.Fatal("changed brief reused payload identity")
				}
				if fixture.IdempotencyKey == "" || fixture.States["queued"].Status != "queued" || scenario.HTTPStatus != map[string]int{"confirmed_submission": 202, "idempotent_retry": 200}[scenario.ID] {
					t.Fatal("admission/replay contract changed")
				}
			case "status_reconciliation", "cancellation":
				for state, wire := range fixture.States {
					status, reason, terminal := publicapiv1.DelegationState(state, "")
					if wire.Status != status || wire.Terminal != terminal || wire.FailureReason != reason || wire.Revision != publicapiv1.DelegationRevision(wire.TaskID, status, reason) {
						t.Fatalf("state/revision projection drifted: %s", state)
					}
					if wire.DelegationID != fixture.States["queued"].DelegationID || wire.TaskID != fixture.States["queued"].TaskID || wire.AgentID != fixture.Payload.AgentID || wire.RuntimeID != fixture.Payload.ExpectedRuntimeID {
						t.Fatal("lifecycle changed accepted identity")
					}
				}
			case "policy_denial":
				if scenario.PolicyCode != "policy_denied" || scenario.ExpectedPosts != 0 {
					t.Fatal("local managed policy must block before any POST")
				}
				called := false
				h := middleware.RequirePublicAPIScope("delegations:write")(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
				req := httptest.NewRequest("POST", "/api/v1/delegations", nil)
				ctx := middleware.WithPublicAPIUserCredential(req.Context(), middleware.PublicAPIUserCredential{UserID: "member", WorkspaceID: fixture.WorkspaceID, Credential: publicapiv1.CredentialPersonalAccess, Scopes: []string{"delegations:read"}})
				out := httptest.NewRecorder()
				h.ServeHTTP(out, req.WithContext(ctx))
				var problem publicapiv1.Problem
				if err := json.Unmarshal(out.Body.Bytes(), &problem); err != nil {
					t.Fatal(err)
				}
				if called || out.Code != 403 || problem.Code != "insufficient_scope" {
					t.Fatal("server scope policy admitted a denied write")
				}
			default:
				t.Fatal("unverified scenario")
			}
		})
	}
}
