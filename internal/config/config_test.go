package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAnnaHome(t *testing.T) {
	t.Setenv("ANNA_HOME", "")
	ResetAnnaHome()
	dir := AnnaHome()
	if !strings.HasSuffix(dir, ".anna") {
		t.Errorf("AnnaHome() = %q, want suffix .anna", dir)
	}
}

func TestAnnaHomeEnv(t *testing.T) {
	t.Setenv("ANNA_HOME", "/custom/anna")
	ResetAnnaHome()
	dir := AnnaHome()
	if dir != "/custom/anna" {
		t.Errorf("AnnaHome() = %q, want %q", dir, "/custom/anna")
	}
}

func TestCachePath(t *testing.T) {
	t.Setenv("ANNA_HOME", "/test/anna")
	ResetAnnaHome()
	p := CachePath()
	if p != "/test/anna/cache" {
		t.Errorf("CachePath() = %q, want %q", p, "/test/anna/cache")
	}
}

func TestDBPath(t *testing.T) {
	t.Setenv("ANNA_HOME", "/test/anna")
	ResetAnnaHome()
	p := DBPath()
	if p != "/test/anna/anna.db" {
		t.Errorf("DBPath() = %q, want %q", p, "/test/anna/anna.db")
	}
}

func TestSchedulerIsEnabled(t *testing.T) {
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
			c := SchedulerConfig{Enabled: tt.enabled}
			if got := c.IsEnabled(); got != tt.want {
				t.Errorf("IsEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHeartbeatConfig(t *testing.T) {
	c := HeartbeatConfig{}
	if c.IsEnabled() {
		t.Error("Heartbeat should be disabled by default")
	}
	if c.Interval() != "10m" {
		t.Errorf("Interval() = %q, want %q", c.Interval(), "10m")
	}
	if c.FilePath("/workspace") != filepath.Join("/workspace", "HEARTBEAT.md") {
		t.Errorf("FilePath() = %q", c.FilePath("/workspace"))
	}
}

func TestHeartbeatFilePathAbsolute(t *testing.T) {
	c := HeartbeatConfig{File: "/absolute/path/heartbeat.md"}
	got := c.FilePath("/workspace")
	if got != "/absolute/path/heartbeat.md" {
		t.Errorf("FilePath() = %q, want absolute path preserved", got)
	}
}

func TestSandboxConfigDefaults(t *testing.T) {
	c := SandboxConfig{}
	if got := c.BackendName(); got != SandboxBackendAuto {
		t.Fatalf("BackendName() = %q, want %q", got, SandboxBackendAuto)
	}
	if got := c.NetworkMode(); got != SandboxNetworkDisabled {
		t.Fatalf("NetworkMode() = %q, want %q", got, SandboxNetworkDisabled)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate(): %v", err)
	}
}

func TestSandboxConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     SandboxConfig
		wantErr bool
	}{
		{
			name: "allow_all valid",
			cfg:  SandboxConfig{Network: SandboxNetworkConfig{Mode: SandboxNetworkAllowAll}},
		},
		{
			name: "boxsh backend valid",
			cfg:  SandboxConfig{Backend: SandboxBackendBoxsh},
		},
		{
			name: "local backend valid",
			cfg:  SandboxConfig{Backend: SandboxBackendLocal},
		},
		{
			name: "whitelist valid host and cidr",
			cfg:  SandboxConfig{Network: SandboxNetworkConfig{Mode: SandboxNetworkWhitelist, Allowlist: []string{"example.com", "anthropic.com", "internal.net", "registry.example", "10.0.0.0/24"}}},
		},
		{
			name:    "invalid backend",
			cfg:     SandboxConfig{Backend: "gvisor"},
			wantErr: true,
		},
		{
			name:    "invalid mode",
			cfg:     SandboxConfig{Network: SandboxNetworkConfig{Mode: "bogus"}},
			wantErr: true,
		},
		{
			name:    "allowlist without whitelist mode",
			cfg:     SandboxConfig{Network: SandboxNetworkConfig{Mode: SandboxNetworkDisabled, Allowlist: []string{"example.com"}}},
			wantErr: true,
		},
		{
			name:    "whitelist requires entries",
			cfg:     SandboxConfig{Network: SandboxNetworkConfig{Mode: SandboxNetworkWhitelist}},
			wantErr: true,
		},
		{
			name:    "invalid whitelist entry",
			cfg:     SandboxConfig{Network: SandboxNetworkConfig{Mode: SandboxNetworkWhitelist, Allowlist: []string{"bad host"}}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr && err == nil {
				t.Fatal("expected validation error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

func TestSnapshotResolveModelID(t *testing.T) {
	tests := []struct {
		name   string
		snap   Snapshot
		tier   string
		wantID string
	}{
		{
			name:   "strong falls back to model",
			snap:   Snapshot{Model: "default-model"},
			tier:   "strong",
			wantID: "default-model",
		},
		{
			name:   "strong uses model_strong",
			snap:   Snapshot{Model: "default-model", ModelStrong: "strong-model"},
			tier:   "strong",
			wantID: "strong-model",
		},
		{
			name:   "fast falls back to model",
			snap:   Snapshot{Model: "default-model"},
			tier:   "fast",
			wantID: "default-model",
		},
		{
			name:   "fast uses model_fast",
			snap:   Snapshot{Model: "default-model", ModelFast: "fast-model"},
			tier:   "fast",
			wantID: "fast-model",
		},
		{
			name:   "unknown tier falls back like strong",
			snap:   Snapshot{Model: "default-model", ModelStrong: "strong-model"},
			tier:   "unknown",
			wantID: "strong-model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.snap.ResolveModelID(tt.tier)
			if got != tt.wantID {
				t.Errorf("ResolveModelID(%q) = %q, want %q", tt.tier, got, tt.wantID)
			}
		})
	}
}

func TestSnapshotResolveModel(t *testing.T) {
	snap := Snapshot{
		Provider: "anthropic",
		Model:    "anthropic/claude-sonnet-4-6",
		BaseURL:  "https://api.example.com",
		Providers: map[string]ProviderCreds{
			"anthropic": {BaseURL: "https://api.example.com"},
		},
	}
	model := snap.ResolveModel()
	if model.ID != "claude-sonnet-4-6" {
		t.Errorf("model.ID = %q, want %q", model.ID, "claude-sonnet-4-6")
	}
	if model.Provider != "anthropic" {
		t.Errorf("model.Provider = %q, want %q", model.Provider, "anthropic")
	}
	if model.BaseURL != "https://api.example.com" {
		t.Errorf("model.BaseURL = %q, want %q", model.BaseURL, "https://api.example.com")
	}
}

func TestParseModelRef(t *testing.T) {
	tests := []struct {
		ref      string
		wantProv string
		wantMod  string
	}{
		{"anthropic/claude-sonnet-4-6", "anthropic", "claude-sonnet-4-6"},
		{"openai/gpt-4", "openai", "gpt-4"},
		{"plain-model", "", "plain-model"},
		{"provider/nested/model", "provider", "nested/model"},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			prov, mod := ParseModelRef(tt.ref)
			if prov != tt.wantProv {
				t.Errorf("provider = %q, want %q", prov, tt.wantProv)
			}
			if mod != tt.wantMod {
				t.Errorf("model = %q, want %q", mod, tt.wantMod)
			}
		})
	}
}

func TestSnapshotPaths(t *testing.T) {
	snap := Snapshot{Workspace: "/home/user/.anna/workspaces/anna"}
	if snap.SkillsPath() != "/home/user/.anna/workspaces/anna/.agents/skills" {
		t.Errorf("SkillsPath() = %q", snap.SkillsPath())
	}
	if snap.LogPath() != "/home/user/.anna/workspaces/anna/anna.log" {
		t.Errorf("LogPath() = %q", snap.LogPath())
	}
}

func TestModelConfigToAI(t *testing.T) {
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

	model := ModelConfigToAI("anthropic", m)
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
}

func TestModelConfigToAIFallbacks(t *testing.T) {
	m := ModelConfig{ID: "test-model"}
	model := ModelConfigToAI("openai", m)
	if model.API != "openai" {
		t.Errorf("API = %q, want fallback to provider", model.API)
	}
	if model.Name != "test-model" {
		t.Errorf("Name = %q, want ID fallback", model.Name)
	}
}
