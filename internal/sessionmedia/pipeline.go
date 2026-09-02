package sessionmedia

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/internal/vision"
	"github.com/CherryHQ/stella/pkg/ai"
)

// Pipeline is the complete ordinary-session image module. Construction binds
// persistence, reloadable baseline rendering, canonical enrichment, and active
// hydration so callers cannot wire only half of the policy.
type Pipeline struct {
	media    *mediaStore
	enricher *enricher

	// riverMu guards the one-shot River bind that arms the orphan sweep. The
	// pipeline works fully without it; the sweep is storage hygiene, not part of
	// any request path.
	riverMu      sync.Mutex
	river        *river.Client[pgx.Tx]
	sweepStarted bool
}

func NewPipeline(media *asset.SessionMedia, db *pgxpool.Pool, snapshots SnapshotLoader, build vision.StreamBuilder, opts PipelineOptions) (*Pipeline, error) {
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
// already carry a baseline are skipped, and so are references whose media object
// was described by an earlier reader.
//
// The baseline is a property of the ctx_media row, keyed by owner and sha256, so
// one image forwarded into ten messages costs exactly one VLM pass. First write
// wins and the description is then immutable; a reader that loses the race
// adopts the stored text so every message shows the same description.
func (p *Pipeline) RenderBaselines(ctx context.Context, owner Owner, agentID string, blocks []ai.ContentBlock) ([]ai.ContentBlock, error) {
	return p.enricher.RenderBaselines(ctx, owner, agentID, blocks)
}

func (p *Pipeline) Load(ctx context.Context, owner Owner, mediaID string) (ai.ImageContent, error) {
	return p.media.Load(ctx, owner, mediaID)
}

// PurgeOwner drops every media object an owner holds. Deleting an owner
// cascades the ctx_media rows away, but the immutable blobs are outside the
// database and nothing else would ever reclaim them.
//
// An agent is not a media owner: agent-produced images belong to the user or
// group whose session produced them, so an agent deletion has nothing to purge.
func (p *Pipeline) PurgeOwner(ctx context.Context, kind home.OwnerKind, id string) error {
	var owner Owner
	switch kind {
	case home.OwnerUser:
		parsed, err := uuid.Parse(id)
		if err != nil {
			return fmt.Errorf("purge session media: %w", err)
		}
		owner = UserOwner(parsed)
	case home.OwnerGroup:
		parsed, err := uuid.Parse(id)
		if err != nil {
			return fmt.Errorf("purge session media: %w", err)
		}
		owner = GroupOwner(parsed)
	default:
		return nil
	}
	return p.media.PurgeOwner(ctx, owner)
}
