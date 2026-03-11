package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigDefaults(t *testing.T) {
	t.Setenv("ANNA_RUNNER_TYPE", "")
	dir := t.TempDir()
	cfg, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	if cfg.Runner.Type != "go" {
		t.Errorf("Runner.Type = %q, want %q", cfg.Runner.Type, "go")
	}
	if cfg.Runner.IdleTimeout != 10 {
		t.Errorf("Runner.IdleTimeout = %d, want 10", cfg.Runner.IdleTimeout)
	}
	if cfg.SessionsPath() != filepath.Join(dir, "workspace", "sessions") {
		t.Errorf("SessionsPath() = %q, want %q", cfg.SessionsPath(), filepath.Join(dir, "workspace", "sessions"))
	}
	if cfg.Channels.Telegram.Token != "" {
		t.Errorf("Channels.Telegram.Token = %q, want empty", cfg.Channels.Telegram.Token)
	}
	if cfg.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", cfg.Provider, "anthropic")
	}
	if cfg.Model != "claude-sonnet-4-6" {
		t.Errorf("Model = %q, want %q", cfg.Model, "claude-sonnet-4-6")
	}
	if cfg.Workspace != filepath.Join(dir, "workspace") {
		t.Errorf("Workspace = %q, want %q", cfg.Workspace, filepath.Join(dir, "workspace"))
	}
	if cfg.Heartbeat.IsEnabled() {
		t.Error("Heartbeat should be disabled by default")
	}
	if cfg.Heartbeat.Interval() != "10m" {
		t.Errorf("Heartbeat.Interval() = %q, want %q", cfg.Heartbeat.Interval(), "10m")
	}
	if cfg.Heartbeat.FilePath(cfg.Workspace) != filepath.Join(dir, "workspace", "HEARTBEAT.md") {
		t.Errorf("Heartbeat.FilePath() = %q, want %q", cfg.Heartbeat.FilePath(cfg.Workspace), filepath.Join(dir, "workspace", "HEARTBEAT.md"))
	}
}

func TestLoadConfigFromFile(t *testing.T) {
	dir := t.TempDir()
	yaml := `
runner:
  type: go
  idle_timeout: 5
heartbeat:
  enabled: true
  every: 15m
  file: notes/HEARTBEAT.md
channels:
  telegram:
    token: "test-token-123"
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	if cfg.Runner.IdleTimeout != 5 {
		t.Errorf("Runner.IdleTimeout = %d, want 5", cfg.Runner.IdleTimeout)
	}
	if cfg.Channels.Telegram.Token != "test-token-123" {
		t.Errorf("Channels.Telegram.Token = %q, want %q", cfg.Channels.Telegram.Token, "test-token-123")
	}
	if !cfg.Heartbeat.IsEnabled() {
		t.Error("Heartbeat should be enabled from file config")
	}
	if cfg.Heartbeat.Interval() != "15m" {
		t.Errorf("Heartbeat.Interval() = %q, want %q", cfg.Heartbeat.Interval(), "15m")
	}
	if cfg.Heartbeat.FilePath(cfg.Workspace) != filepath.Join(cfg.Workspace, "notes/HEARTBEAT.md") {
		t.Errorf("Heartbeat.FilePath() = %q, want %q", cfg.Heartbeat.FilePath(cfg.Workspace), filepath.Join(cfg.Workspace, "notes/HEARTBEAT.md"))
	}
}

func TestLoadConfigEnvOverrides(t *testing.T) {
	dir := t.TempDir()

	t.Setenv("ANNA_TELEGRAM_TOKEN", "env-token")
	t.Setenv("ANNA_HEARTBEAT_ENABLED", "true")
	t.Setenv("ANNA_HEARTBEAT_EVERY", "30m")

	cfg, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	if cfg.Channels.Telegram.Token != "env-token" {
		t.Errorf("Channels.Telegram.Token = %q, want %q", cfg.Channels.Telegram.Token, "env-token")
	}
	if !cfg.Heartbeat.IsEnabled() {
		t.Error("Heartbeat should be enabled from env")
	}
	if cfg.Heartbeat.Interval() != "30m" {
		t.Errorf("Heartbeat.Interval() = %q, want %q", cfg.Heartbeat.Interval(), "30m")
	}
}

func TestLoadConfigEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	yaml := `
channels:
  telegram:
    token: "file-token"
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ANNA_TELEGRAM_TOKEN", "env-token")

	cfg, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	// Env var overrides file value.
	if cfg.Channels.Telegram.Token != "env-token" {
		t.Errorf("Channels.Telegram.Token = %q, want %q", cfg.Channels.Telegram.Token, "env-token")
	}
}

func TestLoadConfigInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(":::invalid"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFrom(dir)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestLoadConfigCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "dir")

	_, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("expected directory")
	}
}

func TestAnnaHome(t *testing.T) {
	t.Setenv("ANNA_HOME", "")
	dir := AnnaHome()
	if !strings.HasSuffix(dir, ".anna") {
		t.Errorf("AnnaHome() = %q, want suffix .anna", dir)
	}
}

func TestAnnaHomeEnv(t *testing.T) {
	t.Setenv("ANNA_HOME", "/custom/anna")
	dir := AnnaHome()
	if dir != "/custom/anna" {
		t.Errorf("AnnaHome() = %q, want %q", dir, "/custom/anna")
	}
}

func TestPath(t *testing.T) {
	t.Setenv("ANNA_HOME", "")
	p := Path()
	if !strings.HasSuffix(p, filepath.Join(".anna", "config.yaml")) {
		t.Errorf("Path() = %q, want suffix .anna/config.yaml", p)
	}
}

func TestLoad(t *testing.T) {
	t.Setenv("ANNA_HOME", t.TempDir())
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Runner.Type == "" {
		t.Error("Runner.Type should have a default")
	}
}

func TestProviderEnvAnthropicAPIKey(t *testing.T) {
	dir := t.TempDir()

	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-123")
	t.Setenv("ANTHROPIC_BASE_URL", "https://custom-proxy.example.com")
	t.Setenv("ANNA_RUNNER_TYPE", "go")

	cfg, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	p := cfg.Providers["anthropic"]
	if p.APIKey != "sk-ant-test-123" {
		t.Errorf("Providers[anthropic].APIKey = %q, want %q", p.APIKey, "sk-ant-test-123")
	}
	if p.BaseURL != "https://custom-proxy.example.com" {
		t.Errorf("Providers[anthropic].BaseURL = %q, want %q", p.BaseURL, "https://custom-proxy.example.com")
	}
}

