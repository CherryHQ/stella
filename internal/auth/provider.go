package auth

import (
	"context"
	"net/http"
)

// AuthState carries the PKCE code verifier and CSRF state string between the
// login redirect and the callback. It is embedded in a signed cookie.
type AuthState struct {
	State        string
	CodeVerifier string
	ProviderName string
}

// ExternalIdentity is the normalised identity returned by an AuthProvider after
// a successful callback. It contains all information needed to upsert a User,
// LoginIdentity, Organization, and Membership in ProcessOIDCLogin.
type ExternalIdentity struct {
	Provider  string
	Subject   string
	Email     string
	Name      string
	AvatarURL string
	OrgID     string // from IdP-specific claim (e.g. OIDC_ORG_ID_CLAIM)
	OrgName   string // from IdP-specific claim (e.g. OIDC_ORG_NAME_CLAIM)
	Claims    map[string]any
}

// AuthProvider is the abstraction over all login methods (OIDC, LocalProvider,
// GitHub OAuth, reverse proxy, etc.). Business code only depends on this interface.
type AuthProvider interface {
	// Name returns the stable provider identifier used in route paths and DB records.
	Name() string

	// LoginURL generates the IdP redirect URL for the given state.
	LoginURL(ctx context.Context, state AuthState) (string, error)

	// HandleCallback validates the callback request, exchanges the code for
	// tokens, verifies the ID token, and returns the normalised identity.
	// Implementations must verify email_verified == true and reject missing emails.
	HandleCallback(ctx context.Context, r *http.Request, state AuthState) (*ExternalIdentity, error)
}
