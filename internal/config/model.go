package config

import "github.com/vaayne/anna/internal/ai"

// Model tier constants.
const (
	ModelTierStrong = "strong"
	ModelTierFast   = "fast"
)

// ModelConfig describes a model's metadata (used in model listing/caching).
type ModelConfig struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	API           string            `json:"api"`
	Reasoning     bool              `json:"reasoning"`
	Input         []string          `json:"input"`
	ContextWindow int               `json:"context_window"`
	MaxTokens     int               `json:"max_tokens"`
	Headers       map[string]string `json:"headers"`
	Cost          *ModelCostConfig  `json:"cost"`
}

// ModelCostConfig describes the cost of a model per million tokens.
type ModelCostConfig struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

// ModelConfigToAI converts a ModelConfig to an ai.Model.
func ModelConfigToAI(provider string, m ModelConfig) ai.Model {
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
