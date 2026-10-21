package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/pkg/publicapi/v1"
)

func TestMemberPublicAPIRoutesCoverSharedLedger(t *testing.T) {
	router := chi.NewRouter()
	registerMemberPublicAPIRoutes(router, (*handler.Handler)(nil))
	for _, operation := range publicapiv1.Operations {
		if operation.Contract != publicapiv1.ContractSharedResource {
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
