package server

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/credential"
	sqlc "github.com/CherryHQ/stella/pkg/db/sqlc"
)

// patStore adapts the sqlc queries to credential.PATStore + credential.UserLookup.
// It is the only place the PAT table shape is translated to the storage-agnostic
// credential.PATRecord.
type patStore struct {
	q *sqlc.Queries
}

func (p patStore) CreatePAT(ctx context.Context, rec credential.PATRecord) (credential.PATRecord, error) {
	row, err := p.q.CreatePersonalAccessToken(ctx, sqlc.CreatePersonalAccessTokenParams{
		PublicID:  rec.PublicID,
		UserID:    rec.UserID,
		Name:      rec.Name,
		TokenHash: rec.TokenHash,
		Last4:     rec.Last4,
		Scopes:    rec.Scopes,
		ExpiresAt: timestamptzFromPtr(rec.ExpiresAt),
	})
	if err != nil {
		return credential.PATRecord{}, err
	}
	return patRecordFromRow(row), nil
}

func (p patStore) GetPATByPublicID(ctx context.Context, publicID string) (credential.PATRecord, error) {
	row, err := p.q.GetPersonalAccessTokenByPublicID(ctx, publicID)
	if err != nil {
		return credential.PATRecord{}, err
	}
	return patRecordFromRow(row), nil
}

func (p patStore) ListPATByUser(ctx context.Context, userID string) ([]credential.PATRecord, error) {
	rows, err := p.q.ListPersonalAccessTokenByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]credential.PATRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, patRecordFromRow(r))
	}
	return out, nil
}

func (p patStore) RevokePAT(ctx context.Context, id, userID string) (int64, error) {
	return p.q.RevokePersonalAccessToken(ctx, sqlc.RevokePersonalAccessTokenParams{ID: id, UserID: userID})
}

func (p patStore) RevokePATByUser(ctx context.Context, userID string) (int64, error) {
	return p.q.RevokePersonalAccessTokenByUser(ctx, userID)
}

func (p patStore) TouchPATLastUsed(ctx context.Context, id string) (int64, error) {
	return p.q.UpdatePersonalAccessTokenLastUsed(ctx, id)
}

func (p patStore) LookupUser(ctx context.Context, userID string) (credential.Identity, error) {
	u, err := p.q.GetAuthUser(ctx, userID)
	if err != nil {
		return credential.Identity{}, err
	}
	return credential.Identity{
		UserID:    u.ID,
		Username:  u.Email,
		Email:     u.Email,
		Name:      u.Name,
		AvatarURL: u.AvatarUrl,
		Role:      u.Role,
		IsAdmin:   false,
		IsActive:  u.IsActive,
	}, nil
}

func patRecordFromRow(r sqlc.PersonalAccessToken) credential.PATRecord {
	return credential.PATRecord{
		ID:         r.ID,
		PublicID:   r.PublicID,
		UserID:     r.UserID,
		Name:       r.Name,
		TokenHash:  r.TokenHash,
		Last4:      r.Last4,
		Scopes:     r.Scopes,
		ExpiresAt:  ptrFromTimestamptz(r.ExpiresAt),
		LastUsedAt: ptrFromTimestamptz(r.LastUsedAt),
		RevokedAt:  ptrFromTimestamptz(r.RevokedAt),
		CreatedAt:  r.CreatedAt.UTC(),
	}
}

func timestamptzFromPtr(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t.UTC(), Valid: true}
}

func ptrFromTimestamptz(n pgtype.Timestamptz) *time.Time {
	if !n.Valid {
		return nil
	}
	t := n.Time.UTC()
	return &t
}
