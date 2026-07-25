package config

const GroupMemoryMinimumContextWindow = 128_000

type ProviderModelCost struct {
	Input      float64 `json:"input,omitempty"`
	Output     float64 `json:"output,omitempty"`
	CacheRead  float64 `json:"cacheRead,omitempty"`
	CacheWrite float64 `json:"cacheWrite,omitempty"`
}

type ProviderModel struct {
	ID            string            `json:"id,omitempty"`
	Name          string            `json:"name,omitempty"`
	Enabled       bool              `json:"enabled"`
	Reasoning     bool              `json:"reasoning,omitempty"`
	Input         []string          `json:"input,omitempty"`
	Output        []string          `json:"output,omitempty"`
	ContextWindow int               `json:"contextWindow,omitempty"`
	MaxTokens     int               `json:"maxTokens,omitempty"`
	Cost          ProviderModelCost `json:"cost,omitzero"`
}

// Provider represents an LLM API provider.
type Provider struct {
	ID      string                   `json:"id"`
	Type    string                   `json:"type"`
	Name    string                   `json:"name"`
	Enabled bool                     `json:"enabled"`
	APIKey  string                   `json:"api_key"`
	BaseURL string                   `json:"base_url"`
	Models  map[string]ProviderModel `json:"models,omitempty"`
}
