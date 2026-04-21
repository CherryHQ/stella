package builddeps

import (
	"context"
	"fmt"
)

// SkillSyncFunc performs host-side skill synchronization.
type SkillSyncFunc func(context.Context, Config) error

// ToolSyncFunc performs target-specific embedded binary synchronization.
type ToolSyncFunc func(context.Context, Config) error

// Syncer orchestrates pre-build dependency sync phases.
type Syncer struct {
	SyncSkills SkillSyncFunc
	SyncTools  ToolSyncFunc
}

// Run validates cfg, fills defaults, and executes the selected sync phases.
func (s Syncer) Run(ctx context.Context, cfg Config) error {
	cfg = cfg.Normalized()
	if err := cfg.Validate(); err != nil {
		return err
	}
	if cfg.SyncSkills {
		if s.SyncSkills == nil {
			return fmt.Errorf("skill sync not implemented")
		}
		if err := s.SyncSkills(ctx, cfg); err != nil {
			return err
		}
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
