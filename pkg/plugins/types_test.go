package plugins

import (
	"reflect"
	"testing"
	"time"
)

func TestPluginStateCloneCopiesConfig(t *testing.T) {
	state := PluginState{
		ID:      "mcp",
		Enabled: true,
		Config: map[string]any{
			"token": "secret",
		},
	}

	clone := state.Clone()
	clone.Config["token"] = "changed"

	if got := state.Config["token"]; got != "secret" {
		t.Fatalf("expected original config to stay isolated, got %v", got)
	}
}

func TestRuntimeSnapshotCloneCopiesMetadata(t *testing.T) {
	now := time.Now().UTC()
	snapshot := RuntimeSnapshot{
		State:     RuntimeStateRunning,
		Message:   "ready",
		UpdatedAt: now,
		Metadata: map[string]any{
			"workers": 2,
		},
	}

	clone := snapshot.Clone()
	clone.Metadata["workers"] = 5

	if got := snapshot.Metadata["workers"]; got != 2 {
		t.Fatalf("expected original metadata to stay isolated, got %v", got)
	}
	if !clone.UpdatedAt.Equal(now) {
		t.Fatalf("expected timestamp to survive clone, got %v", clone.UpdatedAt)
	}
}

func TestPromptToolInfoCloneCopiesMetadata(t *testing.T) {
	info := PromptToolInfo{
		Name: "mcp.exec",
		Metadata: map[string]any{
			"server": "docs",
		},
	}

	clone := info.Clone()
	clone.Metadata["server"] = "code"

	if got := info.Metadata["server"]; got != "docs" {
		t.Fatalf("expected original metadata to stay isolated, got %v", got)
	}
}

func TestConfigRegistrationHelpers(t *testing.T) {
	reg := ConfigRegistration{
		PluginID: "mcp",
		DefaultConfig: func() map[string]any {
			return map[string]any{"token": "secret"}
		},
		Redact: func(raw map[string]any) map[string]any {
			raw["token"] = "***"
			return raw
		},
	}

	defaults := reg.Defaults()
	defaults["token"] = "changed"
	if got := reg.Defaults()["token"]; got != "secret" {
		t.Fatalf("expected default config to be defensively copied, got %v", got)
	}

	raw := map[string]any{"token": "secret", "url": "https://example.com"}
	redacted := reg.Redacted(raw)
	redacted["url"] = "changed"

	if got := raw["token"]; got != "secret" {
		t.Fatalf("expected raw config to remain unchanged, got %v", got)
	}
	if got := raw["url"]; got != "https://example.com" {
		t.Fatalf("expected raw config to remain unchanged, got %v", got)
	}
	if !reflect.DeepEqual(redacted, map[string]any{"token": "***", "url": "changed"}) {
		t.Fatalf("unexpected redacted config: %#v", redacted)
	}
}

func TestConfigRegistrationHelpersWithoutCallbacks(t *testing.T) {
	reg := ConfigRegistration{}
	if got := reg.Defaults(); len(got) != 0 {
		t.Fatalf("expected empty defaults, got %#v", got)
	}

	raw := map[string]any{"token": "secret"}
	redacted := reg.Redacted(raw)
	redacted["token"] = "changed"

	if got := raw["token"]; got != "secret" {
		t.Fatalf("expected cloned raw config, got %v", got)
	}
}
