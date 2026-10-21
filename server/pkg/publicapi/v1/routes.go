// Package publicapiv1 owns the stable Multica Public API v1 contract.
//
// The same resource contract is exposed through different trust surfaces. The
// Plugin API accepts installation/callback tokens and applies manifest scopes;
// the member API accepts scoped PATs and checks workspace membership. Both
// surfaces reuse these paths, DTOs, and operation semantics.
package publicapiv1

import (
	"fmt"
	"net/http"
	"sort"
)

const BasePath = "/v1"
const MemberBasePath = "/api/v1"

const (
	PathDelegations   = "/delegations"
	PathContext       = "/context"
	PathIssue         = "/issues/{issue_ref}"
	PathIssueComments = "/issues/{issue_ref}/comments"
	PathStorageScope  = "/storage/{scope}"
	PathStorageValue  = "/storage/{scope}/{key}"
	PathAgents        = "/agents"
	PathAgent         = "/agents/{agent_id}"
	PathRuntimes      = "/runtimes"
	PathRuntime       = "/runtimes/{runtime_id}"
)

type ContractKind string

const (
	// ContractSharedResource is a Multica resource contract that can be exposed
	// to multiple credential types through surface-specific authorization.
	ContractSharedResource ContractKind = "shared_resource"
	// ContractPluginExtension belongs to Plugin installations rather than the
	// general Multica resource API.
	ContractPluginExtension ContractKind = "plugin_extension"
)

// Operation is the capability ledger for the currently implemented v1 slice.
// OpenAPI tests pin this registry to openapi.yaml so routes, scopes, and docs
// cannot drift independently.
type Operation struct {
	Method   string
	Path     string
	Contract ContractKind
	Policy   OperationPolicy
}

var sharedCredentials = []CredentialKind{
	CredentialPersonalAccess,
	CredentialPluginInstall,
	CredentialPluginInvocation,
}

var pluginCredentials = []CredentialKind{
	CredentialPluginInstall,
	CredentialPluginInvocation,
}

var sharedRateLimits = []RateLimitProfile{RateLimitUserDefault, RateLimitPluginStrict}
var pluginRateLimits = []RateLimitProfile{RateLimitPluginStrict}
var memberWorkspaceRoles = []string{"member", "admin", "owner"}
var memberCredentials = []CredentialKind{CredentialPersonalAccess}
var memberRateLimits = []RateLimitProfile{RateLimitUserDefault}

var Operations = []Operation{
	{Method: http.MethodPost, Path: PathDelegations, Contract: ContractSharedResource, Policy: OperationPolicy{Credentials: memberCredentials, Scope: "delegations:write", WorkspaceRoles: memberWorkspaceRoles, Risk: RiskHigh, Audit: AuditPlanned, RateLimits: memberRateLimits}},
	{Method: http.MethodGet, Path: PathAgents, Contract: ContractSharedResource, Policy: OperationPolicy{Credentials: memberCredentials, Scope: "agents:read", WorkspaceRoles: memberWorkspaceRoles, Risk: RiskRead, Audit: AuditPlanned, RateLimits: memberRateLimits}},
	{Method: http.MethodGet, Path: PathAgent, Contract: ContractSharedResource, Policy: OperationPolicy{Credentials: memberCredentials, Scope: "agents:read", WorkspaceRoles: memberWorkspaceRoles, Risk: RiskRead, Audit: AuditPlanned, RateLimits: memberRateLimits}},
	{Method: http.MethodGet, Path: PathRuntimes, Contract: ContractSharedResource, Policy: OperationPolicy{Credentials: memberCredentials, Scope: "runtimes:read", WorkspaceRoles: memberWorkspaceRoles, Risk: RiskRead, Audit: AuditPlanned, RateLimits: memberRateLimits}},
	{Method: http.MethodGet, Path: PathRuntime, Contract: ContractSharedResource, Policy: OperationPolicy{Credentials: memberCredentials, Scope: "runtimes:read", WorkspaceRoles: memberWorkspaceRoles, Risk: RiskRead, Audit: AuditPlanned, RateLimits: memberRateLimits}},
	{Method: http.MethodGet, Path: PathContext, Contract: ContractPluginExtension, Policy: OperationPolicy{Credentials: pluginCredentials, Risk: RiskRead, Audit: AuditNotRequired, RateLimits: pluginRateLimits}},
	{Method: http.MethodGet, Path: PathIssue, Contract: ContractSharedResource, Policy: OperationPolicy{Credentials: sharedCredentials, Scope: "issues:read", WorkspaceRoles: memberWorkspaceRoles, Risk: RiskRead, Audit: AuditPlanned, RateLimits: sharedRateLimits}},
	{Method: http.MethodPatch, Path: PathIssue, Contract: ContractSharedResource, Policy: OperationPolicy{Credentials: sharedCredentials, Scope: "issues:write", WorkspaceRoles: memberWorkspaceRoles, Risk: RiskContentWrite, Audit: AuditPlanned, RateLimits: sharedRateLimits}},
	{Method: http.MethodGet, Path: PathIssueComments, Contract: ContractSharedResource, Policy: OperationPolicy{Credentials: sharedCredentials, Scope: "comments:read", WorkspaceRoles: memberWorkspaceRoles, Risk: RiskRead, Audit: AuditPlanned, RateLimits: sharedRateLimits}},
	{Method: http.MethodPost, Path: PathIssueComments, Contract: ContractSharedResource, Policy: OperationPolicy{Credentials: sharedCredentials, Scope: "comments:write", WorkspaceRoles: memberWorkspaceRoles, Risk: RiskContentWrite, Audit: AuditPlanned, RateLimits: sharedRateLimits}},
	{Method: http.MethodGet, Path: PathStorageScope, Contract: ContractPluginExtension, Policy: OperationPolicy{Credentials: pluginCredentials, Risk: RiskRead, Audit: AuditNotRequired, RateLimits: pluginRateLimits}},
	{Method: http.MethodGet, Path: PathStorageValue, Contract: ContractPluginExtension, Policy: OperationPolicy{Credentials: pluginCredentials, Risk: RiskRead, Audit: AuditNotRequired, RateLimits: pluginRateLimits}},
	{Method: http.MethodPut, Path: PathStorageValue, Contract: ContractPluginExtension, Policy: OperationPolicy{Credentials: pluginCredentials, Risk: RiskContentWrite, Audit: AuditPlanned, RateLimits: pluginRateLimits}},
	{Method: http.MethodDelete, Path: PathStorageValue, Contract: ContractPluginExtension, Policy: OperationPolicy{Credentials: pluginCredentials, Risk: RiskContentWrite, Audit: AuditPlanned, RateLimits: pluginRateLimits}},
}

