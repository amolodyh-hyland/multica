package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/auth"
	publicapiv1 "github.com/multica-ai/multica/server/pkg/publicapi/v1"
)

func TestCliTokenExchangeRejectsTaskTokenBeforePATIssuance(t *testing.T) {
	var agentID string
	if err := testPool.QueryRow(context.Background(), `
		SELECT id::text FROM agent WHERE workspace_id = $1 LIMIT 1
	`, testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatalf("load fixture agent: %v", err)
	}
	taskID := ensureAgentTask(t, agentID)
	taskToken := mintAgentTaskToken(t, agentID, taskID, testUserID)

	cliResponse := cliTokenExchangeRequest(t, http.MethodPost, "/api/cli-token", taskToken, nil)
	cliBody := assertCliTokenProblem(t, cliResponse, http.StatusForbidden, "cli_token_issuer_not_allowed")
	var exchanged struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(cliBody, &exchanged); err != nil {
		t.Fatalf("decode rejected CLI exchange: %v; body=%s", err, cliBody)
	}
	if exchanged.Token != "" {
		t.Fatal("task token exchange unexpectedly returned a session JWT")
	}

	patResponse := cliTokenExchangeRequest(t, http.MethodPost, "/api/tokens", taskToken, map[string]any{
		"name":         "task token laundering regression",
		"workspace_id": testWorkspaceID,
		"scopes":       []string{"issues:read"},
	})
	assertCliTokenProblem(t, patResponse, http.StatusForbidden, "pat_issuer_not_allowed")
}

func TestCliTokenExchangeSessionCanIssueScopedPAT(t *testing.T) {
	cliResponse := cliTokenExchangeRequest(t, http.MethodPost, "/api/cli-token", testToken, nil)
	if cliResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(cliResponse.Body)
		cliResponse.Body.Close()
		t.Fatalf("CLI exchange status = %d, want 200; body=%s", cliResponse.StatusCode, body)
	}
	var exchanged struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(cliResponse.Body).Decode(&exchanged); err != nil {
		cliResponse.Body.Close()
		t.Fatalf("decode CLI exchange: %v", err)
	}
	cliResponse.Body.Close()
	if exchanged.Token == "" {
		t.Fatal("interactive session exchange returned no CLI token")
	}
	claims, err := auth.ParseSessionToken(exchanged.Token)
	if err != nil || !auth.HasInteractiveSessionProvenance(claims) {
		t.Fatalf("CLI token lacks interactive provenance: claims=%v err=%v", claims, err)
	}

	patResponse := cliTokenExchangeRequest(t, http.MethodPost, "/api/tokens", exchanged.Token, map[string]any{
		"name":         "CLI scoped Public API token",
		"workspace_id": testWorkspaceID,
		"scopes":       []string{"issues:read"},
	})
	if patResponse.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(patResponse.Body)
		patResponse.Body.Close()
		t.Fatalf("PAT creation status = %d, want 201; body=%s", patResponse.StatusCode, body)
	}
	var created struct {
		ID     string   `json:"id"`
		Scopes []string `json:"scopes"`
	}
	if err := json.NewDecoder(patResponse.Body).Decode(&created); err != nil {
		patResponse.Body.Close()
		t.Fatalf("decode scoped PAT: %v", err)
	}
	patResponse.Body.Close()
	if created.ID == "" || len(created.Scopes) != 1 || created.Scopes[0] != "issues:read" {
		t.Fatalf("unexpected scoped PAT response: %+v", created)
	}
	t.Cleanup(func() {
		revokeResponse := cliTokenExchangeRequest(t, http.MethodDelete, "/api/tokens/"+created.ID, testToken, nil)
		defer revokeResponse.Body.Close()
		if revokeResponse.StatusCode != http.StatusNoContent {
			body, _ := io.ReadAll(revokeResponse.Body)
			t.Errorf("revoke scoped PAT status = %d, want 204; body=%s", revokeResponse.StatusCode, body)
		}
	})
}

func cliTokenExchangeRequest(t *testing.T, method, path, bearer string, body any) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode request body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, testServer.URL+path, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+bearer)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Workspace-ID", testWorkspaceID)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	return response
}

func assertCliTokenProblem(t *testing.T, response *http.Response, status int, code string) []byte {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if response.StatusCode != status {
		t.Fatalf("status = %d, want %d; body=%s", response.StatusCode, status, body)
	}
	if got := response.Header.Get("Content-Type"); got != publicapiv1.ProblemContentType {
		t.Fatalf("Content-Type = %q, body=%s", got, body)
	}
	var problem publicapiv1.Problem
	if err := json.Unmarshal(body, &problem); err != nil {
		t.Fatalf("decode problem: %v; body=%s", err, body)
	}
	if problem.Status != status || problem.Code != code || problem.RequestID == "" {
		t.Fatalf("problem = %+v, want status=%d code=%q and request id", problem, status, code)
	}
	return body
}
