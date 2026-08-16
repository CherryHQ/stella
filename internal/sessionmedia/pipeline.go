package sessionmedia

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/vision"
	"github.com/CherryHQ/stella/pkg/ai"
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
	factory, err := newSnapshotVisionFactory(snapshots, build, opts.StorageAdmission)
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
	return p.enricher.Enrich(ctx, principal, agentID, blocks)
}

func (p *Pipeline) Load(ctx context.Context, userID, mediaID string) (ai.ImageContent, error) {
	principal, err := uuid.Parse(userID)
	if err != nil {
		return ai.ImageContent{}, ErrNotFound
	}
	return p.media.Load(ctx, principal, mediaID)
}
