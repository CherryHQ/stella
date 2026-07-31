package webhook

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// PostgresStore confines sqlc and transaction mechanics to the webhook domain.
type PostgresStore struct {
	db *pgxpool.Pool
	q  *sqlc.Queries
}

func NewPostgresStore(db *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{db: db, q: sqlc.New(db)}
}

func (s *PostgresStore) BindEndpoint(ctx context.Context, channelID string, build func(context.Context, ChannelBinding) (endpointRecord, error)) (endpointRecord, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return endpointRecord{}, fmt.Errorf("webhook: begin endpoint bind: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	binding, err := qtx.GetChannelBindingForUpdate(ctx, channelID)
	if err != nil {
		return endpointRecord{}, mapNotFound(err)
	}
	rec, err := build(ctx, ChannelBinding{
		ChannelID:    binding.ID,
		OwnerUserID:  binding.OwnerUserID.String,
		Type:         binding.Type,
		AgentID:      binding.AgentID.String,
		AgentEnabled: binding.AgentEnabled,
		Config:       binding.Config,
	})
	if err != nil {
		return endpointRecord{}, err
	}
	// BindEndpoint, not its callback, owns the channel relation. This prevents a
	// callback from inserting against a different, unlocked channel row.
	rec.ChannelID = channelID
	row, err := qtx.CreateChannelWebhookEndpoint(ctx, sqlc.CreateChannelWebhookEndpointParams{
		ChannelID:     rec.ChannelID,
		Provider:      string(rec.Provider),
		TokenPublicID: rec.TokenPublicID,
		TokenHash:     rec.TokenHash,
		TokenLast4:    rec.TokenLast4,
	})
	if err != nil {
		return endpointRecord{}, mapEndpointConflict(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return endpointRecord{}, fmt.Errorf("webhook: commit endpoint bind: %w", err)
	}
	stored := endpointFromRow(row)
	stored.OwnerUserID = rec.OwnerUserID
	return stored, nil
}

func (s *PostgresStore) ObserveBinding(ctx context.Context, channelID string) (ChannelBinding, error) {
	row, err := s.q.GetChannelBinding(ctx, channelID)
	if err != nil {
		return ChannelBinding{}, mapNotFound(err)
	}
	return ChannelBinding{
		ChannelID:    row.ID,
		OwnerUserID:  row.OwnerUserID.String,
		Type:         row.Type,
		AgentID:      row.AgentID.String,
		AgentEnabled: row.AgentEnabled,
		Config:       row.Config,
	}, nil
}

func (s *PostgresStore) GetEndpointByChannel(ctx context.Context, channelID, ownerID string) (endpointRecord, error) {
	row, err := s.q.GetChannelWebhookEndpointByChannelIDForOwner(ctx, sqlc.GetChannelWebhookEndpointByChannelIDForOwnerParams{
		ChannelID:   channelID,
		OwnerUserID: ownerParam(ownerID),
	})
	if err != nil {
		return endpointRecord{}, mapNotFound(err)
	}
	return endpointFromOwnedRow(row), nil
}

func (s *PostgresStore) ResolveEndpoint(ctx context.Context, publicID string) (resolvedRecord, error) {
	row, err := s.q.ResolveChannelWebhookEndpointByPublicID(ctx, publicID)
	if err != nil {
		return resolvedRecord{}, mapNotFound(err)
	}
	endpoint := Endpoint{
		ChannelID:     row.ChannelID,
		OwnerUserID:   row.OwnerUserID.String,
		Provider:      Provider(row.Provider),
		TokenPublicID: publicID,
		TokenLast4:    row.TokenLast4,
		Revision:      row.Revision,
		CreatedAt:     row.CreatedAt.UTC(),
		UpdatedAt:     row.UpdatedAt.UTC(),
	}
	if row.RotatedAt.Valid {
		rotated := row.RotatedAt.Time.UTC()
		endpoint.RotatedAt = &rotated
	}
	return resolvedRecord{
		endpointRecord: endpointRecord{Endpoint: endpoint, TokenHash: row.TokenHash},
		AgentID:        row.AgentID.String,
		ChannelEnabled: row.ChannelEnabled,
		OwnerActive:    row.OwnerActive,
		AgentEnabled:   row.AgentEnabled,
	}, nil
}

func (s *PostgresStore) ResolveByPublicID(ctx context.Context, publicID string) (endpointRecord, error) {
	row, err := s.q.GetChannelWebhookEndpointByPublicID(ctx, publicID)
	return endpointFromRow(row), mapNotFound(err)
}

func (s *PostgresStore) RotateEndpoint(ctx context.Context, channelID, ownerID string, expectedETag string, next endpointRecord) (endpointRecord, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return endpointRecord{}, fmt.Errorf("webhook: begin endpoint rotate: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	// Lock the endpoint row after SQL has confirmed channel ownership, so a
	// cross-owner request is indistinguishable from an absent endpoint.
	current, err := qtx.GetChannelWebhookEndpointByChannelIDForOwnerForUpdate(ctx, sqlc.GetChannelWebhookEndpointByChannelIDForOwnerForUpdateParams{
		ChannelID:   channelID,
		OwnerUserID: ownerParam(ownerID),
	})
	if err != nil {
		return endpointRecord{}, mapNotFound(err)
	}
	if EncodeETag(current.TokenPublicID, current.Revision) != expectedETag {
		return endpointRecord{}, ErrStaleETag
	}
	row, err := qtx.RotateChannelWebhookEndpoint(ctx, sqlc.RotateChannelWebhookEndpointParams{
		ChannelID:     channelID,
		TokenPublicID: next.TokenPublicID,
		TokenHash:     next.TokenHash,
		TokenLast4:    next.TokenLast4,
	})
	if err != nil {
		return endpointRecord{}, fmt.Errorf("webhook: rotate endpoint: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return endpointRecord{}, fmt.Errorf("webhook: commit endpoint rotate: %w", err)
	}
	stored := endpointFromRow(row)
	stored.OwnerUserID = current.OwnerUserID.String
	return stored, nil
}

func (s *PostgresStore) DeleteEndpoint(ctx context.Context, channelID, ownerID string) (int64, error) {
	return s.q.DeleteChannelWebhookEndpointForOwner(ctx, sqlc.DeleteChannelWebhookEndpointForOwnerParams{
		ChannelID:   channelID,
		OwnerUserID: ownerParam(ownerID),
	})
}

func endpointFromRow(row sqlc.ChannelWebhookEndpoint) endpointRecord {
	endpoint := Endpoint{
		ChannelID:     row.ChannelID,
		Provider:      Provider(row.Provider),
		TokenPublicID: row.TokenPublicID,
		TokenLast4:    row.TokenLast4,
		Revision:      row.Revision,
		CreatedAt:     row.CreatedAt.UTC(),
		UpdatedAt:     row.UpdatedAt.UTC(),
	}
	if row.RotatedAt.Valid {
		rotated := row.RotatedAt.Time.UTC()
		endpoint.RotatedAt = &rotated
	}
	return endpointRecord{Endpoint: endpoint, TokenHash: row.TokenHash}
}

func endpointFromOwnedRow(row sqlc.GetChannelWebhookEndpointByChannelIDForOwnerRow) endpointRecord {
	endpoint := Endpoint{
		ChannelID:     row.ChannelID,
		OwnerUserID:   row.OwnerUserID.String,
		Provider:      Provider(row.Provider),
		TokenPublicID: row.TokenPublicID,
		TokenLast4:    row.TokenLast4,
		Revision:      row.Revision,
		CreatedAt:     row.CreatedAt.UTC(),
		UpdatedAt:     row.UpdatedAt.UTC(),
	}
	if row.RotatedAt.Valid {
		rotated := row.RotatedAt.Time.UTC()
		endpoint.RotatedAt = &rotated
	}
	return endpointRecord{Endpoint: endpoint, TokenHash: row.TokenHash}
}

func ownerParam(id string) pgtype.Text { return pgtype.Text{String: id, Valid: id != ""} }

func mapNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func mapEndpointConflict(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "channel_webhook_endpoint_pkey" {
		return ErrEndpointExists
	}
	return err
}
