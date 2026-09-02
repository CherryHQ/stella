package main

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/model/embedding"
)

// setupEmbedding builds the always-present semantic-search lane. The lane is
// config-driven at runtime: it reads the embedding settings from the DB config
// store on each query and backfill pass, so enabling/disabling it or changing the
// model/dimension in the web settings page — or the deployment's embedding model
// on the default-models page — takes effect without a restart.
// When disabled (the default for a fresh deployment) the query embedder reports
// no vector space and the backfill worker idles, keeping pure-BM25 behavior.
func setupEmbedding(db *pgxpool.Pool, store config.Store, logger *slog.Logger) *embedding.Service {
	return embedding.Boot(embedding.BootConfig{
		DB:       db,
		Settings: embeddingSettingsProvider{store: store},
		Logger:   logger,
	})
}

// embeddingSettingsProvider adapts the DB config store to the embedding package's
// SettingsProvider, keeping the embedding package free of a config dependency.
type embeddingSettingsProvider struct{ store config.Store }

func (p embeddingSettingsProvider) EmbeddingSettings(ctx context.Context) (embedding.Settings, error) {
	s, err := config.ResolveEmbedding(ctx, p.store)
	if err != nil {
		return embedding.Settings{}, err
	}
	return embedding.Settings{
		Enabled:   s.Enabled,
		Provider:  s.Provider,
		Model:     s.Model,
		Dim:       s.Dim,
		APIKey:    s.APIKey,
		BaseURL:   s.BaseURL,
		Normalize: s.Normalize,
	}, nil
}
