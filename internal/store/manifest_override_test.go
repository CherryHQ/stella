package store_test

import (
	"testing"

	"github.com/CherryHQ/stella/internal/config"
)

func TestManifestPluginOverrideRoundtrip(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()

	// Empty initial state.
	overrides, err := s.ListManifestPluginOverrides(ctx)
	if err != nil {
		t.Fatalf("List initial: %v", err)
	}
	if len(overrides) != 0 {
		t.Fatalf("expected 0 overrides initially, got %d", len(overrides))
	}

	// Upsert an override that disables a plugin.
	enabled := false
	if err := s.UpsertManifestPluginOverride(ctx, config.ManifestPluginOverride{
		PluginID:           "tool/example",
		Enabled:            &enabled,
		SessionEnvVaultKey: "manifest/tool/example/session_env",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, ok, err := s.GetManifestPluginOverride(ctx, "tool/example")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected override row to exist after Upsert")
	}
	if got.Enabled == nil || *got.Enabled {
		t.Fatalf("Enabled = %v, want non-nil false", got.Enabled)
	}
	if got.SessionEnvVaultKey == "" {
		t.Fatal("SessionEnvVaultKey lost in roundtrip")
	}

	// Upsert again, this time with Enabled=nil (= fallback to default).
	if err := s.UpsertManifestPluginOverride(ctx, config.ManifestPluginOverride{
		PluginID: "tool/example",
		Enabled:  nil,
	}); err != nil {
		t.Fatalf("Upsert (nil enabled): %v", err)
	}
	got, _, err = s.GetManifestPluginOverride(ctx, "tool/example")
	if err != nil {
		t.Fatalf("Get after nil: %v", err)
	}
	if got.Enabled != nil {
		t.Fatalf("Enabled = %v, want nil after override cleared", got.Enabled)
	}

	// Delete.
	if err := s.DeleteManifestPluginOverride(ctx, "tool/example"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, ok, err = s.GetManifestPluginOverride(ctx, "tool/example")
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if ok {
		t.Fatal("expected override gone after Delete")
	}
}
