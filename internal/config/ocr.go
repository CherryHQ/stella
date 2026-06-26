package config

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// OCRSettingKey is the app_setting key under which the singleton local-OCR
// configuration JSON is stored (same key-value mechanism as embedding settings).
const OCRSettingKey = "ocr"

// OCRSettings is the deployment-wide local-OCR configuration, edited in the web
// settings page and stored as one JSON value in app_setting. Local OCR is opt-in:
// with Enabled false (the default) the read tool never falls back to the built-in
// model. Whether the OCR models are actually installed is a separate, runtime
// concern reported alongside this setting by the API, not stored here.
type OCRSettings struct {
	Enabled bool `json:"enabled"`
}

// LoadOCRSettings reads the singleton local-OCR config. A missing row is not an
// error — it means "never configured", which defaults to disabled.
func LoadOCRSettings(ctx context.Context, store SettingStore) (OCRSettings, error) {
	raw, err := store.GetSetting(ctx, OCRSettingKey)
	if err != nil {
		return OCRSettings{}, err
	}
	var s OCRSettings
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return OCRSettings{}, fmt.Errorf("parse ocr settings: %w", err)
		}
	}
	return s, nil
}

// SaveOCRSettings persists the singleton local-OCR config.
func SaveOCRSettings(ctx context.Context, store SettingStore, s OCRSettings) error {
	b, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal ocr settings: %w", err)
	}
	return store.SetSetting(ctx, OCRSettingKey, string(b))
}
