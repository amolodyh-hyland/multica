package publicapiv1

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenAPICoversCapabilityLedger(t *testing.T) {
	var doc struct {
		OpenAPI string `yaml:"openapi"`
		Info    struct {
			Version string `yaml:"version"`
		} `yaml:"info"`
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(OpenAPI(), &doc); err != nil {
		t.Fatalf("parse embedded OpenAPI: %v", err)
	}
	if doc.OpenAPI != "3.1.0" || doc.Info.Version != "v1" {
		t.Fatalf("unexpected contract version: openapi=%q info.version=%q", doc.OpenAPI, doc.Info.Version)
	}

	want := make(map[string]map[string]bool)
	for _, operation := range Operations {
		methods := want[operation.Path]
		if methods == nil {
			methods = make(map[string]bool)
			want[operation.Path] = methods
		}
		methods[strings.ToLower(operation.Method)] = true
	}
	if len(doc.Paths) != len(want) {
		t.Fatalf("OpenAPI path count = %d, ledger path count = %d", len(doc.Paths), len(want))
	}
	for path, methods := range want {
		pathItem, ok := doc.Paths[path]
		if !ok {
			t.Errorf("OpenAPI is missing %s", path)
			continue
		}
		for method := range methods {
			if _, ok := pathItem[method]; !ok {
				t.Errorf("OpenAPI is missing %s %s", strings.ToUpper(method), path)
			}
		}
	}
	for _, operation := range Operations {
		method := strings.ToLower(operation.Method)
		raw, ok := doc.Paths[operation.Path][method].(map[string]any)
		if !ok {
			t.Errorf("OpenAPI operation %s %s is not an object", operation.Method, operation.Path)
			continue
		}
		if got, _ := raw["x-multica-contract"].(string); got != string(operation.Contract) {
			t.Errorf("%s %s x-multica-contract = %q, want %q", operation.Method, operation.Path, got, operation.Contract)
		}
		gotScope, _ := raw["x-multica-scope"].(string)
		if gotScope != operation.Policy.Scope {
			t.Errorf("%s %s x-multica-scope = %q, want %q", operation.Method, operation.Path, gotScope, operation.Policy.Scope)
		}
		if operation.Contract == ContractSharedResource && !hasWorkspaceRoles(raw["x-multica-workspace-roles"], operation.Policy.WorkspaceRoles) {
			t.Errorf("%s %s workspace roles do not match the ledger", operation.Method, operation.Path)
		}
		if gotAudit, _ := raw["x-multica-audit"].(string); gotAudit != string(operation.Policy.Audit) {
			t.Errorf("%s %s x-multica-audit = %q, want %q", operation.Method, operation.Path, gotAudit, operation.Policy.Audit)
		}
		if !hasRateLimitProfiles(raw["x-multica-rate-limit-profiles"], operation.Policy.RateLimits) {
			t.Errorf("%s %s rate-limit profiles do not match the ledger", operation.Method, operation.Path)
		}
		if operation.Contract == ContractSharedResource {
			security, ok := raw["security"].([]any)
			if !ok || !hasSecurityScheme(security, "memberPAT") {
				t.Errorf("%s %s does not declare member PAT security", operation.Method, operation.Path)
			}
			servers, ok := raw["servers"].([]any)
			if !ok || !hasServer(servers, "https://api.multica.ai/api/v1") {
				t.Errorf("%s %s does not declare the member API server", operation.Method, operation.Path)
			}
			if !containsCredential(operation.Policy.Credentials, CredentialPersonalAccess) {
				t.Errorf("%s %s does not declare PAT credentials in the ledger", operation.Method, operation.Path)
			}
			if containsCredential(operation.Policy.Credentials, CredentialUserOAuth) {
				t.Errorf("%s %s declares OAuth before a verifier is implemented", operation.Method, operation.Path)
			}
		}
	}
	if _, exposed := doc.Paths["/hooks/{hook_key}"]; exposed {
		t.Fatal("Plugin-only person hook leaked into the public contract")
	}
}

func hasWorkspaceRoles(raw any, want []string) bool {
	got, ok := raw.([]any)
	if !ok || len(got) != len(want) {
		return false
	}
	for i, role := range want {
		if got[i] != role {
			return false
		}
	}
	return true
}

func TestDeclaredUserScopesComeFromTheCapabilityLedger(t *testing.T) {
	want := []string{"agents:read", "comments:read", "comments:write", "delegations:read", "delegations:results:read", "delegations:write", "issues:read", "issues:write", "runtimes:read"}
	got := DeclaredUserScopes()
	if len(got) != len(want) {
		t.Fatalf("declared user scopes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("declared user scopes = %v, want %v", got, want)
		}
	}
	validated, err := ValidateUserScopes([]string{"issues:read", "comments:write", "issues:read"})
	if err != nil {
		t.Fatalf("validate declared scopes: %v", err)
	}
	if len(validated) != 2 || validated[0] != "issues:read" || validated[1] != "comments:write" {
		t.Fatalf("validated scopes = %v", validated)
	}
	if _, err := ValidateUserScopes([]string{"storage:user"}); err == nil {
		t.Fatal("Plugin-only scope was accepted for a member PAT")
	}
	if scopes, err := ValidateUserScopes(nil); err != nil || scopes == nil || len(scopes) != 0 {
		t.Fatalf("empty scopes = %v, error=%v; want a non-nil empty set", scopes, err)
	}
}

func TestUserScopesAreBoundToWorkspaceRoleCapabilities(t *testing.T) {
	for _, role := range []string{"member", "admin", "owner"} {
		for _, scope := range DeclaredUserScopes() {
			if !UserScopeAllowedForRole(scope, role) {
				t.Errorf("workspace role %q cannot grant declared member capability %q", role, scope)
			}
		}
	}
	if _, err := ValidateUserScopesForRole([]string{"issues:write"}, "unknown"); err == nil {
		t.Fatal("valid scope was granted to a role with no ledger capabilities")
	}
	if _, err := ValidateUserScopesForRole([]string{"workspace:admin"}, "member"); err == nil {
		t.Fatal("undeclared elevated scope was granted to a member")
	}
}

func containsCredential(credentials []CredentialKind, want CredentialKind) bool {
	for _, credential := range credentials {
		if credential == want {
			return true
		}
	}
	return false
}

func hasSecurityScheme(security []any, want string) bool {
	for _, item := range security {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if _, ok := entry[want]; ok {
			return true
		}
	}
	return false
}

func hasServer(servers []any, want string) bool {
	for _, item := range servers {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if url, _ := entry["url"].(string); url == want {
			return true
		}
	}
	return false
}

func hasRateLimitProfiles(raw any, want []RateLimitProfile) bool {
	profiles, ok := raw.([]any)
	if !ok || len(profiles) != len(want) {
		return false
	}
	got := make(map[string]bool, len(profiles))
	for _, profile := range profiles {
		value, ok := profile.(string)
		if !ok {
			return false
		}
		got[value] = true
	}
	for _, profile := range want {
		if !got[string(profile)] {
			return false
		}
	}
	return true
}
