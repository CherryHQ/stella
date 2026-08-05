package credential

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/auth"

	sqlc "github.com/CherryHQ/stella/pkg/db/sqlc"
)

// PostgresStore adapts the sqlc queries to PATStore + UserLookup. It is the only
// place the PAT table shape and the auth_user lookup are translated to the
// storage-agnostic credential records. The composition root constructs it once
// and hands it to NewService as both the PATs and Users dependency.
type PostgresStore struct {
	q *sqlc.Queries
}

// NewPostgresStore builds the PAT/user-lookup store over the shared pool.
func NewPostgresStore(db *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{q: sqlc.New(db)}
}

var (
	_ PATStore   = (*PostgresStore)(nil)
	_ UserLookup = (*PostgresStore)(nil)
)

func (p *PostgresStore) CreatePAT(ctx context.Context, rec PATRecord) (PATRecord, error) {
	row, err := p.q.CreatePersonalAccessToken(ctx, sqlc.CreatePersonalAccessTokenParams{
		PublicID:  rec.PublicID,
		UserID:    rec.UserID,
		Name:      rec.Name,
		TokenHash: rec.TokenHash,
		Last4:     rec.Last4,
		Scopes:    rec.Scopes,
		ExpiresAt: timestamptzFromPtr(rec.ExpiresAt),
		TokenUse:  string(rec.TokenUse),
	})
	if err != nil {
		return PATRecord{}, err
	}
	return patRecordFromRow(row), nil
}

func (p *PostgresStore) GetPATByPublicID(ctx context.Context, publicID string) (PATRecord, error) {
	row, err := p.q.GetPersonalAccessTokenByPublicID(ctx, publicID)
	if err != nil {
		return PATRecord{}, err
	}
	return patRecordFromRow(row), nil
}

func (p *PostgresStore) ListPATByUser(ctx context.Context, userID string) ([]PATRecord, error) {
	rows, err := p.q.ListPersonalAccessTokenByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]PATRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, patRecordFromRow(r))
	}
	return out, nil
}

func (p *PostgresStore) ListProvisioningTokenByUser(ctx context.Context, userID string) ([]PATRecord, error) {
	rows, err := p.q.ListProvisioningTokenByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]PATRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, patRecordFromRow(r))
	}
	return out, nil
}

func (p *PostgresStore) RevokePAT(ctx context.Context, id, userID string) (int64, error) {
	return p.q.RevokePersonalAccessToken(ctx, sqlc.RevokePersonalAccessTokenParams{ID: id, UserID: userID})
}

func (p *PostgresStore) RevokeProvisioningToken(ctx context.Context, id, userID string) (int64, error) {
	return p.q.RevokeProvisioningToken(ctx, sqlc.RevokeProvisioningTokenParams{ID: id, UserID: userID})
}

func (p *PostgresStore) RevokePATByUser(ctx context.Context, userID string) (int64, error) {
	return p.q.RevokePersonalAccessTokenByUser(ctx, userID)
}

func (p *PostgresStore) TouchPATLastUsed(ctx context.Context, id string) (int64, error) {
	return p.q.UpdatePersonalAccessTokenLastUsed(ctx, id)
}

func (p *PostgresStore) LookupUser(ctx context.Context, userID string) (Identity, error) {
	u, err := p.q.GetAuthUser(ctx, userID)
	if err != nil {
		return Identity{}, err
	}
	return Identity{
		UserID:    u.ID,
		Username:  u.Email,
		Email:     u.Email,
		Name:      u.Name,
		AvatarURL: u.AvatarUrl,
		Role:      u.Role,
		IsAdmin:   u.Role == auth.RoleAdmin,
		IsActive:  u.IsActive,
	}, nil
}

func patRecordFromRow(r sqlc.PersonalAccessToken) PATRecord {
	return PATRecord{
		ID:         r.ID,
		PublicID:   r.PublicID,
		UserID:     r.UserID,
		Name:       r.Name,
		TokenHash:  r.TokenHash,
		Last4:      r.Last4,
		Scopes:     r.Scopes,
		TokenUse:   TokenUse(r.TokenUse),
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
