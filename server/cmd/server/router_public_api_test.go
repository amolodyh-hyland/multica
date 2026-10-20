package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/handler"
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
