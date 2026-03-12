package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// SaveModelSelection persists the provider and model to state.yaml
// in the given workspace, keeping config.yaml as a static, user-edited file.
func SaveModelSelection(workspace, provider, model string) error {
	path := filepath.Join(workspace, "state.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	raw := make(map[string]any)
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read state: %w", err)
	}
	if err == nil {
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("parse state: %w", err)
		}
	}

	raw["provider"] = provider
	raw["model"] = model

	out, err := yaml.Marshal(raw)
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return nil
}

// applyState loads state.yaml from the workspace and overrides provider/model in cfg.
func applyState(cfg *Config) {
	path := cfg.StatePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("failed to read state file", "path", path, "error", err)
		}
		return
	}
	var state struct {
		Provider string `yaml:"provider"`
		Model    string `yaml:"model"`
	}
	if err := yaml.Unmarshal(data, &state); err != nil {
		return
	}
	if state.Provider != "" {
		cfg.Provider = state.Provider
	}
	if state.Model != "" {
		cfg.Model = state.Model
	}
}
