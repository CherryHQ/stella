package oidc

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/credential"
	sqlc "github.com/CherryHQ/stella/pkg/db/sqlc"
)

// PostgresStore adapts the sqlc queries to both credential.OAuthAccessStore (the
// opaque access-token front door) and oidc.Store (the authorization-server
// client/code/refresh storage). One adapter, two interfaces: access tokens stay
// owned by credential, while the flow tables stay in internal/oidc. The
// composition root constructs it once and hands it to credential.NewService (as
// OAuth) and oidc.NewService (as Store).
type PostgresStore struct {
	q *sqlc.Queries
}

// NewPostgresStore builds the OAuth access + authorization-server store over the
// shared pool.
func NewPostgresStore(db *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{q: sqlc.New(db)}
}

var (
	_ credential.OAuthAccessStore = (*PostgresStore)(nil)
	_ Store                       = (*PostgresStore)(nil)
)

// ---- credential.OAuthAccessStore ----

func (o *PostgresStore) CreateOAuthAccess(ctx context.Context, rec credential.OAuthAccessRecord) (credential.OAuthAccessRecord, error) {
	row, err := o.q.CreateOAuthAccessToken(ctx, sqlc.CreateOAuthAccessTokenParams{
		PublicID:        rec.PublicID,
		TokenHash:       rec.TokenHash,
		Last4:           rec.Last4,
		ClientID:        rec.ClientID,
		UserID:          rec.UserID,
		Scopes:          rec.Scopes,
		RefreshFamilyID: rec.RefreshFamilyID,
		ExpiresAt:       rec.ExpiresAt.UTC(),
	})
	if err != nil {
		return credential.OAuthAccessRecord{}, err
	}
	// A freshly created access token's family is live, so FamilyRevokedAt is nil.
	return credential.OAuthAccessRecord{
		ID:              row.ID,
		PublicID:        row.PublicID,
		TokenHash:       row.TokenHash,
		Last4:           row.Last4,
		ClientID:        row.ClientID,
		UserID:          row.UserID,
		Scopes:          row.Scopes,
		RefreshFamilyID: row.RefreshFamilyID,
		ExpiresAt:       row.ExpiresAt.UTC(),
		LastUsedAt:      ptrFromTimestamptz(row.LastUsedAt),
		CreatedAt:       row.CreatedAt.UTC(),
	}, nil
}

func (o *PostgresStore) GetOAuthAccessByPublicID(ctx context.Context, publicID string) (credential.OAuthAccessRecord, error) {
	row, err := o.q.GetOAuthAccessTokenByPublicID(ctx, publicID)
	if err != nil {
		return credential.OAuthAccessRecord{}, err
	}
	return credential.OAuthAccessRecord{
		ID:              row.ID,
		PublicID:        row.PublicID,
		TokenHash:       row.TokenHash,
		Last4:           row.Last4,
		ClientID:        row.ClientID,
		UserID:          row.UserID,
		Scopes:          row.Scopes,
		RefreshFamilyID: row.RefreshFamilyID,
		ExpiresAt:       row.ExpiresAt.UTC(),
		LastUsedAt:      ptrFromTimestamptz(row.LastUsedAt),
		FamilyRevokedAt: ptrFromTimestamptz(row.FamilyRevokedAt),
		CreatedAt:       row.CreatedAt.UTC(),
	}, nil
}

func (o *PostgresStore) TouchOAuthAccessLastUsed(ctx context.Context, id string) (int64, error) {
	return o.q.UpdateOAuthAccessTokenLastUsed(ctx, id)
}

// ---- oidc.Store: clients ----

func (o *PostgresStore) CreateClient(ctx context.Context, c ClientCreate) (Client, error) {
	row, err := o.q.CreateOAuthClient(ctx, sqlc.CreateOAuthClientParams{
		ClientID:         c.ClientID,
		Name:             c.Name,
		ClientSecretHash: c.ClientSecretHash,
		ClientType:       c.ClientType,
		RedirectUris:     c.RedirectURIs,
		GrantTypes:       c.GrantTypes,
		Scopes:           c.Scopes,
		OwnerUserID:      c.OwnerUserID,
	})
	if err != nil {
		return Client{}, err
	}
	return oauthClientFromRow(row), nil
}

