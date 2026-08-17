package config

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// VisionSettingKey is the app_setting key under which the singleton vision
// configuration JSON is stored (same key-value mechanism as the embedding,
// runner, compaction and scheduler settings).
const VisionSettingKey = "vision"

// VisionConfigAdvisoryLockKey serializes output-affecting Vision configuration
// writes with Library OCR publication. It is transaction-scoped in PostgreSQL,
// so process crashes cannot leave the fence locked.
const VisionConfigAdvisoryLockKey int64 = 0x5354454c4c415649

// VisionSettings is the deployment-wide image-understanding configuration for
// chat images and scanned Library PDF OCR.
//
// The vision model is infrastructure, not personality: it transcribes and
// describes an image so a model that cannot see it still receives its content.
// There is no reason for two agents in one deployment to read the same image
// differently, and per-agent configuration would mean every new agent silently
// starts without image understanding — so this is one deployment-wide setting.
//
// Model is a "provider/model" reference resolved against the configured
// providers. Empty means the deployment has no vision model, and image
// understanding degrades to local text extraction.
type VisionSettings struct {
	Model string `json:"model"`
}

// LoadVisionSettings reads the singleton vision config. A missing row is not an
// error — it means "never configured", which is the same as having no vision
// model.
func LoadVisionSettings(ctx context.Context, store SettingStore) (VisionSettings, error) {
	raw, err := store.GetSetting(ctx, VisionSettingKey)
	if err != nil {
		return VisionSettings{}, err
	}
	var s VisionSettings
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return VisionSettings{}, fmt.Errorf("parse vision settings: %w", err)
		}
	}
	s.Model = strings.TrimSpace(s.Model)
	return s, nil
}

// SaveVisionSettings persists the singleton vision config.
func SaveVisionSettings(ctx context.Context, store SettingStore, s VisionSettings) error {
	s.Model = strings.TrimSpace(s.Model)
	b, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal vision settings: %w", err)
	}
	return store.SetSetting(ctx, VisionSettingKey, string(b))
}
