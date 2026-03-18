package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// CachedModel is the on-disk representation of a model in models.json.
type CachedModel struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// ModelsCache is the top-level structure for models.json in the cache directory.
type ModelsCache struct {
	UpdatedAt time.Time     `json:"updated_at"`
	Models    []CachedModel `json:"models"`
}

// ModelsCachePath returns the path to the models.json cache file.
func ModelsCachePath() string {
	return filepath.Join(CachePath(), "models.json")
}

// LoadModelsCache reads the cached models from the workspace models.json.
func LoadModelsCache() (*ModelsCache, error) {
	data, err := os.ReadFile(ModelsCachePath())
	if err != nil {
		return nil, err
	}
	var cache ModelsCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, fmt.Errorf("parse models cache: %w", err)
	}
	return &cache, nil
}

// SaveModelsCache writes the models cache to the cache directory.
func SaveModelsCache(cache *ModelsCache) error {
	path := ModelsCachePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal models cache: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}
