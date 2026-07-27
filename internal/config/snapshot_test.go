package config

import (
	"testing"

	"github.com/CherryHQ/stella/pkg/ai"
)

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

func TestSnapshotResolveModelTierCarriesInput(t *testing.T) {
	snap := Snapshot{
		Provider:  "anthropic",
		Model:     "anthropic/claude-sonnet-4-6",
		ModelFast: "openai/gpt-text-only",
		Providers: map[string]ProviderCreds{
			"anthropic": {Type: "anthropic"},
			"openai":    {Type: "openai"},
		},
		ModelInputs: map[ModelKey][]string{
			{Provider: "anthropic", Model: "claude-sonnet-4-6"}: {"text", "image"},
			{Provider: "openai", Model: "gpt-text-only"}:        {"text"},
		},
	}

	strong := snap.ResolveModelTier(ModelTierStrong)
	if got := strong.ImageCapability(); got != ai.ImageSupported {
		t.Errorf("strong tier ImageCapability = %v, want ImageSupported (Input=%v)", got, strong.Input)
	}

	fast := snap.ResolveModelTier(ModelTierFast)
	if got := fast.ImageCapability(); got != ai.ImageUnsupported {
		t.Errorf("fast tier ImageCapability = %v, want ImageUnsupported (Input=%v)", got, fast.Input)
	}
}

func TestSnapshotResolveModelTierWithoutDeclaredInput(t *testing.T) {
	snap := Snapshot{
		Provider:  "anthropic",
		Model:     "anthropic/claude-sonnet-4-6",
		Providers: map[string]ProviderCreds{"anthropic": {Type: "anthropic"}},
	}

	model := snap.ResolveModel()
	if model.Input != nil {
		t.Errorf("Input = %v, want nil", model.Input)
	}
	if got := model.ImageCapability(); got != ai.ImageUnknown {
		t.Errorf("ImageCapability = %v, want ImageUnknown", got)
	}
}

func TestSnapshotResolveVisionModel(t *testing.T) {
	snap := Snapshot{
		Provider: "anthropic",
		Model:    "anthropic/claude-sonnet-4-6",
		Providers: map[string]ProviderCreds{
			"anthropic": {Type: "anthropic"},
			"openai":    {Type: "openai", BaseURL: "https://api.example.com"},
		},
		ModelInputs: map[ModelKey][]string{
			{Provider: "openai", Model: "gpt-4o"}: {"text", "image"},
		},
	}

	// Unset must not fall back to the default model: sending images back to the
	// model that cannot read them is the bug this tier exists to avoid.
	if _, ok := snap.ResolveVisionModel(); ok {
		t.Fatal("expected no vision model when model_vision is unset")
	}
	if got := snap.ResolveModelID(ModelTierVision); got != "" {
		t.Errorf("ResolveModelID(vision) = %q, want empty", got)
	}

	snap.ModelVision = "openai/gpt-4o"
	model, ok := snap.ResolveVisionModel()
	if !ok {
		t.Fatal("expected a vision model once model_vision is set")
	}
	if model.ID != "gpt-4o" || model.Provider != "openai" || model.API != "openai" {
		t.Errorf("model = %+v, want the openai gpt-4o entry", model)
	}
	if model.BaseURL != "https://api.example.com" {
		t.Errorf("BaseURL = %q, want the provider's", model.BaseURL)
	}
	if got := model.ImageCapability(); got != ai.ImageSupported {
		t.Errorf("ImageCapability = %v, want ImageSupported", got)
	}
}

func TestSnapshotModelInputHandlesNestedModelID(t *testing.T) {
	snap := Snapshot{
		Provider:  "openrouter",
		Model:     "openrouter/anthropic/claude-sonnet-4-6",
		Providers: map[string]ProviderCreds{"openrouter": {Type: "openai"}},
		ModelInputs: map[ModelKey][]string{
			{Provider: "openrouter", Model: "anthropic/claude-sonnet-4-6"}: {"text", "image"},
		},
	}

	if got := snap.ResolveModel().ImageCapability(); got != ai.ImageSupported {
		t.Errorf("ImageCapability = %v, want ImageSupported", got)
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
