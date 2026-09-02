package store_test

import (
	"testing"

	"github.com/CherryHQ/stella/cmd/stellad/store"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/core/providercred"
)

// overlayAgent creates an enabled agent whose default model references provider
// ref (which may be a canonical id or a type alias).
func overlayAgent(t *testing.T, s *store.DBStore, id, modelRef string) {
	t.Helper()
	a := config.Agent{ID: id, Name: id, Workspace: "/tmp/" + id, Enabled: true, Model: modelRef}
	if err := s.CreateAgent(testCtx(), a); err != nil {
		t.Fatalf("CreateAgent %q: %v", id, err)
	}
}

func TestSnapshotOverlayCanonicalOverride(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()
	createTestProvider(t, s, "openai") // global key intentionally empty
	overlayAgent(t, s, "ag", "openai/gpt")

	svc := providercred.NewService(s, b64Cipher{})
	if _, err := svc.Set(ctx, "ag", providercred.Input{ProviderID: "openai", APIKey: "AGENTKEY"}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	loader := providercred.NewCredentialLoader(s, s, b64Cipher{})

	snap, err := loader.Snapshot(ctx, "ag")
	if err != nil {
		t.Fatalf("overlay Snapshot: %v", err)
	}
	if got := snap.Providers["openai"].APIKey; got != "AGENTKEY" {
		t.Errorf("Providers[openai].APIKey = %q, want AGENTKEY", got)
	}
	if snap.APIKey != "AGENTKEY" {
		t.Errorf("legacy default APIKey = %q, want AGENTKEY", snap.APIKey)
	}
	// Provider metadata is still global.
	if c := snap.Providers["openai"]; c.Type != "openai" || c.BaseURL != "https://openai.example.com" {
		t.Errorf("provider metadata changed: %+v", c)
	}

	// Deleting the override restores the global (empty) key without touching the
	// model or enabled state.
	if err := svc.Delete(ctx, "ag", "openai"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	snap2, err := loader.Snapshot(ctx, "ag")
	if err != nil {
		t.Fatalf("post-delete Snapshot: %v", err)
	}
	if snap2.Providers["openai"].APIKey != "" || snap2.APIKey != "" {
		t.Errorf("after delete want global empty key, got %q / %q", snap2.Providers["openai"].APIKey, snap2.APIKey)
	}
	if snap2.Model != "openai/gpt" {
		t.Errorf("model changed by credential delete: %q", snap2.Model)
	}
}

func TestSnapshotOverlayAppliesToTypeAlias(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()
	// One provider of type "openai" with a distinct canonical id, referenced by its
	// type alias in the model ref.
	if err := s.CreateProvider(ctx, config.Provider{
		ID: "prov-1", Type: "openai", Name: "prov-1", Enabled: true,
		BaseURL: "https://prov1.example.com",
	}); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	overlayAgent(t, s, "ag", "openai/gpt") // "openai" is the type alias for prov-1

	svc := providercred.NewService(s, b64Cipher{})
	// The override must be keyed by the canonical id, not the alias.
	if _, err := svc.Set(ctx, "ag", providercred.Input{ProviderID: "prov-1", APIKey: "AGENTKEY"}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	loader := providercred.NewCredentialLoader(s, s, b64Cipher{})
	snap, err := loader.Snapshot(ctx, "ag")
	if err != nil {
		t.Fatalf("overlay Snapshot: %v", err)
	}
	// The alias map entry ("openai") carries canonical id prov-1, so the override
	// lands on it and on the legacy default key.
	if got := snap.Providers["openai"].APIKey; got != "AGENTKEY" {
		t.Errorf("alias entry APIKey = %q, want AGENTKEY", got)
	}
	if snap.APIKey != "AGENTKEY" {
		t.Errorf("default APIKey = %q, want AGENTKEY", snap.APIKey)
	}
}
