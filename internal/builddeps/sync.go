package builddeps

import (
	"context"
	"fmt"
)

// ToolSyncFunc performs target-specific embedded binary synchronization.
type ToolSyncFunc func(context.Context, Config) error

// Syncer orchestrates pre-build dependency sync phases.
type Syncer struct {
	SyncTools ToolSyncFunc
}

// Run normalizes cfg (filling in runtime GOOS/GOARCH/WorkDir defaults),
// validates it, and executes the selected sync phases. Sync funcs receive
// the already-normalized Config.
func (s Syncer) Run(ctx context.Context, cfg Config) error {
	cfg = cfg.Normalized()
	if err := cfg.Validate(); err != nil {
		return err
	}
	if cfg.SyncTools {
		if s.SyncTools == nil {
			return fmt.Errorf("tool sync not implemented")
		}
		if err := s.SyncTools(ctx, cfg); err != nil {
			return err
		}
	}
	return nil
}
