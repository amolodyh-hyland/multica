package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestCreatePATStoresDeclaredPublicAPIScopes(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantScopes []string
		issuerKind middleware.AuthCredentialKind
	}{
		{
			name:       "scoped PAT",
			body:       `{"name":"Scoped API token","scopes":["issues:read","comments:write"],"workspace_id":"` + testWorkspaceID + `"}`,
			wantStatus: http.StatusCreated,
			wantScopes: []string{"issues:read", "comments:write"},
			issuerKind: middleware.AuthCredentialSessionJWT,
		},
		{
			name:       "legacy unscoped PAT remains issuable",
			body:       `{"name":"Legacy API token"}`,
			wantStatus: http.StatusCreated,
			wantScopes: []string{},
			issuerKind: middleware.AuthCredentialPersonalAccess,
		},
		{
			name:       "Plugin-only scope refused",
			body:       `{"name":"Invalid token","scopes":["storage:user"]}`,
			wantStatus: http.StatusBadRequest,
			issuerKind: middleware.AuthCredentialSessionJWT,
		},
		{
			name:       "scope amplification attempt refused",
			body:       `{"name":"Invalid token","workspace_id":"` + testWorkspaceID + `","scopes":["issues:write","workspace:admin"]}`,
			wantStatus: http.StatusBadRequest,
			issuerKind: middleware.AuthCredentialSessionJWT,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := middleware.WithAuthCredential(context.Background(), middleware.AuthCredential{Kind: tt.issuerKind})
			req := httptest.NewRequest(http.MethodPost, "/api/tokens", bytes.NewBufferString(tt.body)).WithContext(ctx)
			req.Header.Set("X-User-ID", testUserID)
			response := httptest.NewRecorder()
			testHandler.CreatePersonalAccessToken(response, req)
			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, tt.wantStatus, response.Body.String())
			}
			if tt.wantStatus != http.StatusCreated {
				return
			}

			var created CreatePATResponse
			if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
				t.Fatalf("decode PAT response: %v", err)
			}
			if !reflect.DeepEqual(created.Scopes, tt.wantScopes) {
				t.Fatalf("response scopes = %v, want %v", created.Scopes, tt.wantScopes)
			}
			if len(tt.wantScopes) > 0 && created.WorkspaceID != testWorkspaceID {
				t.Fatalf("response workspace_id = %q, want %s", created.WorkspaceID, testWorkspaceID)
			}
			stored, err := testHandler.Queries.GetPersonalAccessTokenByHash(context.Background(), auth.HashToken(created.Token))
			if err != nil {
				t.Fatalf("load created PAT: %v", err)
			}
			if !reflect.DeepEqual(stored.Scopes, tt.wantScopes) {
				t.Fatalf("stored scopes = %v, want %v", stored.Scopes, tt.wantScopes)
			}
			if len(tt.wantScopes) > 0 && (!stored.ScopeWorkspaceID.Valid || uuidToString(stored.ScopeWorkspaceID) != testWorkspaceID) {
				t.Fatalf("stored workspace binding = %v, want %s", stored.ScopeWorkspaceID, testWorkspaceID)
			}
			if len(tt.wantScopes) == 0 && stored.ScopeWorkspaceID.Valid {
				t.Fatalf("unscoped legacy PAT unexpectedly bound to workspace %s", uuidToString(stored.ScopeWorkspaceID))
			}
			t.Cleanup(func() {
				_, _ = testHandler.Queries.RevokePersonalAccessToken(context.Background(), db.RevokePersonalAccessTokenParams{
					ID:     stored.ID,
					UserID: parseUUID(testUserID),
				})
			})
		})
	}
}

func TestCreatePATRejectsWorkspaceOutsideIssuerMembership(t *testing.T) {
	ctx := context.Background()
	var foreignWorkspaceID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug) VALUES ('Foreign PAT workspace', $1) RETURNING id
	`, fmt.Sprintf("foreign-pat-%s", testUserID)).Scan(&foreignWorkspaceID); err != nil {
		t.Fatalf("create foreign workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, foreignWorkspaceID)
	})

	body, err := json.Marshal(CreatePATRequest{
		Name:        "cross-workspace token",
		Scopes:      []string{"issues:read"},
		WorkspaceID: foreignWorkspaceID,
	})
	if err != nil {
		t.Fatalf("encode PAT request: %v", err)
	}
	requestContext := middleware.WithAuthCredential(ctx, middleware.AuthCredential{Kind: middleware.AuthCredentialSessionJWT})
	req := httptest.NewRequest(http.MethodPost, "/api/tokens", bytes.NewReader(body)).WithContext(requestContext)
	req.Header.Set("X-User-ID", testUserID)
	response := httptest.NewRecorder()
	testHandler.CreatePersonalAccessToken(response, req)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", response.Code, response.Body.String())
	}
	var problem struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode stable problem: %v", err)
	}
	if problem.Code != "not_found" {
		t.Fatalf("problem code = %q, want not_found", problem.Code)
	}
}

func TestCreatePATRejectsTaskCloudPluginAndScopedPATIssuers(t *testing.T) {
	tests := []struct {
		name       string
		credential middleware.AuthCredential
		wantCode   string
	}{
		{name: "task token", credential: middleware.AuthCredential{Kind: middleware.AuthCredentialTaskToken}, wantCode: "pat_issuer_not_allowed"},
		{name: "Cloud PAT", credential: middleware.AuthCredential{Kind: middleware.AuthCredentialCloudPAT}, wantCode: "pat_issuer_not_allowed"},
		{name: "Plugin installation token", credential: middleware.AuthCredential{Kind: middleware.AuthCredentialPluginInstall}, wantCode: "pat_issuer_not_allowed"},
		{name: "Plugin invocation token", credential: middleware.AuthCredential{Kind: middleware.AuthCredentialPluginInvocation}, wantCode: "pat_issuer_not_allowed"},
		{name: "scoped PAT", credential: middleware.AuthCredential{Kind: middleware.AuthCredentialPersonalAccess, Scopes: []string{"issues:read"}}, wantCode: "public_api_credential_only"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := middleware.WithAuthCredential(context.Background(), tt.credential)
			req := httptest.NewRequest(http.MethodPost, "/api/tokens", bytes.NewBufferString(`{"name":"must not be created"}`)).WithContext(ctx)
			req.Header.Set("X-User-ID", testUserID)
			response := httptest.NewRecorder()
			(&Handler{}).CreatePersonalAccessToken(response, req)
			if response.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body=%s", response.Code, response.Body.String())
			}
			var problem struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
				t.Fatalf("decode stable problem: %v", err)
			}
			if problem.Code != tt.wantCode {
				t.Fatalf("problem code = %q, want %q", problem.Code, tt.wantCode)
			}
		})
	}
}

func TestCreatePATRequiresAuthenticatedCredentialProvenance(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/tokens", bytes.NewBufferString(`{"name":"must not be created"}`))
	req.Header.Set("X-User-ID", testUserID)
	response := httptest.NewRecorder()
	(&Handler{}).CreatePersonalAccessToken(response, req)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", response.Code, response.Body.String())
	}
}
