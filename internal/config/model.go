package config

import "github.com/vaayne/anna/internal/ai"

// Model tier constants.
const (
	ModelTierStrong = "strong"
	ModelTierFast   = "fast"
)

type ModelConfig struct {
	ID            string            `yaml:"id"`
	Name          string            `yaml:"name"`
	API           string            `yaml:"api"`
	Reasoning     bool              `yaml:"reasoning"`
	Input         []string          `yaml:"input"`
	ContextWindow int               `yaml:"context_window"`
	MaxTokens     int               `yaml:"max_tokens"`
	Headers       map[string]string `yaml:"headers"`
	Cost          *ModelCostConfig  `yaml:"cost"`
}

type ModelCostConfig struct {
	Input      float64 `yaml:"input"`
	Output     float64 `yaml:"output"`
	CacheRead  float64 `yaml:"cache_read"`
	CacheWrite float64 `yaml:"cache_write"`
}

// ResolveModel returns the ai.Model for the default provider/model.
func (cfg *Config) ResolveModel() ai.Model {
	return cfg.ResolveModelTier(ModelTierStrong)
}

// ResolveModelTier returns the model for the given tier after applying
// the fallback: strong -> model, fast -> model.
func (cfg *Config) ResolveModelTier(tier string) ai.Model {
	modelID := cfg.ResolveModelID(tier)
	providerCfg := cfg.Providers[cfg.Provider]
	for _, m := range providerCfg.Models {
		if m.ID == modelID {
			return modelConfigToType(cfg.Provider, m)
		}
	}
	return ai.Model{
		ID:       modelID,
		Name:     modelID,
		API:      cfg.Provider,
		Provider: cfg.Provider,
		BaseURL:  providerCfg.BaseURL,
	}
}

// ResolveModelID returns the model ID string for the given tier,
// falling back to Model if the tier-specific value is not set.
func (cfg *Config) ResolveModelID(tier string) string {
	switch tier {
	case ModelTierFast:
		if cfg.ModelFast != "" {
			return cfg.ModelFast
		}
	default: // strong or unknown tier
		if cfg.ModelStrong != "" {
			return cfg.ModelStrong
		}
	}
	return cfg.Model
}

func modelConfigToType(provider string, m ModelConfig) ai.Model {
	model := ai.Model{
		ID:            m.ID,
		Name:          m.ID,
		API:           m.API,
		Provider:      provider,
		Reasoning:     m.Reasoning,
		Input:         m.Input,
		ContextWindow: m.ContextWindow,
		MaxTokens:     m.MaxTokens,
		Headers:       m.Headers,
	}
	if m.Name != "" && model.Name == "" {
		model.Name = m.Name
	}
	if model.API == "" {
		model.API = provider
	}
	if m.Cost != nil {
		model.Cost = ai.ModelCost{
			Input:      m.Cost.Input,
			Output:     m.Cost.Output,
			CacheRead:  m.Cost.CacheRead,
			CacheWrite: m.Cost.CacheWrite,
		}
	}
	return model
}
