package main

import (
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/embedding"
)

const (
	// defaultEmbeddingModel is the canonical day-1 vector space: OpenAI's
	// text-embedding-3-small emits 1536-dim vectors natively, matching the
	// sidecar storage width with no padding.
	defaultEmbeddingModel = "text-embedding-3-small"
	// defaultEmbeddingName labels the provider instance in logs.
	defaultEmbeddingName = "openai-embedding"
)

// setupEmbedding builds the optional semantic-search lane from STELLA_EMBEDDING_*
// env. The lane is opt-in: with no STELLA_EMBEDDING_API_KEY it returns nil and the
// deployment stays pure-BM25 (no query embedding, no backfill worker). When a key
// is present it constructs the API-first chain + backfill indexer; the caller
// wires the returned Service into the memory provider (query embedder) and the
// shared River client (backfill worker + periodic).
func setupEmbedding(db *pgxpool.Pool, logger *slog.Logger) *embedding.Service {
	apiKey := os.Getenv("STELLA_EMBEDDING_API_KEY")
	if apiKey == "" {
		return nil
	}

	model := os.Getenv("STELLA_EMBEDDING_MODEL")
	if model == "" {
		model = defaultEmbeddingModel
	}

	// Dim pins the requested output width to the sidecar storage width so no
	// padding is needed; text-embedding-3-* honor the API `dimensions` param.
	dim := embedding.StorageDim
	if raw := os.Getenv("STELLA_EMBEDDING_DIM"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			dim = n
		} else {
			logger.Warn("embedding: STELLA_EMBEDDING_DIM unparseable, using default", "value", raw, "default", dim)
		}
	}

	// Normalize defaults off: cosine distance (the <=> operator backing the HNSW
	// index) is scale-invariant, so L2-normalizing query and document vectors is
	// redundant. Enable only for a provider whose vectors are not already unit
	// length and where exact cosine matters.
	normalize := false
	if raw := os.Getenv("STELLA_EMBEDDING_NORMALIZE"); raw != "" {
		if b, err := strconv.ParseBool(raw); err == nil {
			normalize = b
		} else {
			logger.Warn("embedding: STELLA_EMBEDDING_NORMALIZE unparseable, using default", "value", raw, "default", normalize)
		}
	}

	var interval time.Duration
	if raw := os.Getenv("STELLA_EMBEDDING_BACKFILL_INTERVAL"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			interval = d
		} else {
			logger.Warn("embedding: STELLA_EMBEDDING_BACKFILL_INTERVAL unparseable, using default", "value", raw)
		}
	}

	svc := embedding.Boot(embedding.BootConfig{
		DB: db,
		API: embedding.APIConfig{
			Name:    defaultEmbeddingName,
			Model:   model,
			Dim:     dim,
			APIKey:  apiKey,
			BaseURL: os.Getenv("STELLA_EMBEDDING_BASE_URL"),
		},
		Normalize: normalize,
		Interval:  interval,
		Logger:    logger,
	})
	logger.Info("embedding: semantic search lane enabled", "model", model, "dim", dim, "normalize", normalize)
	return svc
}
