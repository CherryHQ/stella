package embedding

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	pgvector "github.com/pgvector/pgvector-go"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Embedder is the entry point the indexer depends on; *Chain satisfies it. Kept
// as a narrow interface so tests inject a deterministic fake.
type Embedder interface {
	Embed(ctx context.Context, req Request) (Result, error)
}

// IndexConfig configures backfill.
type IndexConfig struct {
	// Model is the canonical vector space this deployment embeds into. The indexer
	// scans for rows missing this space and writes only vectors produced in it, so
	// a fallback to a different space never fragments the stored corpus.
	Model string
	// Normalize L2-normalizes each vector before storage (cosine as dot product).
	Normalize bool
	// BatchSize bounds how many rows of each source one BackfillOnce pass embeds
	// (default 128). One API round-trip embeds a whole batch.
	BatchSize int
}

// Indexer populates the *_embedding sidecars from their source rows. One pass
// (BackfillOnce) embeds up to BatchSize stale rows per source; a periodic caller
// drives it until the backlog drains.
type Indexer struct {
	q   *sqlc.Queries
	emb Embedder
	cfg IndexConfig
}

// NewIndexer builds an indexer. emb must produce vectors in cfg.Model's space.
func NewIndexer(q *sqlc.Queries, emb Embedder, cfg IndexConfig) *Indexer {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 128
	}
	return &Indexer{q: q, emb: emb, cfg: cfg}
}

// candidate is a source row that may need (re-)embedding.
type candidate struct {
	id            string
	content       string
	embeddedModel pgtype.Text
	embeddedHash  []byte
}

// source abstracts one *_embedding sidecar so BackfillOnce treats all three
// uniformly: list stale candidates, upsert a computed vector.
type source struct {
	name   string
	list   func(ctx context.Context, limit int32) ([]candidate, error)
	upsert func(ctx context.Context, id string, hash []byte, vec pgvector.Vector) error
}

// BackfillOnce runs one bounded pass over every source and returns how many rows
// it embedded. It is safe to call repeatedly; a row already current in the
// canonical space is skipped, so once the backlog drains a pass returns 0.
func (ix *Indexer) BackfillOnce(ctx context.Context) (int, error) {
	total := 0
	for _, src := range ix.sources() {
		n, err := ix.backfillSource(ctx, src)
		if err != nil {
			return total, fmt.Errorf("backfill %s: %w", src.name, err)
		}
		total += n
	}
	return total, nil
}

func (ix *Indexer) backfillSource(ctx context.Context, src source) (int, error) {
	cands, err := src.list(ctx, int32(ix.cfg.BatchSize))
	if err != nil {
		return 0, err
	}

	// Drop rows already current: a mutable source can surface a row whose
	// non-content fields changed (same space, same content hash). Carry the fresh
	// hash on the survivors so the writer stores it.
	var todo []candidate
	for _, c := range cands {
		h := sha256.Sum256([]byte(c.content))
		if c.embeddedModel.Valid && c.embeddedModel.String == ix.cfg.Model && bytes.Equal(c.embeddedHash, h[:]) {
			continue
		}
		c.embeddedHash = h[:]
		todo = append(todo, c)
	}
	if len(todo) == 0 {
		return 0, nil
	}

	texts := make([]string, len(todo))
	for i, c := range todo {
		texts[i] = c.content
	}
	res, err := ix.emb.Embed(ctx, Request{Texts: texts, Mode: ModeDocument})
	if err != nil {
		return 0, err
	}
	// Never fragment the corpus: only persist vectors in the canonical space. A
	// fallback to another space is left for a later pass once the primary recovers.
	if res.Model != ix.cfg.Model {
		return 0, fmt.Errorf("embedder produced space %q, want %q", res.Model, ix.cfg.Model)
	}
	if len(res.Vectors) != len(todo) {
		return 0, fmt.Errorf("embedder returned %d vectors for %d inputs", len(res.Vectors), len(todo))
	}

	for i, c := range todo {
		vec, err := ToStorageVector(res.Vectors[i], ix.cfg.Normalize)
		if err != nil {
			return 0, err
		}
		if err := src.upsert(ctx, c.id, c.embeddedHash, pgvector.NewVector(vec)); err != nil {
			return 0, err
		}
	}
	return len(todo), nil
}

// sources binds the three sidecars to the uniform list/upsert shape.
func (ix *Indexer) sources() []source {
	model := ix.cfg.Model
	return []source{
		{
			name: "message",
			list: func(ctx context.Context, limit int32) ([]candidate, error) {
				rows, err := ix.q.ListMessagesNeedingEmbedding(ctx, sqlc.ListMessagesNeedingEmbeddingParams{Model: model, Limit: limit})
				if err != nil {
					return nil, err
				}
				cs := make([]candidate, len(rows))
				for i, r := range rows {
					cs[i] = candidate{id: r.ID, content: r.Content, embeddedModel: r.EmbeddedModel, embeddedHash: r.EmbeddedHash}
				}
				return cs, nil
			},
			upsert: func(ctx context.Context, id string, hash []byte, vec pgvector.Vector) error {
				return ix.q.UpsertMessageEmbedding(ctx, sqlc.UpsertMessageEmbeddingParams{MessageID: id, Model: model, ContentHash: hash, Embedding: vec})
			},
		},
		{
			name: "summary",
			list: func(ctx context.Context, limit int32) ([]candidate, error) {
				rows, err := ix.q.ListSummariesNeedingEmbedding(ctx, sqlc.ListSummariesNeedingEmbeddingParams{Model: model, Limit: limit})
				if err != nil {
					return nil, err
				}
				cs := make([]candidate, len(rows))
				for i, r := range rows {
					cs[i] = candidate{id: r.ID, content: r.Content, embeddedModel: r.EmbeddedModel, embeddedHash: r.EmbeddedHash}
				}
				return cs, nil
			},
			upsert: func(ctx context.Context, id string, hash []byte, vec pgvector.Vector) error {
				return ix.q.UpsertSummaryEmbedding(ctx, sqlc.UpsertSummaryEmbeddingParams{SummaryID: id, Model: model, ContentHash: hash, Embedding: vec})
			},
		},
		{
			name: "article",
			list: func(ctx context.Context, limit int32) ([]candidate, error) {
				rows, err := ix.q.ListArticlesNeedingEmbedding(ctx, sqlc.ListArticlesNeedingEmbeddingParams{Model: model, Limit: limit})
				if err != nil {
					return nil, err
				}
				cs := make([]candidate, len(rows))
				for i, r := range rows {
					cs[i] = candidate{id: r.ID, content: r.Content, embeddedModel: r.EmbeddedModel, embeddedHash: r.EmbeddedHash}
				}
				return cs, nil
			},
			upsert: func(ctx context.Context, id string, hash []byte, vec pgvector.Vector) error {
				return ix.q.UpsertArticleEmbedding(ctx, sqlc.UpsertArticleEmbeddingParams{ArticleID: id, Model: model, ContentHash: hash, Embedding: vec})
			},
		},
	}
}