func (o *PostgresStore) GetClient(ctx context.Context, clientID string) (Client, error) {
	row, err := o.q.GetOAuthClientByClientID(ctx, clientID)
	if err != nil {
		return Client{}, err
	}
	return oauthClientFromRow(row), nil
}

func (o *PostgresStore) ListClientsByOwner(ctx context.Context, ownerUserID string) ([]Client, error) {
	rows, err := o.q.ListOAuthClientByOwner(ctx, ownerUserID)
	if err != nil {
		return nil, err
	}
	out := make([]Client, 0, len(rows))
	for _, r := range rows {
		out = append(out, oauthClientFromRow(r))
	}
	return out, nil
}

func (o *PostgresStore) UpdateClientSecret(ctx context.Context, clientID, ownerUserID, secretHash string) (int64, error) {
	return o.q.UpdateOAuthClientSecret(ctx, sqlc.UpdateOAuthClientSecretParams{
		ClientID:         clientID,
		ClientSecretHash: secretHash,
		OwnerUserID:      ownerUserID,
	})
}

func (o *PostgresStore) DisableClient(ctx context.Context, clientID, ownerUserID string) (int64, error) {
	return o.q.DisableOAuthClient(ctx, sqlc.DisableOAuthClientParams{ClientID: clientID, OwnerUserID: ownerUserID})
}

func oauthClientFromRow(r sqlc.OauthClient) Client {
	return Client{
		ClientID:         r.ClientID,
		Name:             r.Name,
		ClientSecretHash: r.ClientSecretHash,
		ClientType:       r.ClientType,
		RedirectURIs:     r.RedirectUris,
		GrantTypes:       r.GrantTypes,
		Scopes:           r.Scopes,
		OwnerUserID:      r.OwnerUserID,
		Disabled:         r.DisabledAt.Valid,
		CreatedAt:        r.CreatedAt.UTC(),
	}
}

// ---- oidc.Store: authorization codes ----

func (o *PostgresStore) CreateCode(ctx context.Context, c AuthCodeCreate) error {
	_, err := o.q.CreateOAuthAuthorizationCode(ctx, sqlc.CreateOAuthAuthorizationCodeParams{
		CodeHash:            c.CodeHash,
		ClientID:            c.ClientID,
		UserID:              c.UserID,
		RedirectUri:         c.RedirectURI,
		Scopes:              c.Scopes,
		CodeChallenge:       c.CodeChallenge,
		CodeChallengeMethod: c.CodeChallengeMethod,
		ExpiresAt:           c.ExpiresAt.UTC(),
	})
	return err
}

func (o *PostgresStore) ConsumeCode(ctx context.Context, codeHash string) (AuthCode, bool, error) {
	row, err := o.q.ConsumeOAuthAuthorizationCode(ctx, codeHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthCode{}, false, nil
	}
	if err != nil {
		return AuthCode{}, false, err
	}
	return AuthCode{
		ClientID:            row.ClientID,
		UserID:              row.UserID,
		RedirectURI:         row.RedirectUri,
		Scopes:              row.Scopes,
		CodeChallenge:       row.CodeChallenge,
		CodeChallengeMethod: row.CodeChallengeMethod,
		ExpiresAt:           row.ExpiresAt.UTC(),
	}, true, nil
}

func (o *PostgresStore) RevokeCodesForUserClient(ctx context.Context, userID, clientID string) error {
	_, err := o.q.RevokeOAuthAuthorizationCodesForUserClient(ctx, sqlc.RevokeOAuthAuthorizationCodesForUserClientParams{
		UserID:   userID,
		ClientID: clientID,
	})
	return err
}

// ---- oidc.Store: refresh families (the revocation unit) ----

func (o *PostgresStore) CreateFamily(ctx context.Context, userID, clientID string) (string, error) {
	row, err := o.q.CreateOAuthRefreshFamily(ctx, sqlc.CreateOAuthRefreshFamilyParams{
		UserID:   userID,
		ClientID: clientID,
	})
	if err != nil {
		return "", err
	}
	return row.ID, nil
}

