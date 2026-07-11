package config

import "testing"

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
	snap := Snapshot{Workspace: "/home/user/.stella/workspaces/stella"}
	if snap.SkillsPath() != "/home/user/.stella/workspaces/stella/.agents/skills" {
		t.Errorf("SkillsPath() = %q", snap.SkillsPath())
	}
	if snap.LogPath() != "/home/user/.stella/workspaces/stella/stella.log" {
		t.Errorf("LogPath() = %q", snap.LogPath())
	}
}
