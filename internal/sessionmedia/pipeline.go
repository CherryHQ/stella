package sessionmedia

import (
	"context"

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

// Enrich stores originals and renders baselines in one pass. Direct sessions
// use it: every image a user or tool produces is going to be read by the agent
// on this very turn, so there is nothing to defer.
func (p *Pipeline) Enrich(ctx context.Context, owner Owner, agentID string, blocks []ai.ContentBlock) ([]ai.ContentBlock, error) {
	return p.enricher.Enrich(ctx, owner, agentID, blocks)
}

// Persist stores originals only, returning references without baselines.
// Group ingestion uses it because most group images never wake an agent.
func (p *Pipeline) Persist(ctx context.Context, owner Owner, blocks []ai.ContentBlock) ([]ai.ContentBlock, error) {
	return p.enricher.Persist(ctx, owner, blocks)
}

// RenderBaselines completes references that Persist left bare. It is the lazy
// half of the group path and is safe to call repeatedly: references that
// already carry a baseline are skipped.
//
// The baseline is a property of the message block, not of the media object, so
// the same image forwarded into two messages is described once per message.
// That ceiling holds while a repeat is a human forwarding a picture; move the
// baseline onto ctx_media, keyed by owner and sha256, once duplicate renders
// for one owner are a visible share of VLM cost.
func (p *Pipeline) RenderBaselines(ctx context.Context, owner Owner, agentID string, blocks []ai.ContentBlock) ([]ai.ContentBlock, error) {
	return p.enricher.RenderBaselines(ctx, owner, agentID, blocks)
}

func (p *Pipeline) Load(ctx context.Context, owner Owner, mediaID string) (ai.ImageContent, error) {
	return p.media.Load(ctx, owner, mediaID)
}
