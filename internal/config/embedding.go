package config

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// EmbeddingSettingKey is the app_setting key under which the singleton embedding
// configuration JSON is stored (same key-value mechanism as runner/compaction/
// scheduler settings).
const EmbeddingSettingKey = "embedding"

// DefaultEmbeddingDim is the day-1 canonical vector width: OpenAI's
// text-embedding-3-* family emits 1536-dim vectors natively, matching the
// sidecar storage width with no padding.
const DefaultEmbeddingDim = 1536

// SettingStore is the slice of Store the embedding helpers need: the key-value
// setting accessors. Narrowing the dependency keeps the helpers testable with a
// trivial fake instead of the full Store; config.Store satisfies it.
type SettingStore interface {
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
}

// EmbeddingStore is the slice of Store embedding resolution needs: the settings
// plus the provider catalog the embedding model reference is resolved against.
type EmbeddingStore interface {
	SettingStore
	ListProviders(ctx context.Context) ([]Provider, error)
}

// EmbeddingSettings is the deployment-wide semantic-search lane, edited in the
// web settings page and stored as one JSON value in app_setting. The lane is
// opt-in: with Enabled false (the default) search stays pure-BM25.
//
// These are the lane's own operational knobs and nothing else. Which model it
// runs on, and the credentials to call that model with, are named in
// DefaultModels like every other model role.
type EmbeddingSettings struct {
	Enabled   bool `json:"enabled"`
	Dim       int  `json:"dim"`
	Normalize bool `json:"normalize"`
}

// LoadEmbeddingSettings reads the singleton embedding config, filling defaults for
// an unset deployment (disabled, default dim) so callers always get a usable
// struct. A missing row is not an error — it means "never configured".
func LoadEmbeddingSettings(ctx context.Context, store SettingStore) (EmbeddingSettings, error) {
	raw, err := store.GetSetting(ctx, EmbeddingSettingKey)
	if err != nil {
		return EmbeddingSettings{}, err
	}
	s := EmbeddingSettings{Dim: DefaultEmbeddingDim}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return EmbeddingSettings{}, fmt.Errorf("parse embedding settings: %w", err)
		}
	}
	if s.Dim <= 0 {
		s.Dim = DefaultEmbeddingDim
	}
	return s, nil
}

// SaveEmbeddingSettings persists the singleton embedding config.
func SaveEmbeddingSettings(ctx context.Context, store SettingStore, s EmbeddingSettings) error {
	b, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal embedding settings: %w", err)
	}
	return store.SetSetting(ctx, EmbeddingSettingKey, string(b))
}

// EmbeddingRuntime is the resolved configuration the embedding lane runs on: the
// canonical provider it belongs to, the bare model id that goes on the wire, and
// the credentials to call it with. It is what the lane's knobs and the
// deployment's embedding model reference collapse into, so no runtime caller has
// to resolve anything itself.
//
// Provider is the canonical provider row ID even when the reference reached it
// through a type alias. It is part of the vector-space identity: two accounts
// serving the same model name are not the same space.
type EmbeddingRuntime struct {
	Enabled   bool
	Provider  string
	Model     string
	Dim       int
	APIKey    string
	BaseURL   string
	Normalize bool
}

// ResolveEmbedding returns the effective embedding configuration.
//
// DefaultModels.ModelEmbedding is the only source: its provider half names the
// credentials, its model half is what goes on the wire, and both are resolved
// through the same ProviderIndex the agent tiers and the vision model use.
//
// A reference that does not resolve — never configured, or pointing at a
// provider row that has since been deleted — comes back disabled rather than as
// an error. The lane is an optional accelerator, and a deployment whose
// embedding provider disappeared should degrade to keyword search, not fail
// every query. Enabled is therefore the effective state, not the stored flag,
// which is what lets the setting be saved in any order without a window where
// the lane claims to be running against credentials it does not have.
func ResolveEmbedding(ctx context.Context, store EmbeddingStore) (EmbeddingRuntime, error) {
	s, err := LoadEmbeddingSettings(ctx, store)
	if err != nil {
		return EmbeddingRuntime{}, err
	}
	rt := EmbeddingRuntime{Dim: s.Dim, Normalize: s.Normalize}

	def, err := LoadDefaultModels(ctx, store)
	if err != nil {
		return EmbeddingRuntime{}, err
	}
	providerRef, modelID := ParseModelRef(def.ModelEmbedding)
	if modelID == "" {
		return rt, nil
	}
	providers, err := store.ListProviders(ctx)
	if err != nil {
		return EmbeddingRuntime{}, err
	}
	p, ok := NewProviderIndex(providers).Lookup(providerRef)
	if !ok {
		return rt, nil
	}

	rt.Enabled = s.Enabled
	rt.Provider, rt.Model = p.ID, modelID
	rt.APIKey, rt.BaseURL = p.APIKey, p.BaseURL
	return rt, nil
}
