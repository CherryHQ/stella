package config

// ValuePtr is a small helper for constructing presence-aware configuration in
// callers and tests.
func ValuePtr[T any](value T) *T { return &value }

// ProviderModelCost preserves field presence at the config/API boundary. A nil
// rate inherits the lower layer; a non-nil pointer to zero means free.
type ProviderModelCost struct {
	Input       *float64                `json:"input,omitempty"`
	Output      *float64                `json:"output,omitempty"`
	CacheRead   *float64                `json:"cacheRead,omitempty"`
	CacheWrite  *float64                `json:"cacheWrite,omitempty"`
	Reasoning   *float64                `json:"reasoning,omitempty"`
	InputAudio  *float64                `json:"inputAudio,omitempty"`
	OutputAudio *float64                `json:"outputAudio,omitempty"`
	Tiers       []ProviderModelCostTier `json:"tiers,omitempty"`
}

type ProviderModelCostTier struct {
	MinContext  int      `json:"minContext"`
	Input       *float64 `json:"input,omitempty"`
	Output      *float64 `json:"output,omitempty"`
	CacheRead   *float64 `json:"cacheRead,omitempty"`
	CacheWrite  *float64 `json:"cacheWrite,omitempty"`
	Reasoning   *float64 `json:"reasoning,omitempty"`
	InputAudio  *float64 `json:"inputAudio,omitempty"`
	OutputAudio *float64 `json:"outputAudio,omitempty"`
}

// ProviderModelOverride is the sparse persisted overlay. Nil means inherit,
// including for Enabled and empty modality slices.
type ProviderModelOverride struct {
	// CatalogModel binds a hosted model ID to provider-agnostic lab metadata.
	// Nil keeps automatic matching; empty explicitly disables Catalog metadata.
	CatalogModel  *string            `json:"catalogModel,omitempty"`
	Name          *string            `json:"name,omitempty"`
	Enabled       *bool              `json:"enabled,omitempty"`
	Reasoning     *bool              `json:"reasoning,omitempty"`
	Input         *[]string          `json:"input,omitempty"`
	Output        *[]string          `json:"output,omitempty"`
	ContextWindow *int               `json:"contextWindow,omitempty"`
	MaxTokens     *int               `json:"maxTokens,omitempty"`
	Cost          *ProviderModelCost `json:"cost,omitempty"`
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
	ID          string                           `json:"id"`
	Type        string                           `json:"type"`
	Name        string                           `json:"name"`
	Enabled     bool                             `json:"enabled"`
	APIKey      string                           `json:"api_key"`
	BaseURL     string                           `json:"base_url"`
	Models      map[string]ProviderModelOverride `json:"models,omitempty"`
	CatalogID   string                           `json:"catalog_id,omitempty"`
	ModelPolicy string                           `json:"model_policy,omitempty"`
}

// ProviderSnapshot binds a provider projection to the durable row version that
// must be supplied for a conditional Settings mutation. Both values come from
// one database row read, so a caller cannot pair old fields with a new version.
type ProviderSnapshot struct {
	Provider Provider
	Version  string
}

// ProviderIndex resolves the provider half of a model reference to a provider
// row. Every model role goes through it — the three agent tiers, vision, and
// embedding — so "openai/text-embedding-3-small" names the same account no
// matter which subsystem reads it.
//
// Besides the canonical row ID it accepts a provider *type* name, but only while
// exactly one provider of that type is configured. That alias disappears on its
// own once a second one appears: at that point the reference is genuinely
// ambiguous, and guessing would quietly bill someone's other account.
type ProviderIndex struct{ byRef map[string]Provider }

// NewProviderIndex builds the lookup from a provider catalog.
func NewProviderIndex(providers []Provider) ProviderIndex {
	byRef := make(map[string]Provider, len(providers))
	typeCount := make(map[string]int)
	for _, p := range providers {
		byRef[p.ID] = p
		if p.Type != "" {
			typeCount[p.Type]++
		}
	}
	for _, p := range providers {
		if p.Type != "" && typeCount[p.Type] == 1 {
			if _, taken := byRef[p.Type]; !taken {
				byRef[p.Type] = p
			}
		}
	}
	return ProviderIndex{byRef: byRef}
}

// Lookup returns the provider a reference names. The empty reference never
// resolves: it means "no model configured", not "any provider".
func (ix ProviderIndex) Lookup(ref string) (Provider, bool) {
	if ref == "" {
		return Provider{}, false
	}
	p, ok := ix.byRef[ref]
	return p, ok
}