func TestProviderEnvOpenAI(t *testing.T) {
	dir := t.TempDir()
	yamlContent := `
provider: openai
model: gpt-4o
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yamlContent), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("OPENAI_API_KEY", "sk-openai-test")
	t.Setenv("OPENAI_BASE_URL", "https://openai-proxy.example.com")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_BASE_URL", "")

	cfg, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	if cfg.Provider != "openai" {
		t.Errorf("Provider = %q, want %q", cfg.Provider, "openai")
	}
	p := cfg.Providers["openai"]
	if p.APIKey != "sk-openai-test" {
		t.Errorf("Providers[openai].APIKey = %q, want %q", p.APIKey, "sk-openai-test")
	}
	if p.BaseURL != "https://openai-proxy.example.com" {
		t.Errorf("Providers[openai].BaseURL = %q, want %q", p.BaseURL, "https://openai-proxy.example.com")
	}
}

func TestProviderDefaultValues(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ANNA_RUNNER_TYPE", "")
	t.Setenv("ANNA_PROVIDER", "")
	t.Setenv("ANNA_MODEL", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_BASE_URL", "")

	cfg, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	if cfg.Provider != "anthropic" {
		t.Errorf("Provider = %q, want default %q", cfg.Provider, "anthropic")
	}
	if cfg.Model != "claude-sonnet-4-6" {
		t.Errorf("Model = %q, want default %q", cfg.Model, "claude-sonnet-4-6")
	}
}

func TestRunnerTypeEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ANNA_RUNNER_TYPE", "go")

	cfg, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	if cfg.Runner.Type != "go" {
		t.Errorf("Runner.Type = %q, want %q", cfg.Runner.Type, "go")
	}
}

func TestProviderModelEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ANNA_PROVIDER", "openai")
	t.Setenv("ANNA_MODEL", "gpt-4o")

	cfg, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	if cfg.Provider != "openai" {
		t.Errorf("Provider = %q, want %q", cfg.Provider, "openai")
	}
	if cfg.Model != "gpt-4o" {
		t.Errorf("Model = %q, want %q", cfg.Model, "gpt-4o")
	}
}

func TestProvidersFromYAML(t *testing.T) {
	dir := t.TempDir()
	yamlContent := `
provider: anthropic
model: claude-sonnet-4-6
providers:
  anthropic:
    api_key: "yaml-key"
    base_url: "https://yaml-proxy.example.com"
    models:
      - id: claude-sonnet-4-6
        name: Claude Sonnet 4
        api: anthropic-messages
        reasoning: false
        context_window: 200000
        max_tokens: 8192
  openai:
    api_key: "openai-yaml-key"
    models:
      - id: gpt-4o
        name: GPT-4o
        api: openai-completions
        context_window: 128000
        max_tokens: 4096
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yamlContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Clear env to ensure YAML values are used.
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	t.Setenv("OPENAI_API_KEY", "")

	cfg, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	ant := cfg.Providers["anthropic"]
	if ant.APIKey != "yaml-key" {
		t.Errorf("Providers[anthropic].APIKey = %q, want %q", ant.APIKey, "yaml-key")
	}
	if ant.BaseURL != "https://yaml-proxy.example.com" {
		t.Errorf("Providers[anthropic].BaseURL = %q, want %q", ant.BaseURL, "https://yaml-proxy.example.com")
	}
	if len(ant.Models) != 1 {
		t.Fatalf("Providers[anthropic].Models len = %d, want 1", len(ant.Models))
	}
	if ant.Models[0].ID != "claude-sonnet-4-6" {
		t.Errorf("model ID = %q, want %q", ant.Models[0].ID, "claude-sonnet-4-6")
	}
	if ant.Models[0].ContextWindow != 200000 {
		t.Errorf("model ContextWindow = %d, want 200000", ant.Models[0].ContextWindow)
	}

	oai := cfg.Providers["openai"]
	if oai.APIKey != "openai-yaml-key" {
		t.Errorf("Providers[openai].APIKey = %q, want %q", oai.APIKey, "openai-yaml-key")
	}
	if len(oai.Models) != 1 {
		t.Fatalf("Providers[openai].Models len = %d, want 1", len(oai.Models))
	}
	if oai.Models[0].ID != "gpt-4o" {
		t.Errorf("model ID = %q, want %q", oai.Models[0].ID, "gpt-4o")
	}
}

func TestResolveModelFromConfig(t *testing.T) {
	cfg := &Config{
		Provider: "anthropic",
		Model:    "claude-sonnet-4-6",
		Providers: map[string]ProviderConfig{
			"anthropic": {
				APIKey: "key",
				Models: []ModelConfig{
					{
						ID:            "claude-sonnet-4-6",
						Name:          "Claude Sonnet 4",
						API:           "anthropic-messages",
						ContextWindow: 200000,
						MaxTokens:     8192,
					},
				},
			},
		},
	}

	model := cfg.ResolveModel()
	if model.ID != "claude-sonnet-4-6" {
		t.Errorf("model.ID = %q, want %q", model.ID, "claude-sonnet-4-6")
	}
	if model.API != "anthropic-messages" {
		t.Errorf("model.API = %q, want %q", model.API, "anthropic-messages")
	}
	if model.ContextWindow != 200000 {
		t.Errorf("model.ContextWindow = %d, want 200000", model.ContextWindow)
	}
}

func TestResolveModelFallback(t *testing.T) {
	cfg := &Config{
		Provider:  "anthropic",
		Model:     "claude-sonnet-4-6",
		Providers: map[string]ProviderConfig{"anthropic": {APIKey: "key"}},
	}

	model := cfg.ResolveModel()
	if model.ID != "claude-sonnet-4-6" {
		t.Errorf("model.ID = %q, want %q", model.ID, "claude-sonnet-4-6")
	}
	if model.API != "anthropic" {
		t.Errorf("model.API = %q, want %q (fallback to provider name)", model.API, "anthropic")
	}
}

