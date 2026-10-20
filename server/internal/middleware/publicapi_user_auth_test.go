package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	publicapiv1 "github.com/multica-ai/multica/server/pkg/publicapi/v1"
)

type publicAPIPATStoreStub struct {
	tokenHash string
	pat       db.PersonalAccessToken
	err       error
}

func (s *publicAPIPATStoreStub) GetPersonalAccessTokenByHash(_ context.Context, hash string) (db.PersonalAccessToken, error) {
	if s.err != nil {
		return db.PersonalAccessToken{}, s.err
	}
	if hash != s.tokenHash {
		return db.PersonalAccessToken{}, pgx.ErrNoRows
	}
	return s.pat, nil
}

func (*publicAPIPATStoreStub) UpdatePersonalAccessTokenLastUsed(context.Context, pgtype.UUID) error {
	return nil
}

func TestPublicAPIUserPATAcceptsScopedMemberPAT(t *testing.T) {
	const token = "mul_public_api_test_token"
	userID := pgtype.UUID{Bytes: [16]byte{1, 2, 3}, Valid: true}
	workspaceID, err := util.ParseUUID("00000000-0000-0000-0000-000000000002")
	if err != nil {
		t.Fatal(err)
	}
	store := &publicAPIPATStoreStub{
		tokenHash: auth.HashToken(token),
		pat: db.PersonalAccessToken{
			UserID:           userID,
			Scopes:           []string{"issues:read", "comments:write"},
			ScopeWorkspaceID: workspaceID,
		},
	}
	called := false
	handler := PublicAPIUserPAT(store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		credential, ok := PublicAPIUserCredentialFromContext(r.Context())
		if !ok {
			t.Error("verified member credential missing from context")
			return
		}
		if credential.UserID != "01020300-0000-0000-0000-000000000000" || credential.Credential != publicapiv1.CredentialPersonalAccess {
			t.Errorf("unexpected member credential: %+v", credential)
		}
		if credential.WorkspaceID != "00000000-0000-0000-0000-000000000002" {
			t.Errorf("workspace binding = %q", credential.WorkspaceID)
		}
		if strings.Join(credential.Scopes, ",") != "issues:read,comments:write" {
			t.Errorf("scopes = %v", credential.Scopes)
		}
		if r.Header.Get("X-User-ID") != credential.UserID {
			t.Errorf("X-User-ID = %q, want authenticated user %q", r.Header.Get("X-User-ID"), credential.UserID)
		}
		if r.Header.Get("X-Actor-Source") != "" || r.Header.Get("X-Agent-ID") != "" || r.Header.Get("X-Task-ID") != "" {
			t.Errorf("untrusted actor headers survived authentication")
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, publicapiv1.MemberBasePath+publicapiv1.PathIssue, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-User-ID", "00000000-0000-0000-0000-000000000099")
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Agent-ID", "00000000-0000-0000-0000-000000000099")
	req.Header.Set("X-Task-ID", "00000000-0000-0000-0000-000000000099")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusNoContent || !called {
		t.Fatalf("status=%d called=%v body=%s", response.Code, called, response.Body.String())
	}
}

func TestPublicAPIUserPATRejectsUnscopedAndInvalidPATs(t *testing.T) {
	const validToken = "mul_unscoped_test_token"
	tests := []struct {
		name  string
		store *publicAPIPATStoreStub
		want  int
		code  string
	}{
		{
			name: "legacy unscoped PAT",
			store: &publicAPIPATStoreStub{
				tokenHash: auth.HashToken(validToken),
				pat:       db.PersonalAccessToken{UserID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}},
			},
			want: http.StatusForbidden,
			code: "scoped_token_required",
		},
		{
			name: "scoped PAT without workspace binding",
			store: &publicAPIPATStoreStub{
				tokenHash: auth.HashToken(validToken),
				pat: db.PersonalAccessToken{
					UserID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
					Scopes: []string{"issues:read"},
				},
			},
			want: http.StatusForbidden,
			code: "workspace_binding_required",
		},
		{
			name:  "invalid PAT",
			store: &publicAPIPATStoreStub{err: pgx.ErrNoRows},
			want:  http.StatusUnauthorized,
			code:  "invalid_token",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, publicapiv1.MemberBasePath+publicapiv1.PathIssue, nil)
			req.Header.Set("Authorization", "Bearer "+validToken)
			response := httptest.NewRecorder()
			PublicAPIUserPAT(tt.store)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("rejected PAT reached the handler")
			})).ServeHTTP(response, req)
			assertPublicAPIProblem(t, response, tt.want, tt.code)
		})
	}
}

