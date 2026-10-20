package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
	publicapiv1 "github.com/multica-ai/multica/server/pkg/publicapi/v1"
)

func TestIssueCliTokenRejectsNonInteractiveCredentialKinds(t *testing.T) {
	tests := []struct {
		name       string
		credential middleware.AuthCredential
	}{
		{name: "unmarked JWT", credential: middleware.AuthCredential{Kind: middleware.AuthCredentialJWT}},
		{name: "unscoped PAT", credential: middleware.AuthCredential{Kind: middleware.AuthCredentialPersonalAccess}},
		{name: "scoped PAT", credential: middleware.AuthCredential{Kind: middleware.AuthCredentialPersonalAccess, Scopes: []string{"issues:read"}}},
		{name: "task token", credential: middleware.AuthCredential{Kind: middleware.AuthCredentialTaskToken}},
		{name: "Cloud PAT", credential: middleware.AuthCredential{Kind: middleware.AuthCredentialCloudPAT}},
		{name: "Plugin installation token", credential: middleware.AuthCredential{Kind: middleware.AuthCredentialPluginInstall}},
		{name: "Plugin invocation token", credential: middleware.AuthCredential{Kind: middleware.AuthCredentialPluginInvocation}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := middleware.WithAuthCredential(context.Background(), tt.credential)
			req := httptest.NewRequest(http.MethodPost, "/api/cli-token", nil).WithContext(ctx)
			req.Header.Set("X-User-ID", testUserID)
			response := httptest.NewRecorder()

			(&Handler{}).IssueCliToken(response, req)

			assertAuthProblem(t, response, http.StatusForbidden, "cli_token_issuer_not_allowed")
		})
	}
}

func TestIssueCliTokenRejectsCloudPATBeforePATIssuance(t *testing.T) {
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/pat/verify" {
			t.Errorf("Fleet path = %q, want /api/v1/pat/verify", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"valid":    true,
			"owner_id": testUserID,
		})
	}))
	defer cloud.Close()

	verifier := auth.NewCloudPATVerifier(auth.CloudPATVerifierConfig{
		FleetBaseURL: cloud.URL,
		HTTPClient:   cloud.Client(),
	})
	userAuth := middleware.Auth(testHandler.Queries, nil, verifier, nil)

	cliRequest := httptest.NewRequest(http.MethodPost, "/api/cli-token", nil)
	cliRequest.Header.Set("Authorization", "Bearer mcn_cli_exchange_test")
	cliResponse := httptest.NewRecorder()
	userAuth(http.HandlerFunc(testHandler.IssueCliToken)).ServeHTTP(cliResponse, cliRequest)
	assertAuthProblem(t, cliResponse, http.StatusForbidden, "cli_token_issuer_not_allowed")

	patRequest := httptest.NewRequest(http.MethodPost, "/api/tokens", bytes.NewBufferString(`{"name":"must not be created"}`))
	patRequest.Header.Set("Authorization", "Bearer mcn_cli_exchange_test")
	patResponse := httptest.NewRecorder()
	userAuth(http.HandlerFunc(testHandler.CreatePersonalAccessToken)).ServeHTTP(patResponse, patRequest)
	assertAuthProblem(t, patResponse, http.StatusForbidden, "pat_issuer_not_allowed")
}

func assertAuthProblem(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, status, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != publicapiv1.ProblemContentType {
		t.Fatalf("Content-Type = %q, want %q", got, publicapiv1.ProblemContentType)
	}
	var problem publicapiv1.Problem
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem: %v; body=%s", err, response.Body.String())
	}
	if problem.Status != status || problem.Code != code || problem.RequestID == "" {
		t.Fatalf("problem = %+v, want status=%d code=%q and request id", problem, status, code)
	}
}
