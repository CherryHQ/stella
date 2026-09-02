package sessionmedia

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/vision"
)

// SnapshotLoader is the app-scoped configuration seam needed to resolve the
// deployment vision setting at message time. DBStore already satisfies it.
type SnapshotLoader interface {
	Snapshot(context.Context, string) (*config.Snapshot, error)
}

// newSnapshotVisionFactory creates a reloadable factory. It deliberately does
// no caching: image ingestion is low frequency and reading one fresh snapshot
// per message is simpler than coupling it to runner lifetime or cache eviction.
// A snapshot that cannot be read is not an error here: the message falls back
// to a model-less service, which is the local Xberg ladder.
func newSnapshotVisionFactory(loader SnapshotLoader, build vision.StreamBuilder) (visionFactory, error) {
	if loader == nil || build == nil {
		return nil, fmt.Errorf("session media vision factory: %w", ErrInvalidInput)
	}
	return func(ctx context.Context, agentID string) vision.BaselineRenderer {
		snapshot, err := loader.Snapshot(ctx, agentID)
		if err != nil {
			return vision.New(vision.Options{})
		}
		return vision.NewFromSnapshot(snapshot, build)
	}, nil
}