func TestPublicAPIUserPATRejectsOtherCredentialKinds(t *testing.T) {
	sessionJWT, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "00000000-0000-0000-0000-000000000001",
		"exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString(auth.JWTSecret())
	if err != nil {
		t.Fatalf("sign session JWT fixture: %v", err)
	}
	tests := []struct {
		name          string
		authorization string
		cookie        string
		code          string
	}{
		{name: "session JWT", authorization: "Bearer " + sessionJWT, code: "unsupported_credential"},
		{name: "task token", authorization: "Bearer mat_task_token", code: "unsupported_credential"},
		{name: "Cloud PAT", authorization: "Bearer mcn_cloud_token", code: "unsupported_credential"},
		{name: "Plugin installation token", authorization: "Bearer mpi_install_token", code: "unsupported_credential"},
		{name: "Plugin callback token", authorization: "Bearer mpc_callback_token", code: "unsupported_credential"},
		{name: "OAuth token is not issued yet", authorization: "Bearer oauth_access_token", code: "unsupported_credential"},
		{name: "wrong auth scheme", authorization: "Basic mul_personal_token", code: "unsupported_credential"},
		{name: "session cookie", cookie: "multica_auth=session", code: "credential_required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, publicapiv1.MemberBasePath+publicapiv1.PathIssue, nil)
			if tt.authorization != "" {
				req.Header.Set("Authorization", tt.authorization)
			}
			if tt.cookie != "" {
				req.Header.Set("Cookie", tt.cookie)
			}
			response := httptest.NewRecorder()
			PublicAPIUserPAT(nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("non-PAT credential reached the handler")
			})).ServeHTTP(response, req)
			assertPublicAPIProblem(t, response, http.StatusUnauthorized, tt.code)
		})
	}
}

func TestRequirePublicAPIScopeDeniesMissingScope(t *testing.T) {
	ctx := WithPublicAPIUserCredential(context.Background(), PublicAPIUserCredential{
		UserID:     "00000000-0000-0000-0000-000000000001",
		Scopes:     []string{"issues:read"},
		Credential: publicapiv1.CredentialPersonalAccess,
	})
	req := httptest.NewRequest(http.MethodPost, publicapiv1.MemberBasePath+publicapiv1.PathIssueComments, nil).WithContext(ctx)
	response := httptest.NewRecorder()
	called := false
	RequirePublicAPIScope("comments:write")(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).ServeHTTP(response, req)
	if called {
		t.Fatal("operation without a granted scope reached the handler")
	}
	assertPublicAPIProblem(t, response, http.StatusForbidden, "insufficient_scope")
}

func TestPublicAPIUserPATReturnsStableProblemEnvelope(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, publicapiv1.MemberBasePath, nil)
	response := httptest.NewRecorder()
	PublicAPIUserPAT(nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("request without credentials reached the handler")
	})).ServeHTTP(response, req)
	assertPublicAPIProblem(t, response, http.StatusUnauthorized, "credential_required")
}

func TestPublicAPIUserRateLimitIsPerCredentialAndUsesStableProblem(t *testing.T) {
	rdb := newRedisTestClient(t)
	handler := PublicAPIUserRateLimit(rdb, 1, time.Minute)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	call := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, publicapiv1.MemberBasePath+publicapiv1.PathIssue, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	if response := call("mul_first_test_pat"); response.Code != http.StatusNoContent {
		t.Fatalf("first call status = %d", response.Code)
	}
	limited := call("mul_first_test_pat")
	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") != "60" {
		t.Fatalf("limited status=%d retry-after=%q body=%s", limited.Code, limited.Header().Get("Retry-After"), limited.Body.String())
	}
	assertPublicAPIProblem(t, limited, http.StatusTooManyRequests, "rate_limited")
	if response := call("mul_second_test_pat"); response.Code != http.StatusNoContent {
		t.Fatalf("different PAT shared budget: status=%d", response.Code)
	}
}

func TestPublicAPIUserRateLimitWithoutRedisFailsOpen(t *testing.T) {
	handler := PublicAPIUserRateLimit(nil, 1, time.Minute)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, publicapiv1.MemberBasePath+publicapiv1.PathIssue, nil)
		req.Header.Set("Authorization", "Bearer mul_test_pat")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != http.StatusNoContent {
			t.Fatalf("nil Redis call %d status = %d", i+1, response.Code)
		}
	}
}

func assertPublicAPIProblem(t *testing.T, response *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, wantStatus, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != publicapiv1.ProblemContentType {
		t.Fatalf("Content-Type = %q, want %q", got, publicapiv1.ProblemContentType)
	}
	var problem publicapiv1.Problem
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if problem.Code != wantCode || problem.Status != wantStatus || problem.RequestID == "" || problem.Detail == "" || problem.Error == "" {
		t.Fatalf("unexpected problem envelope: %+v", problem)
	}
	if problem.Type != "urn:multica:problem:"+wantCode {
		t.Fatalf("problem type = %q", problem.Type)
	}
}
