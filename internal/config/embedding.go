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

// Day-1 canonical vector space: OpenAI text-embedding-3-small emits 1536-dim
// vectors natively, matching the sidecar storage width with no padding.
const (
	DefaultEmbeddingModel = "text-embedding-3-small"
	DefaultEmbeddingDim   = 1536
)

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

// EmbeddingSettings is the deployment-wide semantic-search configuration, edited
// in the web settings page and stored as one JSON value in app_setting. The lane
// is opt-in: with Enabled false (the default) search stays pure-BM25.
//
// Enabled, Dim and Normalize are the lane's own operational knobs. Model, APIKey
// and BaseURL are legacy: the embedding model is now named in DefaultModels like
// every other model role, and its credentials come from that model's provider.
// These three are still read — and only read — when DefaultModels.ModelEmbedding
// is unset, which is the state a deployment lands in when the unification
// migration could not match its stored key to a provider row. They are not
// writable through the API; pointing the deployment at a provider clears them
// from the resolution path for good.
type EmbeddingSettings struct {
	Enabled   bool   `json:"enabled"`
	Model     string `json:"model"`
	Dim       int    `json:"dim"`
	APIKey    string `json:"api_key"`
	BaseURL   string `json:"base_url"`
	Normalize bool   `json:"normalize"`
}

// LoadEmbeddingSettings reads the singleton embedding config, filling defaults for
// an unset deployment (disabled, default model/dim) so callers always get a usable
// struct. A missing row is not an error — it means "never configured".
func LoadEmbeddingSettings(ctx context.Context, store SettingStore) (EmbeddingSettings, error) {
	raw, err := store.GetSetting(ctx, EmbeddingSettingKey)
	if err != nil {
		return EmbeddingSettings{}, err
	}
	s := EmbeddingSettings{Model: DefaultEmbeddingModel, Dim: DefaultEmbeddingDim}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return EmbeddingSettings{}, fmt.Errorf("parse embedding settings: %w", err)
		}
	}
	if s.Model == "" {
		s.Model = DefaultEmbeddingModel
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

// EmbeddingRuntime is the resolved configuration the embedding lane runs on: a
// bare model id plus the credentials to call it with. It is what the two config
// sources — the deployment's embedding model reference and the lane's own knobs —
// collapse into, so no runtime caller has to know which source won.
type EmbeddingRuntime struct {
	Enabled   bool
	Model     string
	Dim       int
	APIKey    string
	BaseURL   string
	Normalize bool
}

// ResolveEmbedding returns the effective embedding configuration.
//
// DefaultModels.ModelEmbedding is the source of truth: its provider half names
// the credentials, its model half is what goes on the wire. A deployment that
// has not been pointed at a provider yet falls back to the legacy embedding
// block so its lane keeps running unchanged.
//
// A reference whose provider row is gone resolves to no credentials rather than
// to the legacy key: silently embedding against a different account would poison
// the vector space with a second model's geometry. The lane treats missing
// credentials as "disabled", which is the visible, recoverable failure.
func ResolveEmbedding(ctx context.Context, store EmbeddingStore) (EmbeddingRuntime, error) {
	s, err := LoadEmbeddingSettings(ctx, store)
	if err != nil {
		return EmbeddingRuntime{}, err
	}
	rt := EmbeddingRuntime{Enabled: s.Enabled, Dim: s.Dim, Normalize: s.Normalize}

	def, err := LoadDefaultModels(ctx, store)
	if err != nil {
		return EmbeddingRuntime{}, err
	}
	if def.ModelEmbedding == "" {
		rt.Model, rt.APIKey, rt.BaseURL = s.Model, s.APIKey, s.BaseURL
		return rt, nil
	}

	providerID, modelID := ParseModelRef(def.ModelEmbedding)
	rt.Model = modelID
	providers, err := store.ListProviders(ctx)
	if err != nil {
		return EmbeddingRuntime{}, err
	}
	if p, ok := FindProvider(providers, providerID); ok {
		rt.APIKey, rt.BaseURL = p.APIKey, p.BaseURL
	}
	return rt, nil
}

// FindProvider resolves a model reference's provider half to a provider row: by
// canonical ID, or by provider type when exactly one provider of that type
// exists. The type alias is the same compatibility lookup snapshot credential
// resolution performs, and it disappears on its own once a second provider of
// that type is configured.
func FindProvider(providers []Provider, ref string) (Provider, bool) {
	if ref == "" {
		return Provider{}, false
	}
	var byType Provider
	typeCount := 0
	for _, p := range providers {
		if p.ID == ref {
			return p, true
		}
		if p.Type == ref {
			byType = p
			typeCount++
		}
	}
	if typeCount == 1 {
		return byType, true
	}
	return Provider{}, false
}
