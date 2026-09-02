package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/core/providercred"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// DBStore is the persistence port for Agent Provider credentials. Only ciphertext
// and metadata cross this boundary; encryption happens above, in
// providercred.Service.
var _ providercred.Store = (*DBStore)(nil)

// ListAgentProviderCredentials returns secret-free metadata for one Agent. The
// underlying query never selects api_key_enc.
func (s *DBStore) ListAgentProviderCredentials(ctx context.Context, agentID string) ([]providercred.Metadata, error) {
	rows, err := s.q.ListAgentProviderCredential(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("list agent %q provider credentials: %w", agentID, err)
	}
	out := make([]providercred.Metadata, len(rows))
	for i, r := range rows {
		out[i] = providercred.Metadata{
			ProviderID: r.ProviderID,
			HasAPIKey:  true, // a stored row always carries a non-empty key (DB CHECK)
			UpdatedAt:  r.UpdatedAt.UTC(),
		}
	}
	return out, nil
}

// GetAgentProviderCredential returns one credential's ciphertext, or found=false
// when the Agent has no override for that Provider.
func (s *DBStore) GetAgentProviderCredential(ctx context.Context, agentID, providerID string) (providercred.Encrypted, bool, error) {
	r, err := s.q.GetAgentProviderCredential(ctx, sqlc.GetAgentProviderCredentialParams{
		AgentID:    agentID,
		ProviderID: providerID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return providercred.Encrypted{}, false, nil
	}
	if err != nil {
		return providercred.Encrypted{}, false, fmt.Errorf("get agent %q provider credential %q: %w", agentID, providerID, err)
	}
	return providercred.Encrypted{ProviderID: r.ProviderID, APIKeyEnc: r.ApiKeyEnc}, true, nil
}

// UpsertAgentProviderCredential writes or atomically rotates one credential.
func (s *DBStore) UpsertAgentProviderCredential(ctx context.Context, agentID string, cred providercred.Encrypted) (providercred.Metadata, error) {
	row, err := s.q.UpsertAgentProviderCredential(ctx, sqlc.UpsertAgentProviderCredentialParams{
		AgentID:    agentID,
		ProviderID: cred.ProviderID,
		ApiKeyEnc:  cred.APIKeyEnc,
	})
	if err != nil {
		return providercred.Metadata{}, fmt.Errorf("upsert agent %q provider credential %q: %w", agentID, cred.ProviderID, err)
	}
	return providercred.Metadata{ProviderID: row.ProviderID, HasAPIKey: true, UpdatedAt: row.UpdatedAt.UTC()}, nil
}

// DeleteAgentProviderCredential removes one credential. It is idempotent: a
// DELETE affecting zero rows is not an error.
func (s *DBStore) DeleteAgentProviderCredential(ctx context.Context, agentID, providerID string) error {
	if err := s.q.DeleteAgentProviderCredential(ctx, sqlc.DeleteAgentProviderCredentialParams{
		AgentID:    agentID,
		ProviderID: providerID,
	}); err != nil {
		return fmt.Errorf("delete agent %q provider credential %q: %w", agentID, providerID, err)
	}
	return nil
}

// CreateAgentWithCredentials inserts the Agent and all its credential rows in one
// transaction. A plain (non-upsert) credential insert means a duplicate
// (agent_id, provider_id) aborts the whole transaction, so a partial write can
// never leave an Agent with some of its credentials.
func (s *DBStore) CreateAgentWithCredentials(ctx context.Context, a config.Agent, creds []providercred.Encrypted) error {
	params, err := createAgentParams(a)
	if err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("create agent %q: begin tx: %w", params.ID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.q.WithTx(tx)
	if _, err := qtx.CreateAgent(ctx, params); err != nil {
		return fmt.Errorf("create agent %q: %w", params.ID, err)
	}
	for _, c := range creds {
		if _, err := qtx.CreateAgentProviderCredential(ctx, sqlc.CreateAgentProviderCredentialParams{
			AgentID:    params.ID,
			ProviderID: c.ProviderID,
			ApiKeyEnc:  c.APIKeyEnc,
		}); err != nil {
			return fmt.Errorf("create agent %q credential %q: %w", params.ID, c.ProviderID, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("create agent %q: commit: %w", params.ID, err)
	}
	return nil
}
