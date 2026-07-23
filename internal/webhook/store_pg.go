package webhook

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
		ID: rec.ID, ChannelID: rec.ChannelID, OwnerUserID: rec.OwnerUserID, Provider: string(rec.Provider),
		TokenPublicID: rec.TokenPublicID, TokenHash: rec.TokenHash, TokenLast4: rec.TokenLast4,
		ProviderSecretCiphertext: rec.ProviderSecretCiphertext,
	})
	if err != nil {
		return endpointRecord{}, mapEndpointConflict(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return endpointRecord{}, fmt.Errorf("webhook: commit endpoint bind: %w", err)
	}
	return endpointFromRow(row), nil
}

func (s *PostgresStore) GetEndpoint(ctx context.Context, id string) (endpointRecord, error) {
	row, err := s.q.GetChannelWebhookEndpointByID(ctx, id)
	return endpointFromRow(row), mapNotFound(err)
}

func (s *PostgresStore) GetEndpointByChannel(ctx context.Context, channelID string) (endpointRecord, error) {
	row, err := s.q.GetChannelWebhookEndpointByChannelID(ctx, channelID)
	return endpointFromRow(row), mapNotFound(err)
}

func (s *PostgresStore) ResolveEndpoint(ctx context.Context, publicID string) (resolvedRecord, error) {
	row, err := s.q.ResolveChannelWebhookEndpointByPublicID(ctx, publicID)
	if err != nil {
		return resolvedRecord{}, mapNotFound(err)
	}
	return resolvedRecord{
		endpointRecord: endpointRecord{Endpoint: resolvedEndpoint(row), TokenHash: row.TokenHash, ProviderSecretCiphertext: row.ProviderSecretCiphertext},
		AgentID:        row.AgentID.String, ChannelEnabled: row.ChannelEnabled, OwnerActive: row.OwnerActive, AgentEnabled: row.AgentEnabled,
	}, nil
}

func (s *PostgresStore) RotateEndpoint(ctx context.Context, rec endpointRecord) (endpointRecord, error) {
	row, err := s.q.RotateChannelWebhookEndpoint(ctx, sqlc.RotateChannelWebhookEndpointParams{
		ID: rec.ID, TokenPublicID: rec.TokenPublicID, TokenHash: rec.TokenHash, TokenLast4: rec.TokenLast4,
		ProviderSecretCiphertext: rec.ProviderSecretCiphertext,
	})
	return endpointFromRow(row), mapNotFound(err)
}

func (s *PostgresStore) DeleteEndpoint(ctx context.Context, id string) (int64, error) {
	return s.q.DeleteChannelWebhookEndpoint(ctx, id)
}

func (s *PostgresStore) ClaimDelivery(ctx context.Context, endpointID string, provider Provider, deliveryID string) (bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin delivery claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	// Delete the exact expired key first so a redelivery after the 30-day window
	// is accepted even if a bounded global cleanup backlog exists.
	if _, err := qtx.DeleteExpiredChannelWebhookDeliveryForClaim(ctx, sqlc.DeleteExpiredChannelWebhookDeliveryForClaimParams{
		EndpointID: endpointID, Provider: string(provider), DeliveryID: deliveryID,
	}); err != nil {
		return false, fmt.Errorf("expire claimed delivery: %w", err)
	}
	if _, err := qtx.DeleteExpiredChannelWebhookDelivery(ctx, cleanupBatchLimit); err != nil {
		return false, fmt.Errorf("expire delivery batch: %w", err)
	}
	claimed, err := qtx.ClaimChannelWebhookDelivery(ctx, sqlc.ClaimChannelWebhookDeliveryParams{
		ID: newID(), EndpointID: endpointID, Provider: string(provider), DeliveryID: deliveryID,
	})
	if err != nil {
		return false, fmt.Errorf("claim delivery: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit delivery claim: %w", err)
	}
	return claimed, nil
}

func (s *PostgresStore) ReleaseDelivery(ctx context.Context, endpointID string, provider Provider, deliveryID string) (int64, error) {
	return s.q.ReleaseChannelWebhookDelivery(ctx, sqlc.ReleaseChannelWebhookDeliveryParams{
		EndpointID: endpointID, Provider: string(provider), DeliveryID: deliveryID,
	})
}

func resolvedEndpoint(row sqlc.ResolveChannelWebhookEndpointByPublicIDRow) Endpoint {
	endpoint := Endpoint{
		ID: row.EndpointID, ChannelID: row.ChannelID, OwnerUserID: row.OwnerUserID, Provider: Provider(row.Provider),
		TokenLast4: row.TokenLast4, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}
	if row.RotatedAt.Valid {
		rotated := row.RotatedAt.Time.UTC()
		endpoint.RotatedAt = &rotated
	}
	return endpoint
}

func endpointFromRow(row sqlc.ChannelWebhookEndpoint) endpointRecord {
	endpoint := Endpoint{
		ID: row.ID, ChannelID: row.ChannelID, OwnerUserID: row.OwnerUserID, Provider: Provider(row.Provider),
		TokenLast4: row.TokenLast4, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}
	if row.RotatedAt.Valid {
		rotated := row.RotatedAt.Time.UTC()
		endpoint.RotatedAt = &rotated
	}
	return endpointRecord{
		Endpoint: endpoint, TokenPublicID: row.TokenPublicID, TokenHash: row.TokenHash,
		ProviderSecretCiphertext: row.ProviderSecretCiphertext,
	}
}

func mapNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func mapEndpointConflict(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "channel_webhook_endpoint_channel_id_key" {
		return ErrEndpointExists
	}
	return err
}

func newID() string {
	// uuidv7 keeps the claim index append-friendly. The database default remains
	// the backstop for direct SQL inserts.
	return uuid.Must(uuid.NewV7()).String()
}
