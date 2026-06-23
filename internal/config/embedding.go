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

// EmbeddingSettings is the deployment-wide semantic-search configuration, edited
// in the web settings page and stored as one JSON value in app_setting. The lane
// is opt-in: with Enabled false (the default) search stays pure-BM25. APIKey is
// stored as-is, consistent with how provider credentials are stored.
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