func TestResolveModelTierFallbackChain(t *testing.T) {
	tests := []struct {
		name   string
		cfg    Config
		tier   string
		wantID string
	}{
		{
			name:   "strong falls back to model",
			cfg:    Config{Provider: "anthropic", Model: "default-model", Providers: map[string]ProviderConfig{"anthropic": {}}},
			tier:   "strong",
			wantID: "default-model",
		},
		{
			name:   "strong uses model_strong",
			cfg:    Config{Provider: "anthropic", Model: "default-model", ModelStrong: "strong-model", Providers: map[string]ProviderConfig{"anthropic": {}}},
			tier:   "strong",
			wantID: "strong-model",
		},
		{
			name:   "fast falls back to model",
			cfg:    Config{Provider: "anthropic", Model: "default-model", Providers: map[string]ProviderConfig{"anthropic": {}}},
			tier:   "fast",
			wantID: "default-model",
		},
		{
			name:   "fast uses model_fast",
			cfg:    Config{Provider: "anthropic", Model: "default-model", ModelStrong: "s", ModelFast: "fast-model", Providers: map[string]ProviderConfig{"anthropic": {}}},
			tier:   "fast",
			wantID: "fast-model",
		},
		{
			name:   "unknown tier falls back like strong",
			cfg:    Config{Provider: "anthropic", Model: "default-model", ModelStrong: "strong-model", Providers: map[string]ProviderConfig{"anthropic": {}}},
			tier:   "unknown",
			wantID: "strong-model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := tt.cfg.ResolveModelTier(tt.tier)
			if model.ID != tt.wantID {
				t.Errorf("ResolveModelTier(%q) = %q, want %q", tt.tier, model.ID, tt.wantID)
			}
		})
	}
}

func TestModelTiersFromYAML(t *testing.T) {
	dir := t.TempDir()
	yamlContent := `
provider: anthropic
model: claude-sonnet-4-6
model_strong: claude-opus-4-6
model_fast: claude-haiku-4-5
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yamlContent), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	if cfg.ModelStrong != "claude-opus-4-6" {
		t.Errorf("ModelStrong = %q, want %q", cfg.ModelStrong, "claude-opus-4-6")
	}
	if cfg.ModelFast != "claude-haiku-4-5" {
		t.Errorf("ModelFast = %q, want %q", cfg.ModelFast, "claude-haiku-4-5")
	}
}

func TestModelTierEnvOverrides(t *testing.T) {
	dir := t.TempDir()

	t.Setenv("ANNA_MODEL_STRONG", "env-strong")
	t.Setenv("ANNA_MODEL_FAST", "env-fast")

	cfg, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	if cfg.ModelStrong != "env-strong" {
		t.Errorf("ModelStrong = %q, want %q", cfg.ModelStrong, "env-strong")
	}
	if cfg.ModelFast != "env-fast" {
		t.Errorf("ModelFast = %q, want %q", cfg.ModelFast, "env-fast")
	}

	// Verify tier resolution uses env values.
	model := cfg.ResolveModelTier("fast")
	if model.ID != "env-fast" {
		t.Errorf("ResolveModelTier(fast) = %q, want %q", model.ID, "env-fast")
	}
}

func TestModelTierEnvOverridesYAML(t *testing.T) {
	dir := t.TempDir()
	yamlContent := `
model_strong: yaml-strong
model_fast: yaml-fast
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yamlContent), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ANNA_MODEL_STRONG", "env-strong")

	cfg, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	// Env should override YAML.
	if cfg.ModelStrong != "env-strong" {
		t.Errorf("ModelStrong = %q, want %q", cfg.ModelStrong, "env-strong")
	}
	// YAML value should remain for non-overridden tiers.
	if cfg.ModelFast != "yaml-fast" {
		t.Errorf("ModelFast = %q, want %q", cfg.ModelFast, "yaml-fast")
	}
}

