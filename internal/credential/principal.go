// Package credential is the single front door for turning any bearer credential
// presented to the HTTP API into an authenticated Principal, and for enforcing
// what that principal may do.
//
// Anti-scatter rule: all credential resolution and authorization lives here.
// Per-kind differences are thin sub-resolvers (pat.go, scoped.go, internal.go),
// not logic sprinkled across handlers. Adding a scope touches scopes.go;
// changing the token format touches mint.go; changing authz touches enforce.go;
// adding a token kind adds one sub-resolver and leaves the front door intact.
package credential

import "time"

// Kind identifies which credential family a Principal was resolved from.
type Kind string

const (
	// KindPAT is a user-owned personal access token (stella_pat_).
	KindPAT Kind = "pat"
	// KindOAuth is an OAuth2 access token (stella_oat_). Reserved for #613.
	KindOAuth Kind = "oauth"
)

// Principal is the single output type of Resolve: the authenticated identity
// plus the authorization inputs Enforce needs. Identity fields are a snapshot
// taken at resolve time so callers can build their request context without a
// second user lookup.
type Principal struct {
	Kind   Kind
	UserID string
	// Scopes is the granted API-permission scope set (resource:action / resource:*).
	Scopes []string

	// Identity snapshot.
	Username  string
	Email     string
	Name      string
	AvatarURL string
	Role      string
	IsAdmin   bool
}

// Identity is the user snapshot a backend returns for a resolved credential.
type Identity struct {
	UserID    string
	Username  string
	Email     string
	Name      string
	AvatarURL string
	Role      string
	IsAdmin   bool
	IsActive  bool
}

// PATRecord is the storage-agnostic shape of a personal_access_token row.
type PATRecord struct {
	ID         string
	PublicID   string
	UserID     string
	Name       string
	TokenHash  string
	Last4      string
	Scopes     []string
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
}

// OAuthAccessRecord is the storage-agnostic shape of an oauth_access_token row.
// It is the opaque stella_oat_ credential that resolves to a Principal{Kind:oauth}
// through the same front door as a PAT. Access tokens are NEVER JWTs.
type OAuthAccessRecord struct {
	ID              string
	PublicID        string
	TokenHash       string
	Last4           string
	ClientID        string
	UserID          string
	Scopes          []string
	RefreshFamilyID string // the refresh family this token belongs to; always set
	ExpiresAt       time.Time
	LastUsedAt      *time.Time
	// FamilyRevokedAt is the revoked_at of the token's refresh family (nil while
	// live). Revocation is a single flag on the family, checked here at read time
	// so an access token can never outlive its revoked family.
	FamilyRevokedAt *time.Time
	CreatedAt       time.Time
}
