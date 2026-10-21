package publicapiv1

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestProfileReadDTOsHaveOnlySafeContractFields(t *testing.T) {
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				AdditionalProperties bool           `yaml:"additionalProperties"`
				Properties           map[string]any `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(OpenAPI(), &doc); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		dto    any
		fields []string
	}{
		{"Agent", Agent{InvocationTargets: []InvocationTarget{}}, []string{"agent_id", "workspace_id", "name", "runtime_id", "runtime_bound", "permission_mode", "can_invoke", "invocation_targets", "archived", "availability"}},
		{"Runtime", Runtime{}, []string{"runtime_id", "workspace_id", "availability"}},
		{"InvocationTarget", InvocationTarget{}, []string{"target_type", "target_id"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.dto)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]any
			if err := json.Unmarshal(data, &wire); err != nil {
				t.Fatal(err)
			}
			fields := make([]string, 0, len(wire))
			for key := range wire {
				fields = append(fields, key)
			}
			slices.Sort(fields)
			slices.Sort(tc.fields)
			if !reflect.DeepEqual(fields, tc.fields) {
				t.Fatalf("wire fields = %v, want %v", fields, tc.fields)
			}
			schema, ok := doc.Components.Schemas[tc.name]
			if !ok || schema.AdditionalProperties || len(schema.Properties) != len(tc.fields) {
				t.Fatalf("schema %s does not pin the safe field set", tc.name)
			}
			for _, key := range tc.fields {
				if _, ok := schema.Properties[key]; !ok {
					t.Errorf("schema missing %s", key)
				}
			}
			for _, key := range []string{"instructions", "system_instructions", "conversation_starters", "runtime_config", "custom_env", "custom_args", "mcp_config", "metadata", "device_info", "daemon_id", "owner_id", "last_seen_at", "token", "credentials", "composio_toolkit_allowlist"} {
				if _, present := wire[key]; present {
					t.Errorf("private field %q serialized", key)
				}
			}
		})
	}
}

func TestProfileReadOperationsAreMemberOnly(t *testing.T) {
	var doc struct {
		Paths map[string]struct {
			Get struct {
				Security []map[string]any `yaml:"security"`
				Servers  []struct {
					URL string `yaml:"url"`
				} `yaml:"servers"`
			} `yaml:"get"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(OpenAPI(), &doc); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{PathAgents, PathAgent, PathRuntimes, PathRuntime} {
		var found bool
		for _, operation := range Operations {
			if operation.Path != path {
				continue
			}
			found = true
			if operation.Method != "GET" || !reflect.DeepEqual(operation.Policy.Credentials, []CredentialKind{CredentialPersonalAccess}) || !reflect.DeepEqual(operation.Policy.RateLimits, []RateLimitProfile{RateLimitUserDefault}) {
				t.Errorf("%s is not a member-only read: %+v", path, operation)
			}
		}
		if !found {
			t.Fatalf("operation missing for %s", path)
		}
		op := doc.Paths[path].Get
		if len(op.Security) != 1 || len(op.Security[0]) != 1 {
			t.Fatalf("unexpected security on %s: %v", path, op.Security)
		}
		if _, ok := op.Security[0]["memberPAT"]; !ok {
			t.Errorf("%s accepts another credential", path)
		}
		if len(op.Servers) != 1 || op.Servers[0].URL != "https://api.multica.ai/api/v1" {
			t.Errorf("%s exposes another server", path)
		}
	}
}