func TestProviderEnvDoesNotOverrideYAML(t *testing.T) {
	dir := t.TempDir()
	yamlContent := `
providers:
  anthropic:
    api_key: "yaml-key"
    base_url: "https://yaml.example.com"
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yamlContent), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	t.Setenv("ANTHROPIC_BASE_URL", "https://env.example.com")

	cfg, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	p := cfg.Providers["anthropic"]
	// YAML values should NOT be overridden by env.
	if p.APIKey != "yaml-key" {
		t.Errorf("Providers[anthropic].APIKey = %q, want %q (YAML should win)", p.APIKey, "yaml-key")
	}
	if p.BaseURL != "https://yaml.example.com" {
		t.Errorf("Providers[anthropic].BaseURL = %q, want %q (YAML should win)", p.BaseURL, "https://yaml.example.com")
	}
}

func TestStateOverridesConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ANNA_HOME", dir)

	configYAML := `
provider: anthropic
model: claude-sonnet-4-6
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	// state.yaml lives under workspace (dir/workspace/state.yaml).
	wsDir := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stateYAML := `
provider: openai
model: gpt-4o
`
	if err := os.WriteFile(filepath.Join(wsDir, "state.yaml"), []byte(stateYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	if cfg.Provider != "openai" {
		t.Errorf("Provider = %q, want %q (state should override config)", cfg.Provider, "openai")
	}
	if cfg.Model != "gpt-4o" {
		t.Errorf("Model = %q, want %q (state should override config)", cfg.Model, "gpt-4o")
	}
}

func TestEnvOverridesState(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ANNA_HOME", dir)
	t.Setenv("ANNA_PROVIDER", "anthropic")
	t.Setenv("ANNA_MODEL", "claude-opus-4-6")

	// state.yaml lives under workspace (dir/workspace/state.yaml).
	wsDir := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stateYAML := `
provider: openai
model: gpt-4o
`
	if err := os.WriteFile(filepath.Join(wsDir, "state.yaml"), []byte(stateYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFrom(dir)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	if cfg.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q (env should override state)", cfg.Provider, "anthropic")
	}
	if cfg.Model != "claude-opus-4-6" {
		t.Errorf("Model = %q, want %q (env should override state)", cfg.Model, "claude-opus-4-6")
	}
}

func TestSaveModelSelectionWritesState(t *testing.T) {
	wsDir := t.TempDir()

	if err := SaveModelSelection(wsDir, "openai", "gpt-4o"); err != nil {
		t.Fatalf("SaveModelSelection: %v", err)
	}

	// state.yaml should exist in workspace.
	data, err := os.ReadFile(filepath.Join(wsDir, "state.yaml"))
	if err != nil {
		t.Fatalf("read state.yaml: %v", err)
	}
	if !strings.Contains(string(data), "openai") {
		t.Errorf("state.yaml should contain provider, got: %s", data)
	}
}

func TestWorkspacePaths(t *testing.T) {
	cfg := &Config{
		Workspace: "/home/user/.anna/workspace",
	}

	if cfg.SessionsPath() != "/home/user/.anna/workspace/sessions" {
		t.Errorf("SessionsPath() = %q", cfg.SessionsPath())
	}
	if cfg.MemoryPath() != "/home/user/.anna/workspace/memory" {
		t.Errorf("MemoryPath() = %q", cfg.MemoryPath())
	}
	if cfg.SkillsPath() != "/home/user/.anna/workspace/skills" {
		t.Errorf("SkillsPath() = %q", cfg.SkillsPath())
	}
	wantModels := filepath.Join(CachePath(), "models.json")
	if cfg.ModelsPath() != wantModels {
		t.Errorf("ModelsPath() = %q, want %q", cfg.ModelsPath(), wantModels)
	}
	if cfg.LogPath() != "/home/user/.anna/workspace/anna.log" {
		t.Errorf("LogPath() = %q", cfg.LogPath())
	}
}

func TestCronEnabled(t *testing.T) {
	tr := true
	fa := false
	tests := []struct {
		name    string
		enabled *bool
		want    bool
	}{
		{"nil defaults to true", nil, true},
		{"explicit true", &tr, true},
		{"explicit false", &fa, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := CronConfig{Enabled: tt.enabled}
			if got := c.CronEnabled(); got != tt.want {
				t.Errorf("CronEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestChannelConfigHelpers(t *testing.T) {
	tr := true
	fa := false

	// QQ
	if !(QQConfig{}).IsEnabled() {
		t.Error("QQ nil enabled should default to true")
	}
	if !(QQConfig{Enabled: &tr}).IsEnabled() {
		t.Error("QQ explicit true should be enabled")
	}
	if (QQConfig{Enabled: &fa}).IsEnabled() {
		t.Error("QQ explicit false should be disabled")
	}
	if (QQConfig{}).IsNotifyEnabled() {
		t.Error("QQ nil notify should be false")
	}
	if !(QQConfig{EnableNotify: &tr}).IsNotifyEnabled() {
		t.Error("QQ explicit true notify should be enabled")
	}

	// Feishu
	if !(FeishuConfig{}).IsEnabled() {
		t.Error("Feishu nil enabled should default to true")
	}
	if (FeishuConfig{Enabled: &fa}).IsEnabled() {
		t.Error("Feishu explicit false should be disabled")
	}
	if (FeishuConfig{}).IsNotifyEnabled() {
		t.Error("Feishu nil notify should be false")
	}
	if !(FeishuConfig{EnableNotify: &tr}).IsNotifyEnabled() {
		t.Error("Feishu explicit true notify should be enabled")
	}

	// Telegram
	if !(TelegramConfig{}).IsEnabled() {
		t.Error("Telegram nil enabled should default to true")
	}
	if (TelegramConfig{Enabled: &fa}).IsEnabled() {
		t.Error("Telegram explicit false should be disabled")
	}
	if (TelegramConfig{}).IsNotifyEnabled() {
		t.Error("Telegram nil notify should be false")
	}
	if !(TelegramConfig{EnableNotify: &tr}).IsNotifyEnabled() {
		t.Error("Telegram explicit true notify should be enabled")
	}
}

func TestModelConfigToType(t *testing.T) {
	cost := &ModelCostConfig{Input: 3.0, Output: 15.0, CacheRead: 0.3, CacheWrite: 3.75}
	m := ModelConfig{
		ID:            "claude-sonnet-4-6",
		Name:          "Claude Sonnet",
		API:           "anthropic-messages",
		Reasoning:     true,
		Input:         []string{"text", "image"},
		ContextWindow: 200000,
		MaxTokens:     8192,
		Headers:       map[string]string{"x-custom": "val"},
		Cost:          cost,
	}

	model := modelConfigToType("anthropic", m)
	if model.ID != "claude-sonnet-4-6" {
		t.Errorf("ID = %q", model.ID)
	}
	if model.Provider != "anthropic" {
		t.Errorf("Provider = %q", model.Provider)
	}
	if model.API != "anthropic-messages" {
		t.Errorf("API = %q", model.API)
	}
	if !model.Reasoning {
		t.Error("Reasoning should be true")
	}
	if model.Cost.Input != 3.0 {
		t.Errorf("Cost.Input = %f", model.Cost.Input)
	}
	if model.Cost.Output != 15.0 {
		t.Errorf("Cost.Output = %f", model.Cost.Output)
	}
}

func TestModelConfigToTypeFallbacks(t *testing.T) {
	// Empty API should fall back to provider
	m := ModelConfig{ID: "test-model"}
	model := modelConfigToType("openai", m)
	if model.API != "openai" {
		t.Errorf("API = %q, want fallback to provider", model.API)
	}
	// Empty Name should use ID
	if model.Name != "test-model" {
		t.Errorf("Name = %q, want ID fallback", model.Name)
	}
}

func TestHeartbeatFilePathAbsolute(t *testing.T) {
	c := HeartbeatConfig{File: "/absolute/path/heartbeat.md"}
	got := c.FilePath("/workspace")
	if got != "/absolute/path/heartbeat.md" {
		t.Errorf("FilePath() = %q, want absolute path preserved", got)
	}
}
