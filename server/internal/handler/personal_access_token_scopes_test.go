package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/multica-ai/multica/server/internal/auth"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestCreatePATStoresDeclaredPublicAPIScopes(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantScopes []string
	}{
		{
			name:       "scoped PAT",
			body:       `{"name":"Scoped API token","scopes":["issues:read","comments:write"]}`,
			wantStatus: http.StatusCreated,
			wantScopes: []string{"issues:read", "comments:write"},
		},
		{
			name:       "legacy unscoped PAT remains issuable",
			body:       `{"name":"Legacy API token"}`,
			wantStatus: http.StatusCreated,
			wantScopes: []string{},
		},
		{
			name:       "Plugin-only scope refused",
			body:       `{"name":"Invalid token","scopes":["storage:user"]}`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/tokens", bytes.NewBufferString(tt.body))
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
			stored, err := testHandler.Queries.GetPersonalAccessTokenByHash(context.Background(), auth.HashToken(created.Token))
			if err != nil {
				t.Fatalf("load created PAT: %v", err)
			}
			if !reflect.DeepEqual(stored.Scopes, tt.wantScopes) {
				t.Fatalf("stored scopes = %v, want %v", stored.Scopes, tt.wantScopes)
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
