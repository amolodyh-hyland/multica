package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	publicapiv1 "github.com/multica-ai/multica/server/pkg/publicapi/v1"
)

type publicAPIUserContextKey struct{}

// PublicAPIUserCredential is the authenticated member identity and grant set
// passed from a credential verifier to Public API operations. PAT is the only
// verifier wired today. A future OAuth verifier can populate the same context
// after validating issuer, audience, member identity, and token scopes.
type PublicAPIUserCredential struct {
	UserID     string
	Scopes     []string
	Credential publicapiv1.CredentialKind
}

// WithPublicAPIUserCredential stores a verified user credential for Public API
// handlers. Callers must only pass identity and scopes derived from a verified
// credential, never request headers or body fields.
func WithPublicAPIUserCredential(ctx context.Context, credential PublicAPIUserCredential) context.Context {
	credential.Scopes = append([]string{}, credential.Scopes...)
	return context.WithValue(ctx, publicAPIUserContextKey{}, credential)
}

// PublicAPIUserCredentialFromContext returns the verified member principal, if
// one has been installed by a Public API credential verifier.
func PublicAPIUserCredentialFromContext(ctx context.Context) (PublicAPIUserCredential, bool) {
	credential, ok := ctx.Value(publicAPIUserContextKey{}).(PublicAPIUserCredential)
	if !ok {
		return PublicAPIUserCredential{}, false
	}
	credential.Scopes = append([]string{}, credential.Scopes...)
	return credential, true
}

type publicAPIPATQueryer interface {
	GetPersonalAccessTokenByHash(context.Context, string) (db.PersonalAccessToken, error)
	UpdatePersonalAccessTokenLastUsed(context.Context, pgtype.UUID) error
}

// PublicAPIUserPAT accepts only bearer mul_ PATs with at least one declared
// Public API scope. It deliberately does not call Auth: session JWTs, task
// tokens, Cloud PATs, cookies, and Plugin credentials belong to other trust
// boundaries and receive one stable Public API problem response here.
func PublicAPIUserPAT(queries publicAPIPATQueryer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// These identity/actor headers are server-owned in the App API.
			// Clear client-supplied copies before deriving any member identity.
			for _, name := range []string{"X-User-ID", "X-User-Email", "X-Actor-Source", "X-Agent-ID", "X-Task-ID"} {
				r.Header.Del(name)
			}

			token := BearerToken(r)
			if token == "" {
				code := "credential_required"
				detail := "a scoped member PAT bearer token is required"
				if strings.TrimSpace(r.Header.Get("Authorization")) != "" {
					code = "unsupported_credential"
					detail = "the user Public API accepts scoped member PATs only"
				}
				publicapiv1.WriteProblem(w, r, http.StatusUnauthorized, code, detail)
				return
			}
			if !strings.HasPrefix(token, "mul_") {
				publicapiv1.WriteProblem(w, r, http.StatusUnauthorized, "unsupported_credential", "the user Public API accepts scoped member PATs only")
				return
			}
			if queries == nil {
				publicapiv1.WriteProblem(w, r, http.StatusServiceUnavailable, "service_unavailable", "credential verification is unavailable")
				return
			}

			pat, err := queries.GetPersonalAccessTokenByHash(r.Context(), auth.HashToken(token))
			if err != nil {
				publicapiv1.WriteProblem(w, r, http.StatusUnauthorized, "invalid_token", "the bearer token is invalid or expired")
				return
			}
			userID := util.UUIDToString(pat.UserID)
			if auth.IsTemporarilyDisabledUser(userID, "") {
				publicapiv1.WriteProblem(w, r, http.StatusForbidden, "account_disabled", auth.TemporarilyDisabledUserError)
				return
			}
			if len(pat.Scopes) == 0 {
				publicapiv1.WriteProblem(w, r, http.StatusForbidden, "scoped_token_required", "this PAT has no Public API scopes")
				return
			}

			credential := PublicAPIUserCredential{
				UserID:     userID,
				Scopes:     append([]string{}, pat.Scopes...),
				Credential: publicapiv1.CredentialPersonalAccess,
			}
			r.Header.Set("X-User-ID", userID)
			ctx := WithPublicAPIUserCredential(r.Context(), credential)
			go queries.UpdatePersonalAccessTokenLastUsed(context.Background(), pat.ID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequirePublicAPIScope enforces the scope recorded for an operation in the
// Public API capability ledger.
func RequirePublicAPIScope(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			credential, ok := PublicAPIUserCredentialFromContext(r.Context())
			if !ok {
				publicapiv1.WriteProblem(w, r, http.StatusUnauthorized, "unauthorized", "missing authenticated user credential")
				return
			}
			if credential.Credential != publicapiv1.CredentialPersonalAccess && credential.Credential != publicapiv1.CredentialUserOAuth {
				publicapiv1.WriteProblem(w, r, http.StatusUnauthorized, "unsupported_credential", "the credential is not a member Public API credential")
				return
			}
			if !hasPublicAPIScope(credential.Scopes, scope) {
				publicapiv1.WriteProblem(w, r, http.StatusForbidden, "insufficient_scope", "the credential lacks the "+scope+" scope")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func hasPublicAPIScope(scopes []string, required string) bool {
	for _, scope := range scopes {
		if scope == required {
			return true
		}
	}
	return false
}