func (o *PostgresStore) RevokeFamily(ctx context.Context, familyID string) error {
	_, err := o.q.RevokeOAuthRefreshFamily(ctx, familyID)
	return err
}

func (o *PostgresStore) RevokeFamiliesForUserClient(ctx context.Context, userID, clientID string) error {
	_, err := o.q.RevokeOAuthRefreshFamiliesForUserClient(ctx, sqlc.RevokeOAuthRefreshFamiliesForUserClientParams{
		UserID:   userID,
		ClientID: clientID,
	})
	return err
}

// ---- oidc.Store: refresh tokens ----

func (o *PostgresStore) CreateRefresh(ctx context.Context, r RefreshCreate) (RefreshRecord, error) {
	row, err := o.q.CreateOAuthRefreshToken(ctx, sqlc.CreateOAuthRefreshTokenParams{
		PublicID:  r.PublicID,
		TokenHash: r.TokenHash,
		ClientID:  r.ClientID,
		UserID:    r.UserID,
		Scopes:    r.Scopes,
		FamilyID:  r.FamilyID,
		ExpiresAt: r.ExpiresAt.UTC(),
	})
	if err != nil {
		return RefreshRecord{}, err
	}
	// A freshly created token's family is live; FamilyRevoked stays false.
	return RefreshRecord{
		ID: row.ID, PublicID: row.PublicID, TokenHash: row.TokenHash,
		ClientID: row.ClientID, UserID: row.UserID, Scopes: row.Scopes,
		FamilyID: row.FamilyID, ExpiresAt: row.ExpiresAt.UTC(),
		Consumed: row.ConsumedAt.Valid,
	}, nil
}

func (o *PostgresStore) GetRefreshByPublicID(ctx context.Context, publicID string) (RefreshRecord, error) {
	row, err := o.q.GetOAuthRefreshTokenByPublicID(ctx, publicID)
	if err != nil {
		return RefreshRecord{}, err
	}
	return RefreshRecord{
		ID: row.ID, PublicID: row.PublicID, TokenHash: row.TokenHash,
		ClientID: row.ClientID, UserID: row.UserID, Scopes: row.Scopes,
		FamilyID: row.FamilyID, ExpiresAt: row.ExpiresAt.UTC(),
		Consumed:      row.ConsumedAt.Valid,
		FamilyRevoked: row.FamilyRevokedAt.Valid,
	}, nil
}

func (o *PostgresStore) ConsumeRefresh(ctx context.Context, publicID, replacedByID string) (RefreshRecord, bool, error) {
	row, err := o.q.ConsumeOAuthRefreshToken(ctx, sqlc.ConsumeOAuthRefreshTokenParams{
		PublicID:     publicID,
		ReplacedByID: textFromString(replacedByID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return RefreshRecord{}, false, nil
	}
	if err != nil {
		return RefreshRecord{}, false, err
	}
	return RefreshRecord{
		ID: row.ID, PublicID: row.PublicID, TokenHash: row.TokenHash,
		ClientID: row.ClientID, UserID: row.UserID, Scopes: row.Scopes,
		FamilyID: row.FamilyID, ExpiresAt: row.ExpiresAt.UTC(),
		Consumed: row.ConsumedAt.Valid,
	}, true, nil
}

func (o *PostgresStore) ListAuthorizedApps(ctx context.Context, userID string) ([]AuthorizedApp, error) {
	rows, err := o.q.ListOAuthAuthorizedApps(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]AuthorizedApp, 0, len(rows))
	for _, r := range rows {
		out = append(out, AuthorizedApp{
			ClientID:   r.ClientID,
			ClientName: r.ClientName,
			FamilyID:   r.FamilyID,
			Scopes:     r.Scopes,
			GrantedAt:  r.CreatedAt.UTC(),
		})
	}
	return out, nil
}

func ptrFromTimestamptz(n pgtype.Timestamptz) *time.Time {
	if !n.Valid {
		return nil
	}
	t := n.Time.UTC()
	return &t
}

func textFromString(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}