// DeclaredUserScopes returns the stable set of scopes that may be granted to a
// member PAT. The ledger is the only source of accepted scope names.
func DeclaredUserScopes() []string {
	seen := make(map[string]struct{})
	for _, operation := range Operations {
		if operation.Contract != ContractSharedResource || operation.Policy.Scope == "" {
			continue
		}
		for _, credential := range operation.Policy.Credentials {
			if credential == CredentialPersonalAccess {
				seen[operation.Policy.Scope] = struct{}{}
				break
			}
		}
	}
	scopes := make([]string, 0, len(seen))
	for scope := range seen {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	return scopes
}

// ValidateUserScopes rejects scopes that are not declared by a shared
// resource operation. Duplicate entries are collapsed while preserving the
// caller's order; an omitted or empty list remains a valid unscoped PAT for
// compatibility with the existing /api surface.
func ValidateUserScopes(scopes []string) ([]string, error) {
	declared := make(map[string]struct{})
	for _, scope := range DeclaredUserScopes() {
		declared[scope] = struct{}{}
	}
	validated := make([]string, 0, len(scopes))
	seen := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		if _, ok := declared[scope]; !ok {
			return nil, fmt.Errorf("unsupported Public API scope %q", scope)
		}
		if _, duplicate := seen[scope]; duplicate {
			continue
		}
		seen[scope] = struct{}{}
		validated = append(validated, scope)
	}
	return validated, nil
}

// ValidateUserScopesForRole bounds a requested PAT grant to the capabilities
// explicitly assigned to the issuer's workspace role in the ledger.
func ValidateUserScopesForRole(scopes []string, role string) ([]string, error) {
	validated, err := ValidateUserScopes(scopes)
	if err != nil {
		return nil, err
	}
	for _, scope := range validated {
		if !UserScopeAllowedForRole(scope, role) {
			return nil, fmt.Errorf("Public API scope %q is not permitted for workspace role %q", scope, role)
		}
	}
	return validated, nil
}

// UserScopeAllowedForRole fails closed unless every shared-resource
// operation represented by the scope grants it to the issuer's workspace role.
func UserScopeAllowedForRole(scope, role string) bool {
	matched := false
	for _, operation := range Operations {
		if operation.Contract != ContractSharedResource || operation.Policy.Scope != scope || !hasLedgerCredential(operation.Policy.Credentials, CredentialPersonalAccess) {
			continue
		}
		matched = true
		allowed := false
		for _, allowedRole := range operation.Policy.WorkspaceRoles {
			if allowedRole == role {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	return matched
}

func hasLedgerCredential(credentials []CredentialKind, want CredentialKind) bool {
	for _, credential := range credentials {
		if credential == want {
			return true
		}
	}
	return false
}
