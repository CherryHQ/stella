package sessionmedia

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/vision"
)

// SnapshotLoader is the app-scoped configuration seam needed to resolve the
// deployment vision setting at message time. DBStore already satisfies it.
type SnapshotLoader interface {
	Snapshot(context.Context, string) (*config.Snapshot, error)
}

type snapshotVisionFactory struct {
	loader SnapshotLoader
	build  vision.StreamBuilder
}

// NewSnapshotVisionFactory creates a reloadable factory. It deliberately does
// no caching: image ingestion is low frequency and reading one fresh snapshot
// per message is simpler than coupling it to runner lifetime or cache eviction.
func NewSnapshotVisionFactory(loader SnapshotLoader, build vision.StreamBuilder) (VisionFactory, error) {
	if loader == nil || build == nil {
		return nil, fmt.Errorf("session media vision factory: %w", ErrInvalidInput)
	}
	return snapshotVisionFactory{loader: loader, build: build}, nil
}

func (f snapshotVisionFactory) ForMessage(ctx context.Context, agentID string) vision.BaselineRenderer {
	snapshot, err := f.loader.Snapshot(ctx, agentID)
	if err != nil {
		return vision.New(vision.Options{})
	}
	return vision.NewFromSnapshot(snapshot, f.build)
}
