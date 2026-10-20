package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	publicapiv1 "github.com/multica-ai/multica/server/pkg/publicapi/v1"
)

func TestPluginActionRouteTrustBoundaries(t *testing.T) {
	tests := []struct {
		name              string
		method            string
		path              string
		authorization     string
		scopedPAT         bool
		wantStatus        int
		wantHandler       bool
		wantProblem       bool
		wantProblemCode   string
		wantProblemDetail string
	}{
		{
			name:       "public API rejects a missing token before the handler",
			path:       "/v1/context",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:          "public API rejects a browser session token",
			path:          "/v1/context",
			authorization: "Bearer " + testToken,
			wantStatus:    http.StatusUnauthorized,
		},
		{
			name:          "public API passes plugin tokens to the Action handler",
			path:          "/v1/context",
			authorization: "Bearer mpi_invalid",
			wantHandler:   true,
		},
		{
			name:          "surface bridge accepts a browser session",
			path:          "/api/plugin-bridge/v1/context",
			authorization: "Bearer " + testToken,
			wantHandler:   true,
		},
		{
			name:              "removed legacy prefix is not routed",
			path:              "/api/v1/plugin/context",
			wantStatus:        http.StatusUnauthorized,
			wantProblem:       true,
			wantProblemCode:   "credential_required",
			wantProblemDetail: "a scoped member PAT bearer token is required",
		},
		{
			name:              "removed legacy prefix with a scoped member PAT is not routed",
			path:              "/api/v1/plugin/context",
			scopedPAT:         true,
			wantStatus:        http.StatusNotFound,
			wantProblem:       true,
			wantProblemCode:   "not_found",
			wantProblemDetail: "resource not found",
		},
		{
			name:          "public API does not expose person-triggered hooks",
			path:          "/v1/hooks/summarize",
			authorization: "Bearer mpi_invalid",
			wantStatus:    http.StatusNotFound,
			wantProblem:   true,
		},
		{
			name:          "public API empty resource path uses the problem contract",
			path:          "/v1/issues/",
			authorization: "Bearer mpi_invalid",
			wantStatus:    http.StatusNotFound,
			wantProblem:   true,
		},
		{
			name:          "public API unsupported method uses the problem contract",
			method:        http.MethodPut,
			path:          "/v1/context",
			authorization: "Bearer mpi_invalid",
			wantStatus:    http.StatusMethodNotAllowed,
			wantProblem:   true,
		},
		{
			name:          "surface bridge retains person-triggered hooks",
			path:          "/api/plugin-bridge/v1/hooks/summarize",
			authorization: "Bearer " + testToken,
			wantStatus:    http.StatusMethodNotAllowed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			method := tt.method
			if method == "" {
				method = http.MethodGet
			}
			req, err := http.NewRequest(method, testServer.URL+tt.path, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			authorization := tt.authorization
			if tt.scopedPAT {
				authorization = "Bearer " + createScopedMemberTestPAT(t)
				req.Header.Set("X-Workspace-ID", testWorkspaceID)
			}
			if authorization != "" {
				req.Header.Set("Authorization", authorization)
			}
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer response.Body.Close()
			body, _ := io.ReadAll(response.Body)

			if tt.wantHandler {
				if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusNotFound {
					t.Fatalf("status = %d, request did not reach the Action handler", response.StatusCode)
				}
				return
			}
			if response.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d", response.StatusCode, tt.wantStatus)
			}
			if tt.wantProblem {
				if got := response.Header.Get("Content-Type"); got != publicapiv1.ProblemContentType {
					t.Fatalf("Content-Type = %q, body=%s", got, body)
				}
				var problem publicapiv1.Problem
				if err := json.Unmarshal(body, &problem); err != nil {
					t.Fatalf("decode problem: %v; body=%s", err, body)
				}
				if problem.Status != tt.wantStatus || problem.RequestID == "" {
					t.Fatalf("unexpected problem: %+v", problem)
				}
				if tt.wantProblemCode != "" && problem.Code != tt.wantProblemCode {
					t.Fatalf("problem code = %q, want %q", problem.Code, tt.wantProblemCode)
				}
				if tt.wantProblemDetail != "" && problem.Detail != tt.wantProblemDetail {
					t.Fatalf("problem detail = %q, want %q", problem.Detail, tt.wantProblemDetail)
				}
			}
		})
	}
}

func createScopedMemberTestPAT(t *testing.T) string {
	t.Helper()

	body := []byte(`{"name":"plugin route boundary test","workspace_id":"` + testWorkspaceID + `","scopes":["issues:read"]}`)
	request, err := http.NewRequest(http.MethodPost, testServer.URL+"/api/tokens", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build scoped PAT request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("create scoped PAT: %v", err)
	}
	defer response.Body.Close()
	responseBody, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create scoped PAT status = %d, want %d; body=%s", response.StatusCode, http.StatusCreated, responseBody)
	}
	var created struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(responseBody, &created); err != nil {
		t.Fatalf("decode scoped PAT response: %v; body=%s", err, responseBody)
	}
	if created.ID == "" || created.Token == "" {
		t.Fatalf("created PAT response is missing its id or token: %s", responseBody)
	}

	t.Cleanup(func() {
		revokeRequest, err := http.NewRequest(http.MethodDelete, testServer.URL+"/api/tokens/"+created.ID, nil)
		if err != nil {
			t.Errorf("build scoped PAT revoke request: %v", err)
			return
		}
		revokeRequest.Header.Set("Authorization", "Bearer "+testToken)
		revokeResponse, err := http.DefaultClient.Do(revokeRequest)
		if err != nil {
			t.Errorf("revoke scoped PAT: %v", err)
			return
		}
		defer revokeResponse.Body.Close()
		if revokeResponse.StatusCode != http.StatusNoContent {
			body, _ := io.ReadAll(revokeResponse.Body)
			t.Errorf("revoke scoped PAT status = %d, want %d; body=%s", revokeResponse.StatusCode, http.StatusNoContent, body)
		}
	})

	return created.Token
}

func TestPluginActionBaseURLPrefersDedicatedVersionedBase(t *testing.T) {
	t.Setenv("MULTICA_PLUGIN_API_URL", " https://plugin-api.example.com/v1/ ")

	if got := pluginActionBaseURL("https://api.example.com/"); got != "https://plugin-api.example.com/v1" {
		t.Fatalf("pluginActionBaseURL() = %q", got)
	}
}

func TestPluginActionBaseURLFallsBackToPublicURL(t *testing.T) {
	t.Setenv("MULTICA_PLUGIN_API_URL", "")

	if got := pluginActionBaseURL(" https://api.example.com/ "); got != "https://api.example.com/v1" {
		t.Fatalf("pluginActionBaseURL() = %q", got)
	}
}

func TestPluginActionBaseURLOmittedWithoutPublicOrigin(t *testing.T) {
	t.Setenv("MULTICA_PLUGIN_API_URL", "")

	if got := pluginActionBaseURL(""); got != "" {
		t.Fatalf("pluginActionBaseURL() = %q, want empty", got)
	}
}
