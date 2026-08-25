package sessionmedia

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/vision"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Pipeline is the complete ordinary-session image module. Construction binds
// persistence, reloadable baseline rendering, canonical enrichment, and active
// hydration so callers cannot wire only half of the policy.
type Pipeline struct {
	media    *mediaStore
	enricher *enricher
}

func NewPipeline(media asset.SessionMediaStore, db *pgxpool.Pool, snapshots SnapshotLoader, build vision.StreamBuilder, opts PipelineOptions) (*Pipeline, error) {
	service, err := newMediaStore(media, db)
	if err != nil {
		return nil, err
	}
	factory, err := newSnapshotVisionFactory(snapshots, build)
	if err != nil {
		return nil, err
	}
	enricher, err := newEnricher(service, factory, opts)
	if err != nil {
		return nil, err
	}
	return &Pipeline{media: service, enricher: enricher}, nil
}

func (p *Pipeline) Enrich(ctx context.Context, userID, agentID string, blocks []ai.ContentBlock) ([]ai.ContentBlock, error) {
	principal, err := uuid.Parse(userID)
	if err != nil {
		return nil, fmt.Errorf("invalid session image user")
	}
	canonical, err := p.enricher.Enrich(ctx, principal, agentID, blocks)
	if err != nil {
		return nil, err
	}
	return enrichFiles(ctx, p.media, principal, canonical)
}

// EnrichWithQueries makes immutable media metadata part of the caller's
// transaction. Content-addressed bytes are written first; if the caller later
// rejects admission, its rollback leaves no durable media row or dangling
// queue reference.
func (p *Pipeline) EnrichWithQueries(ctx context.Context, q *sqlc.Queries, userID, agentID string, blocks []ai.ContentBlock) ([]ai.ContentBlock, error) {
	principal, err := uuid.Parse(userID)
	if err != nil || q == nil {
		return nil, fmt.Errorf("invalid transactional session image admission")
	}
	enricher := *p.enricher
	enricher.media = queryPersister{store: p.media, q: q}
	// A pgx transaction owns one connection and does not support concurrent
	// queries. Blob persistence may ordinarily fan out, but transaction-coupled
	// admission serializes its metadata writes on that connection.
	enricher.workers = 1
	canonical, err := enricher.Enrich(ctx, principal, agentID, blocks)
	if err != nil {
		return nil, err
	}
	return enrichFiles(ctx, queryPersister{store: p.media, q: q}, principal, canonical)
}

func enrichFiles(ctx context.Context, media persister, principal uuid.UUID, blocks []ai.ContentBlock) ([]ai.ContentBlock, error) {
	out := ai.CloneContentBlocks(blocks)
	for index, block := range blocks {
		file, ok := block.(ai.FileContent)
		if !ok {
			continue
		}
		if len(file.Data) == 0 || len(file.Data) > 32<<20 || file.Name == "" || file.Path == "" || file.MimeType == "" {
			return nil, fmt.Errorf("session attachment %d: %w", index, ErrInvalidInput)
		}
		mediaID, err := media.Persist(ctx, Input{UserID: principal, Data: file.Data, MimeType: file.MimeType})
		if err != nil {
			return nil, fmt.Errorf("persist canonical session attachment: %w", err)
		}
		ref := ai.FileRefContent{MediaID: mediaID, Name: file.Name, Path: file.Path}
		if err := ref.Validate(); err != nil {
			return nil, fmt.Errorf("session attachment %d: %w", index, err)
		}
		out[index] = ref
	}
	return out, nil
}

func (p *Pipeline) Load(ctx context.Context, userID, mediaID string) (ai.ImageContent, error) {
	principal, err := uuid.Parse(userID)
	if err != nil {
		return ai.ImageContent{}, ErrNotFound
	}
	return p.media.Load(ctx, principal, mediaID)
}
