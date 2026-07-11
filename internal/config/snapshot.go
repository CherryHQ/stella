package config

import (
	"path/filepath"
	"strings"

	"github.com/CherryHQ/stella/pkg/ai"
)

// Model tier constants.
const (
	ModelTierStrong = "strong"
	ModelTierFast   = "fast"
)

// ProviderCreds holds credentials for a single provider.
type ProviderCreds struct {
	Type    string
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
	Provider            string
	Model               string
	ModelThinking       string
	ModelStrong         string
	ModelStrongThinking string
	ModelFast           string
	ModelFastThinking   string
	Workspace           string
	Sandbox             SandboxConfig
	APIKey              string
	BaseURL             string
	SystemPrompt        string // agent's base system prompt from DB
	Soul                string // agent's default soul from DB (fallback for all users)

	Runner     RunnerConfig
	Compaction CompactionConfig
	Scheduler  SchedulerConfig
	Plugins    []Plugin

	// Providers maps provider ID to credentials, enabling per-tier provider
	// resolution when model_strong or model_fast use a different provider.
	Providers map[string]ProviderCreds
}

// ParseModelRef splits a "provider/model" string into its parts.
// If the string contains no "/", it returns ("", ref) as fallback.
func ParseModelRef(ref string) (provider, model string) {
	if provider, model, ok := strings.Cut(ref, "/"); ok {
		return provider, model
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

// ResolveThinkingLevel returns the thinking level for the given tier,
// falling back to the default model's setting when a tier-specific setting is unset.
func (s *Snapshot) ResolveThinkingLevel(tier string) ai.ThinkingLevel {
	switch tier {
	case ModelTierFast:
		if s.ModelFastThinking != "" {
			return s.ModelFastThinking
		}
	default: // strong or unknown tier
		if s.ModelStrongThinking != "" {
			return s.ModelStrongThinking
		}
	}
	return s.ModelThinking
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

	api := provID
	baseURL := s.BaseURL
	if creds, ok := s.Providers[provID]; ok {
		if creds.Type != "" {
			api = creds.Type
		}
		baseURL = creds.BaseURL
	}

	return ai.Model{
		ID:       modelID,
		Name:     modelID,
		API:      api,
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
	return ProviderCreds{Type: s.Provider, APIKey: s.APIKey, BaseURL: s.BaseURL}
}

// SkillsPath returns the per-workspace agent-level skills directory.
func (s *Snapshot) SkillsPath() string {
	return filepath.Join(s.Workspace, ".agents", "skills")
}

// LogPath returns the log file path inside the workspace.
func (s *Snapshot) LogPath() string {
	return filepath.Join(s.Workspace, "stella.log")
}
