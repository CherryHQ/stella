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
	// ModelTierVision is the auxiliary multimodal tier used to render images as
	// text. It is the one tier that does not fall back to the default model:
	// see ResolveVisionModel.
	ModelTierVision = "vision"
)

// ProviderCreds holds credentials for a single provider.
type ProviderCreds struct {
	Type    string
	APIKey  string
	BaseURL string
	// ProviderID is the canonical provider row ID this entry resolves to. It is
	// stable even when the map key is a type alias (a compatibility lookup that
	// disappears once a second provider of that type exists), so an Agent
	// credential override — keyed by canonical ID — can be applied to every map
	// entry, alias or not, that points at the same provider. Empty only on the
	// legacy default-credential fallback path.
	ProviderID string
}

// ModelKey identifies one model within one provider. Model IDs may themselves
// contain "/" (e.g. "openrouter/anthropic/claude"), so the two parts are kept
// separate instead of being joined back into a ref string.
type ModelKey struct {
	Provider string
	Model    string
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
	// ModelVision is the deployment-wide vision model (VisionSettings), copied
	// into the snapshot so tier resolution treats it like any other tier. It is
	// the one model field here that does not come from the agent row.
	ModelVision  string
	Workspace    string
	Sandbox      SandboxConfig
	APIKey       string
	BaseURL      string
	SystemPrompt string // agent's base system prompt from DB
	Soul         string // agent's default soul from DB (fallback for all users)

	Runner     RunnerConfig
	Compaction CompactionConfig
	Scheduler  SchedulerConfig
	Plugins    []Plugin

	// Providers maps provider ID to credentials, enabling per-tier provider
	// resolution when model_strong or model_fast use a different provider.
	Providers map[string]ProviderCreds

	// ModelInputs carries the input modalities each model declares in provider
	// config (e.g. ["text", "image"]). Only this one per-model field is
	// snapshotted: it is what downstream capability checks need, and the rest
	// of ProviderModel has no consumer here. A missing entry means the
	// deployment never declared modalities, not that there are none.
	ModelInputs map[ModelKey][]string
}

// lookupProvider returns credentials by their snapshot key or canonical
// provider row ID. The latter matters when a model was originally configured
// through a unique provider-type alias but is later passed between runners by
// its stable canonical ID.
func (s *Snapshot) lookupProvider(providerID string) (string, ProviderCreds, bool) {
	if creds, ok := s.Providers[providerID]; ok {
		return providerID, creds, true
	}
	if providerID != "" {
		for lookupID, creds := range s.Providers {
			if creds.ProviderID == providerID {
				return lookupID, creds, true
			}
		}
	}
	return "", ProviderCreds{}, false
}

// ModelInput returns the input modalities declared for a provider's model, or
// nil when none were declared.
func (s *Snapshot) ModelInput(providerID, modelID string) []string {
	lookupID := providerID
	if id, _, ok := s.lookupProvider(providerID); ok {
		lookupID = id
	}
	return s.ModelInputs[ModelKey{Provider: lookupID, Model: modelID}]
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
	case ModelTierVision:
		// No fallback on purpose: an unset vision tier means the deployment has
		// no image-understanding model, not "use the main one". Falling back
		// would send images straight back to the model that cannot read them.
		return s.ModelVision
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
	if _, creds, ok := s.lookupProvider(provID); ok {
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
		Input:    s.ModelInput(provID, modelID),
	}
}

// ResolveVisionModel returns the deployment's auxiliary vision model and true
// when one is configured. It reports false — rather than a model the caller must
// then second-guess — when the vision tier is unset, which is the signal that
// image understanding must degrade to local text extraction.
func (s *Snapshot) ResolveVisionModel() (ai.Model, bool) {
	if s.ModelVision == "" {
		return ai.Model{}, false
	}
	return s.ResolveModelTier(ModelTierVision), true
}

// ResolveProviderCreds returns the API key and base URL for the given
// provider ID, falling back to the default Provider credentials.
func (s *Snapshot) ResolveProviderCreds(providerID string) ProviderCreds {
	if providerID == "" {
		providerID = s.Provider
	}
	if _, creds, ok := s.lookupProvider(providerID); ok {
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
