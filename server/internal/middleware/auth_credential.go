package middleware

import "context"

type authCredentialContextKey struct{}

type AuthCredentialKind string

const (
	AuthCredentialSessionJWT       AuthCredentialKind = "session_jwt"
	AuthCredentialPersonalAccess   AuthCredentialKind = "personal_access_token"
	AuthCredentialTaskToken        AuthCredentialKind = "task_token"
	AuthCredentialCloudPAT         AuthCredentialKind = "cloud_pat"
	AuthCredentialPluginInstall    AuthCredentialKind = "plugin_installation"
	AuthCredentialPluginInvocation AuthCredentialKind = "plugin_invocation"
)

type AuthCredential struct {
	Kind             AuthCredentialKind
	Scopes           []string
	ScopeWorkspaceID string
}

func WithAuthCredential(ctx context.Context, credential AuthCredential) context.Context {
	credential.Scopes = append([]string{}, credential.Scopes...)
	return context.WithValue(ctx, authCredentialContextKey{}, credential)
}

func AuthCredentialFromContext(ctx context.Context) (AuthCredential, bool) {
	credential, ok := ctx.Value(authCredentialContextKey{}).(AuthCredential)
	if !ok {
		return AuthCredential{}, false
	}
	credential.Scopes = append([]string{}, credential.Scopes...)
	return credential, true
}
