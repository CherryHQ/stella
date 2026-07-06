package server

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/credential"
	"github.com/CherryHQ/stella/internal/oidc"
	sqlc "github.com/CherryHQ/stella/pkg/db/sqlc"
)

// oauthStore adapts the sqlc queries to both credential.OAuthAccessStore (the
// opaque access-token front door) and oidc.Store (the authorization-server
// client/code/refresh storage). One adapter, two interfaces: access tokens stay
// owned by credential, while the flow tables stay in internal/oidc.
type oauthStore struct {
	q *sqlc.Queries
}

var (
	_ credential.OAuthAccessStore = oauthStore{}
	_ oidc.Store                  = oauthStore{}
)

// ---- credential.OAuthAccessStore ----

func (o oauthStore) CreateOAuthAccess(ctx context.Context, rec credential.OAuthAccessRecord) (credential.OAuthAccessRecord, error) {
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

func (o oauthStore) GetOAuthAccessByPublicID(ctx context.Context, publicID string) (credential.OAuthAccessRecord, error) {
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

func (o oauthStore) TouchOAuthAccessLastUsed(ctx context.Context, id string) (int64, error) {
	return o.q.UpdateOAuthAccessTokenLastUsed(ctx, id)
}

// ---- oidc.Store: clients ----

func (o oauthStore) CreateClient(ctx context.Context, c oidc.ClientCreate) (oidc.Client, error) {
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
		return oidc.Client{}, err
	}
	return oauthClientFromRow(row), nil
}

func (o oauthStore) GetClient(ctx context.Context, clientID string) (oidc.Client, error) {
	row, err := o.q.GetOAuthClientByClientID(ctx, clientID)
	if err != nil {
		return oidc.Client{}, err
	}
	return oauthClientFromRow(row), nil
}

func (o oauthStore) ListClientsByOwner(ctx context.Context, ownerUserID string) ([]oidc.Client, error) {
	rows, err := o.q.ListOAuthClientByOwner(ctx, ownerUserID)
	if err != nil {
		return nil, err
	}
	out := make([]oidc.Client, 0, len(rows))
	for _, r := range rows {
		out = append(out, oauthClientFromRow(r))
	}
	return out, nil
}

func (o oauthStore) UpdateClientSecret(ctx context.Context, clientID, ownerUserID, secretHash string) (int64, error) {
	return o.q.UpdateOAuthClientSecret(ctx, sqlc.UpdateOAuthClientSecretParams{
		ClientID:         clientID,
		ClientSecretHash: secretHash,
		OwnerUserID:      ownerUserID,
	})
}

func (o oauthStore) DisableClient(ctx context.Context, clientID, ownerUserID string) (int64, error) {
	return o.q.DisableOAuthClient(ctx, sqlc.DisableOAuthClientParams{ClientID: clientID, OwnerUserID: ownerUserID})
}

func oauthClientFromRow(r sqlc.OauthClient) oidc.Client {
	return oidc.Client{
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

func (o oauthStore) CreateCode(ctx context.Context, c oidc.AuthCodeCreate) error {
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

func (o oauthStore) ConsumeCode(ctx context.Context, codeHash string) (oidc.AuthCode, bool, error) {
	row, err := o.q.ConsumeOAuthAuthorizationCode(ctx, codeHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return oidc.AuthCode{}, false, nil
	}
	if err != nil {
		return oidc.AuthCode{}, false, err
	}
	return oidc.AuthCode{
		ClientID:            row.ClientID,
		UserID:              row.UserID,
		RedirectURI:         row.RedirectUri,
		Scopes:              row.Scopes,
		CodeChallenge:       row.CodeChallenge,
		CodeChallengeMethod: row.CodeChallengeMethod,
		ExpiresAt:           row.ExpiresAt.UTC(),
	}, true, nil
}

func (o oauthStore) RevokeCodesForUserClient(ctx context.Context, userID, clientID string) error {
	_, err := o.q.RevokeOAuthAuthorizationCodesForUserClient(ctx, sqlc.RevokeOAuthAuthorizationCodesForUserClientParams{
		UserID:   userID,
		ClientID: clientID,
	})
	return err
}

// ---- oidc.Store: refresh families (the revocation unit) ----

func (o oauthStore) CreateFamily(ctx context.Context, userID, clientID string) (string, error) {
	row, err := o.q.CreateOAuthRefreshFamily(ctx, sqlc.CreateOAuthRefreshFamilyParams{
		UserID:   userID,
		ClientID: clientID,
	})
	if err != nil {
		return "", err
	}
	return row.ID, nil
}

func (o oauthStore) RevokeFamily(ctx context.Context, familyID string) error {
	_, err := o.q.RevokeOAuthRefreshFamily(ctx, familyID)
	return err
}

func (o oauthStore) RevokeFamiliesForUserClient(ctx context.Context, userID, clientID string) error {
	_, err := o.q.RevokeOAuthRefreshFamiliesForUserClient(ctx, sqlc.RevokeOAuthRefreshFamiliesForUserClientParams{
		UserID:   userID,
		ClientID: clientID,
	})
	return err
}

// ---- oidc.Store: refresh tokens ----

func (o oauthStore) CreateRefresh(ctx context.Context, r oidc.RefreshCreate) (oidc.RefreshRecord, error) {
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
		return oidc.RefreshRecord{}, err
	}
	// A freshly created token's family is live; FamilyRevoked stays false.
	return oidc.RefreshRecord{
		ID: row.ID, PublicID: row.PublicID, TokenHash: row.TokenHash,
		ClientID: row.ClientID, UserID: row.UserID, Scopes: row.Scopes,
		FamilyID: row.FamilyID, ExpiresAt: row.ExpiresAt.UTC(),
		Consumed: row.ConsumedAt.Valid,
	}, nil
}

func (o oauthStore) GetRefreshByPublicID(ctx context.Context, publicID string) (oidc.RefreshRecord, error) {
	row, err := o.q.GetOAuthRefreshTokenByPublicID(ctx, publicID)
	if err != nil {
		return oidc.RefreshRecord{}, err
	}
	return oidc.RefreshRecord{
		ID: row.ID, PublicID: row.PublicID, TokenHash: row.TokenHash,
		ClientID: row.ClientID, UserID: row.UserID, Scopes: row.Scopes,
		FamilyID: row.FamilyID, ExpiresAt: row.ExpiresAt.UTC(),
		Consumed:      row.ConsumedAt.Valid,
		FamilyRevoked: row.FamilyRevokedAt.Valid,
	}, nil
}

func (o oauthStore) ConsumeRefresh(ctx context.Context, publicID, replacedByID string) (oidc.RefreshRecord, bool, error) {
	row, err := o.q.ConsumeOAuthRefreshToken(ctx, sqlc.ConsumeOAuthRefreshTokenParams{
		PublicID:     publicID,
		ReplacedByID: textFromString(replacedByID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return oidc.RefreshRecord{}, false, nil
	}
	if err != nil {
		return oidc.RefreshRecord{}, false, err
	}
	return oidc.RefreshRecord{
		ID: row.ID, PublicID: row.PublicID, TokenHash: row.TokenHash,
		ClientID: row.ClientID, UserID: row.UserID, Scopes: row.Scopes,
		FamilyID: row.FamilyID, ExpiresAt: row.ExpiresAt.UTC(),
		Consumed: row.ConsumedAt.Valid,
	}, true, nil
}

func (o oauthStore) ListAuthorizedApps(ctx context.Context, userID string) ([]oidc.AuthorizedApp, error) {
	rows, err := o.q.ListOAuthAuthorizedApps(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]oidc.AuthorizedApp, 0, len(rows))
	for _, r := range rows {
		out = append(out, oidc.AuthorizedApp{
			ClientID:   r.ClientID,
			ClientName: r.ClientName,
			FamilyID:   r.FamilyID,
			Scopes:     r.Scopes,
			GrantedAt:  r.CreatedAt.UTC(),
		})
	}
	return out, nil
}

func textFromString(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}
