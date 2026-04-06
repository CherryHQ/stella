package config

import (
	"path/filepath"
	"strings"

	"github.com/vaayne/anna/pkg/ai"
)

// ProviderCreds holds credentials for a single provider.
type ProviderCreds struct {
	APIKey  string
	BaseURL string
}

// Snapshot is a read-only config snapshot assembled from DB for downstream
// consumption. It replaces the old *Config for code that needs provider/model
// information for a specific agent.
type Snapshot struct {
	AgentID string // the agent ID this snapshot belongs to

	// Provider, APIKey, BaseURL are the default provider credentials derived
	// from the Model field's provider prefix. Kept for backward compatibility.
	Provider     string
	Model        string
	ModelStrong  string
	ModelFast    string
	Workspace    string
	APIKey       string
	BaseURL      string
	SystemPrompt string // agent's soul/personality from DB
	Runner       RunnerConfig
	Compaction   CompactionConfig
	Heartbeat    HeartbeatConfig
	Scheduler    SchedulerConfig
	Plugins      []Plugin

	// Providers maps provider ID to credentials, enabling per-tier provider
	// resolution when model_strong or model_fast use a different provider.
	Providers map[string]ProviderCreds
}

// ParseModelRef splits a "provider/model" string into its parts.
// If the string contains no "/", it returns ("", ref) as fallback.
func ParseModelRef(ref string) (provider, model string) {
	if i := strings.Index(ref, "/"); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return "", ref
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
// It parses the provider from the model ref and looks up per-provider
// credentials from the Providers map.
func (s *Snapshot) ResolveModelTier(tier string) ai.Model {
	modelRef := s.ResolveModelID(tier)
	provID, modelID := ParseModelRef(modelRef)

	// Fall back to default provider if no prefix.
	if provID == "" {
		provID = s.Provider
	}

	baseURL := s.BaseURL
	if creds, ok := s.Providers[provID]; ok {
		baseURL = creds.BaseURL
	}

	return ai.Model{
		ID:       modelID,
		Name:     modelID,
		API:      provID,
		Provider: provID,
		BaseURL:  baseURL,
	}
}

// ResolveProviderCreds returns the API key and base URL for the given
// provider ID, falling back to the default Provider credentials.
func (s *Snapshot) ResolveProviderCreds(providerID string) ProviderCreds {
	if providerID == "" {
		providerID = s.Provider
	}
	if creds, ok := s.Providers[providerID]; ok {
		return creds
	}
	return ProviderCreds{APIKey: s.APIKey, BaseURL: s.BaseURL}
}

// SkillsPath returns the skills directory inside the workspace.
func (s *Snapshot) SkillsPath() string {
	return filepath.Join(s.Workspace, "skills")
}

// LogPath returns the log file path inside the workspace.
func (s *Snapshot) LogPath() string {
	return filepath.Join(s.Workspace, "anna.log")
}
