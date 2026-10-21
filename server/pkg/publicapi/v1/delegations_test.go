package publicapiv1

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDelegationPayloadIdentity(t *testing.T) {
	raw := `{"agent_id":"AAAAAAAA-AAAA-4AAA-AAAA-AAAAAAAAAAAA","expected_runtime_id":"bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb","source":{"system":"ao","project_id":"project","task_id":"task"},"task":{"title":"Review","instructions":"Inspect the change"}}`
	var request CreateDelegationRequest
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		t.Fatal(err)
	}
	hash, err := request.PayloadHash()
	if err != nil {
		t.Fatal(err)
	}
	var reordered CreateDelegationRequest
	encoded, _ := json.MarshalIndent(request, "", "  ")
	if err := json.Unmarshal(encoded, &reordered); err != nil {
		t.Fatal(err)
	}
	same, err := reordered.PayloadHash()
	if err != nil || same != hash {
		t.Fatalf("canonical retry changed hash: %s %s %v", hash, same, err)
	}
	for _, tc := range []struct {
		name   string
		change func(*CreateDelegationRequest)
	}{
		{"instructions", func(r *CreateDelegationRequest) { r.Task.Instructions += " " }},
		{"title", func(r *CreateDelegationRequest) { r.Task.Title += "x" }},
		{"project", func(r *CreateDelegationRequest) { r.Source.ProjectID += "x" }},
		{"source task", func(r *CreateDelegationRequest) { r.Source.TaskID += "x" }},
		{"system", func(r *CreateDelegationRequest) { r.Source.System += "x" }},
		{"runtime", func(r *CreateDelegationRequest) { r.ExpectedRuntimeID = r.AgentID }},
		{"agent", func(r *CreateDelegationRequest) { r.AgentID = r.ExpectedRuntimeID }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := request
			tc.change(&changed)
			got, err := changed.PayloadHash()
			if err != nil || got == hash {
				t.Fatalf("payload change not detected: %s %v", got, err)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*CreateDelegationRequest)
	}{
		{"invalid UUID", func(r *CreateDelegationRequest) { r.AgentID = "no" }},
		{"nil runtime", func(r *CreateDelegationRequest) { r.ExpectedRuntimeID = "00000000-0000-0000-0000-000000000000" }},
		{"empty instructions", func(r *CreateDelegationRequest) { r.Task.Instructions = " \n " }},
		{"long instructions", func(r *CreateDelegationRequest) { r.Task.Instructions = strings.Repeat("x", 65537) }},
		{"missing source", func(r *CreateDelegationRequest) { r.Source = DelegationSource{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := request
			tc.change(&changed)
			if _, err := changed.PayloadHash(); err == nil {
				t.Fatal("invalid payload accepted")
			}
		})
	}
}

func TestDelegationOperationIsMemberOnly(t *testing.T) {
	found := false
	for _, operation := range Operations {
		if operation.Path != PathDelegations {
			continue
		}
		found = true
		if operation.Method != "POST" || operation.Policy.Scope != "delegations:write" ||
			operation.Policy.Risk != RiskHigh || len(operation.Policy.Credentials) != 1 ||
			operation.Policy.Credentials[0] != CredentialPersonalAccess {
			t.Fatalf("wrong delegation policy: %+v", operation)
		}
	}
	if !found {
		t.Fatal("delegation operation is not registered")
	}
}
