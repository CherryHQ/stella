package oidc

import (
	"context"
	"time"
)

// Store is the authorization-server storage the flow needs: clients, single-use
// authorization codes, refresh families (the revocation unit), and the rotating
// refresh tokens. Opaque access-token creation and resolution do NOT live here --
// those go through credential.Service so the /api front door stays single.
// Revocation is a single flag on the family: killing a family (reuse detection or
// a user disconnect) revokes every access + refresh token under it, enforced at
// resolve time by joining the family. The concrete implementation is
// PostgresStore in store_pg.go.
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
	// RevokeCodesForUserClient burns any outstanding codes for a user+client, so a
	// grant revoke cannot be undone by exchanging an in-flight code.
	RevokeCodesForUserClient(ctx context.Context, userID, clientID string) error

	// Refresh families (the revocation unit).
	CreateFamily(ctx context.Context, userID, clientID string) (familyID string, err error)
	// RevokeFamily kills one family (reuse detection or a single-grant revoke).
	RevokeFamily(ctx context.Context, familyID string) error
	// RevokeFamiliesForUserClient kills every family a user holds for a client
	// (user-initiated app disconnect).
	RevokeFamiliesForUserClient(ctx context.Context, userID, clientID string) error

	// Refresh tokens (rotating within a family).
	CreateRefresh(ctx context.Context, r RefreshCreate) (RefreshRecord, error)
	// GetRefreshByPublicID returns the token joined with its family's revoked
	// state (RefreshRecord.FamilyRevoked), so callers see family revocation at
	// read time without a second query.
	GetRefreshByPublicID(ctx context.Context, publicID string) (RefreshRecord, error)
	// ConsumeRefresh atomically consumes the presented token and links it to its
	// replacement. found=false means it was already consumed (replay); the caller
	// then revokes the family. Family-level revocation is checked separately via
	// GetRefreshByPublicID.
	ConsumeRefresh(ctx context.Context, publicID, replacedByID string) (rec RefreshRecord, found bool, err error)
	ListAuthorizedApps(ctx context.Context, userID string) ([]AuthorizedApp, error)
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

// RefreshRecord is a stored refresh token. FamilyRevoked reflects the joined
// family's revoked state -- the single source of truth for revocation.
type RefreshRecord struct {
	ID            string
	PublicID      string
	TokenHash     string
	ClientID      string
	UserID        string
	Scopes        []string
	FamilyID      string
	ExpiresAt     time.Time
	Consumed      bool
	FamilyRevoked bool
}

// RefreshCreate is the input for persisting a fresh refresh token.
type RefreshCreate struct {
	PublicID  string
	TokenHash string
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
