package builddeps

import "context"

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
	if cfg.SyncSkills && s.SyncSkills != nil {
		if err := s.SyncSkills(ctx, cfg); err != nil {
			return err
		}
	}
	if cfg.SyncTools && s.SyncTools != nil {
		if err := s.SyncTools(ctx, cfg); err != nil {
			return err
		}
	}
	return nil
}
