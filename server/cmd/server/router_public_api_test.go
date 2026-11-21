package main

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/pkg/publicapi/v1"
)

func TestMemberPublicAPIRoutesCoverMemberLedger(t *testing.T) {
	router := chi.NewRouter()
	registerMemberPublicAPIRoutes(router, (*handler.Handler)(nil))
	for _, operation := range publicapiv1.Operations {
		if operation.Contract != publicapiv1.ContractSharedResource || !slices.Contains(operation.Policy.Credentials, publicapiv1.CredentialPersonalAccess) {
			continue
		}
		path := strings.ReplaceAll(operation.Path, "{issue_ref}", "MUL-1")
		req := httptest.NewRequest(operation.Method, path, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != http.StatusUnauthorized {
			t.Errorf("%s %s status=%d, want the registered operation's scope middleware (401); body=%s", operation.Method, path, response.Code, response.Body.String())
		}
	}
}

func TestTaskMessagesRemainPluginOnly(t *testing.T) {
	member := chi.NewRouter()
	registerMemberPublicAPIRoutes(member, (*handler.Handler)(nil))
	request := httptest.NewRequest(http.MethodGet, "/tasks/00000000-0000-0000-0000-000000000001/messages", nil)
	ctx := middleware.WithPublicAPIUserCredential(request.Context(), middleware.PublicAPIUserCredential{
		Credential: publicapiv1.CredentialPersonalAccess, Scopes: []string{"tasks:read"},
	})
	response := httptest.NewRecorder()
	member.ServeHTTP(response, request.WithContext(ctx))
	if response.Code != http.StatusNotFound {
		t.Fatalf("plugin-only task messages exposed to a member credential: status=%d", response.Code)
	}

	plugin := chi.NewRouter()
	registerPluginActionRoutes(plugin, (*handler.Handler)(nil))
	found := false
	if err := chi.Walk(plugin, func(method, path string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		found = found || method == http.MethodGet && path == publicapiv1.PathTaskMessages
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("task messages missing from the Plugin surface")
	}
}

func TestMemberProfileReadsRequireTheirOwnScope(t *testing.T) {
	router := chi.NewRouter()
	registerMemberPublicAPIRoutes(router, (*handler.Handler)(nil))
	for _, path := range []string{"/agents", "/agents/00000000-0000-0000-0000-000000000001", "/runtimes", "/runtimes/00000000-0000-0000-0000-000000000001"} {
		ctx := middleware.WithPublicAPIUserCredential(httptest.NewRequest(http.MethodGet, path, nil).Context(), middleware.PublicAPIUserCredential{
			Credential: publicapiv1.CredentialPersonalAccess, Scopes: []string{"issues:read"},
		})
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx))
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"insufficient_scope"`) {
			t.Errorf("%s: status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestPluginRoutesDoNotExposeProfileReads(t *testing.T) {
	router := chi.NewRouter()
	registerPluginActionRoutes(router, (*handler.Handler)(nil))
	if err := chi.Walk(router, func(_ string, path string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		for _, privatePath := range []string{publicapiv1.PathAgents, publicapiv1.PathAgent, publicapiv1.PathRuntimes, publicapiv1.PathRuntime} {
			if path == privatePath {
				t.Errorf("member-only operation exposed on Plugin surface: %s", path)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDelegationOperationsRequireTheirOwnScope(t *testing.T) {
	router := chi.NewRouter()
	registerMemberPublicAPIRoutes(router, (*handler.Handler)(nil))
	for _, operation := range publicapiv1.Operations {
		if !strings.HasPrefix(operation.Path, "/delegations") {
			continue
		}
		wrongScope := "delegations:write"
		if operation.Method == "POST" {
			wrongScope = "delegations:read"
		}
		path := strings.ReplaceAll(operation.Path, "{delegation_id}", "00000000-0000-0000-0000-000000000001")
		req := httptest.NewRequest(operation.Method, path, nil)
		ctx := middleware.WithPublicAPIUserCredential(req.Context(), middleware.PublicAPIUserCredential{Credential: publicapiv1.CredentialPersonalAccess, Scopes: []string{wrongScope}})
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req.WithContext(ctx))
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"insufficient_scope"`) {
			t.Fatalf("%s %s: %d %s", operation.Method, path, response.Code, response.Body.String())
		}
	}
}

func TestPluginRoutesDoNotExposeDelegations(t *testing.T) {
	router := chi.NewRouter()
	registerPluginActionRoutes(router, (*handler.Handler)(nil))
	if err := chi.Walk(router, func(_ string, path string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(path, "/delegations") {
			t.Errorf("member delegation exposed to Plugins: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
