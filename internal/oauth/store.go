package oauth

import (
	"context"
	"time"
)

// Store is the authorization-server storage the flow needs: clients,
// single-use authorization codes, and the rotating refresh-token family, plus
// the access-token cascade-revocation queries. Opaque access-token creation and
// resolution do NOT live here -- those go through credential.Service so the /api
// front door stays single. The concrete implementation adapts sqlc in
// internal/server/oauth_wire.go.
type Store interface {
	// Clients.
	CreateClient(ctx context.Context, c ClientCreate) (Client, error)
	GetClient(ctx context.Context, clientID string) (Client, error)
	ListClientsByOwner(ctx context.Context, ownerUserID string) ([]Client, error)
	UpdateClientSecret(ctx context.Context, clientID, ownerUserID, secretHash string) (int64, error)
	DisableClient(ctx context.Context, clientID, ownerUserID string) (int64, error)

	// Authorization codes (single-use).
	CreateCode(ctx context.Context, c AuthCodeCreate) error
	// ConsumeCode atomically marks a code consumed and returns it. found=false
	// means the code was unknown or already consumed (replay).
	ConsumeCode(ctx context.Context, codeHash string) (code AuthCode, found bool, err error)

	// Refresh tokens (rotating family).
	CreateRefresh(ctx context.Context, r RefreshCreate) (RefreshRecord, error)
	GetRefreshByPublicID(ctx context.Context, publicID string) (RefreshRecord, error)
	// ConsumeRefresh atomically consumes the presented token and links it to its
	// replacement. found=false means it was already consumed or revoked (replay).
	ConsumeRefresh(ctx context.Context, publicID, replacedByID string) (rec RefreshRecord, found bool, err error)
	RevokeRefreshFamily(ctx context.Context, familyID string) (int64, error)
	ListAuthorizedApps(ctx context.Context, userID string) ([]AuthorizedApp, error)
	RevokeGrantForUser(ctx context.Context, userID, clientID string) (int64, error)

	// Access-token cascade revocation (the rows themselves are owned by
	// credential; these are the family/user-client kill switches).
	RevokeAccessByFamily(ctx context.Context, familyID string) (int64, error)
	RevokeAccessForUserClient(ctx context.Context, userID, clientID string) (int64, error)
}

// ClientType values.
const (
	ClientTypeConfidential = "confidential"
	ClientTypePublic       = "public"
)

// Client is a registered third-party application.
type Client struct {
	ClientID         string
	Name             string
	ClientSecretHash string
	ClientType       string
	RedirectURIs     []string
	GrantTypes       []string
	Scopes           []string
	OwnerUserID      string
	Disabled         bool
	CreatedAt        time.Time
}

// IsPublic reports whether the client has no secret and must use PKCE.
func (c Client) IsPublic() bool { return c.ClientType == ClientTypePublic }

// ClientCreate is the input for registering a client.
type ClientCreate struct {
	ClientID         string
	Name             string
	ClientSecretHash string
	ClientType       string
	RedirectURIs     []string
	GrantTypes       []string
	Scopes           []string
	OwnerUserID      string
}

// AuthCode is a stored authorization code (the hash, never the plaintext).
type AuthCode struct {
	ClientID            string
	UserID              string
	RedirectURI         string
	Scopes              []string
	CodeChallenge       string
	CodeChallengeMethod string
	ExpiresAt           time.Time
}

// AuthCodeCreate is the input for persisting a fresh authorization code.
type AuthCodeCreate struct {
	CodeHash            string
	ClientID            string
	UserID              string
	RedirectURI         string
	Scopes              []string
	CodeChallenge       string
	CodeChallengeMethod string
	ExpiresAt           time.Time
}

// RefreshRecord is a stored refresh token.
type RefreshRecord struct {
	ID        string
	PublicID  string
	TokenHash string
	ClientID  string
	UserID    string
	Scopes    []string
	FamilyID  string
	ExpiresAt time.Time
	Consumed  bool
	Revoked   bool
}

// RefreshCreate is the input for persisting a fresh refresh token.
type RefreshCreate struct {
	PublicID  string
	TokenHash string
	Last4     string
	ClientID  string
	UserID    string
	Scopes    []string
	FamilyID  string
	ExpiresAt time.Time
}

// AuthorizedApp is one entry in a user's authorized-apps list.
type AuthorizedApp struct {
	ClientID   string
	ClientName string
	FamilyID   string
	Scopes     []string
	GrantedAt  time.Time
}
