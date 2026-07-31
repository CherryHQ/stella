package webhook

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// PostgresStore confines sqlc and short credential-rotation transactions to the
// webhook domain. Every resource operation scopes by (id, user_id) in SQL.
type PostgresStore struct {
	db *pgxpool.Pool
	q  *sqlc.Queries
}

func NewPostgresStore(db *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{db: db, q: sqlc.New(db)}
}

func (s *PostgresStore) Create(ctx context.Context, rec credentialRecord) (credentialRecord, error) {
	row, err := s.q.CreateWebhook(ctx, sqlc.CreateWebhookParams{ID: rec.ID, UserID: rec.UserID, AgentID: rec.AgentID, Name: rec.Name, Provider: string(rec.Provider), IsEnabled: rec.IsEnabled, WaitTimeoutSeconds: rec.WaitTimeoutSeconds, MaxRunTimeoutSeconds: rec.MaxRunTimeoutSeconds, TokenPublicID: rec.TokenPublicID, TokenHash: rec.TokenHash, TokenLast4: rec.TokenLast4})
	if err != nil {
		return credentialRecord{}, fmt.Errorf("webhook: create: %w", err)
	}
	return recordFromRow(row), nil
}

func (s *PostgresStore) Get(ctx context.Context, id, userID string) (credentialRecord, error) {
	row, err := s.q.GetWebhookForUser(ctx, sqlc.GetWebhookForUserParams{ID: id, UserID: userID})
	if err != nil {
		return credentialRecord{}, mapNotFound(err)
	}
	return recordFromRow(row), nil
}

func (s *PostgresStore) List(ctx context.Context, userID string, limit, offset int32) ([]credentialRecord, error) {
	rows, err := s.q.ListWebhookForUser(ctx, sqlc.ListWebhookForUserParams{UserID: userID, Limit: limit, Offset: offset})
	if err != nil {
		return nil, fmt.Errorf("webhook: list: %w", err)
	}
	out := make([]credentialRecord, len(rows))
	for i := range rows {
		out[i] = recordFromRow(rows[i])
	}
	return out, nil
}

func (s *PostgresStore) Update(ctx context.Context, req UpdateRequest, expectedAgent string) (credentialRecord, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return credentialRecord{}, fmt.Errorf("webhook: begin update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	current, err := qtx.GetWebhookForUserForUpdate(ctx, sqlc.GetWebhookForUserForUpdateParams{ID: req.ID, UserID: req.UserID})
	if err != nil {
		return credentialRecord{}, mapNotFound(err)
	}
	locked := recordFromRow(current)
	if locked.AgentID != expectedAgent {
		return credentialRecord{}, ErrBindingChanged
	}
	next := applyUpdate(locked.Webhook, req)
	row, err := qtx.UpdateWebhookForUser(ctx, sqlc.UpdateWebhookForUserParams{ID: next.ID, UserID: next.UserID, Name: next.Name, AgentID: next.AgentID, IsEnabled: next.IsEnabled, WaitTimeoutSeconds: next.WaitTimeoutSeconds, MaxRunTimeoutSeconds: next.MaxRunTimeoutSeconds})
	if err != nil {
		return credentialRecord{}, mapNotFound(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return credentialRecord{}, fmt.Errorf("webhook: commit update: %w", err)
	}
	return recordFromRow(row), nil
}

func (s *PostgresStore) ResolveByPublicID(ctx context.Context, publicID string) (credentialRecord, error) {
	row, err := s.q.GetWebhookByPublicID(ctx, publicID)
	if err != nil {
		return credentialRecord{}, mapNotFound(err)
	}
	return recordFromRow(row), nil
}

func (s *PostgresStore) ResolveAdmitted(ctx context.Context, publicID string) (credentialRecord, error) {
	row, err := s.q.ResolveWebhookByPublicID(ctx, publicID)
	if err != nil {
		return credentialRecord{}, mapNotFound(err)
	}
	return recordFromRow(row), nil
}

func (s *PostgresStore) Rotate(ctx context.Context, id, userID, etag string, next credentialRecord) (credentialRecord, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return credentialRecord{}, fmt.Errorf("webhook: begin rotate: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	current, err := qtx.GetWebhookForUserForUpdate(ctx, sqlc.GetWebhookForUserForUpdateParams{ID: id, UserID: userID})
	if err != nil {
		return credentialRecord{}, mapNotFound(err)
	}
	if EncodeETag(current.TokenPublicID, current.Revision) != etag {
		return credentialRecord{}, ErrStaleETag
	}
	row, err := qtx.RotateWebhook(ctx, sqlc.RotateWebhookParams{ID: id, UserID: userID, TokenPublicID: next.TokenPublicID, TokenHash: next.TokenHash, TokenLast4: next.TokenLast4})
	if err != nil {
		return credentialRecord{}, fmt.Errorf("webhook: rotate: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return credentialRecord{}, fmt.Errorf("webhook: commit rotate: %w", err)
	}
	return recordFromRow(row), nil
}

func (s *PostgresStore) Delete(ctx context.Context, id, userID string) (int64, error) {
	return s.q.DeleteWebhookForUser(ctx, sqlc.DeleteWebhookForUserParams{ID: id, UserID: userID})
}

func recordFromRow(row sqlc.Webhook) credentialRecord {
	out := credentialRecord{Webhook: Webhook{ID: row.ID, UserID: row.UserID, AgentID: row.AgentID, Name: row.Name, Provider: Provider(row.Provider), IsEnabled: row.IsEnabled, WaitTimeoutSeconds: row.WaitTimeoutSeconds, MaxRunTimeoutSeconds: row.MaxRunTimeoutSeconds, TokenPublicID: row.TokenPublicID, TokenLast4: row.TokenLast4, Revision: row.Revision, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC()}, TokenHash: row.TokenHash}
	if row.RotatedAt.Valid {
		t := row.RotatedAt.Time.UTC()
		out.RotatedAt = &t
	}
	return out
}

func mapNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
