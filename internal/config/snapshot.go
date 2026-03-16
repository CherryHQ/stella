package config

import (
	"path/filepath"

	"github.com/vaayne/anna/internal/ai"
)

// Snapshot is a read-only config snapshot assembled from DB for downstream
// consumption. It replaces the old *Config for code that needs provider/model
// information for a specific agent.
type Snapshot struct {
	Provider    string
	Model       string
	ModelStrong string
	ModelFast   string
	Workspace   string
	APIKey      string
	BaseURL     string
	Runner      RunnerConfig
	Compaction  CompactionConfig
	Heartbeat   HeartbeatConfig
	Scheduler   SchedulerConfig
	Plugins     []PluginConfig
}

// ResolveModelID returns the model ID string for the given tier,
// falling back to Model if the tier-specific value is not set.
func (s *Snapshot) ResolveModelID(tier string) string {
	switch tier {
	case ModelTierFast:
		if s.ModelFast != "" {
			return s.ModelFast
		}
	default: // strong or unknown tier
		if s.ModelStrong != "" {
			return s.ModelStrong
		}
	}
	return s.Model
}

// ResolveModel returns the ai.Model for the default (strong) tier.
func (s *Snapshot) ResolveModel() ai.Model {
	return s.ResolveModelTier(ModelTierStrong)
}

// ResolveModelTier returns the ai.Model for the given tier, constructing
// a minimal Model from the snapshot's provider and model information.
func (s *Snapshot) ResolveModelTier(tier string) ai.Model {
	modelID := s.ResolveModelID(tier)
	return ai.Model{
		ID:       modelID,
		Name:     modelID,
		API:      s.Provider,
		Provider: s.Provider,
		BaseURL:  s.BaseURL,
	}
}

// SkillsPath returns the skills directory inside the workspace.
func (s *Snapshot) SkillsPath() string {
	return filepath.Join(s.Workspace, "skills")
}

// LogPath returns the log file path inside the workspace.
func (s *Snapshot) LogPath() string {
	return filepath.Join(s.Workspace, "anna.log")
}
