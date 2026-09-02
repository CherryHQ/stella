package store_test

import (
	"testing"

	"github.com/CherryHQ/stella/internal/platform/config"
)

// TestSnapshotAliasAmbiguityStopsTierCredentialLeak covers the failure mode the
// unique-type alias creates: a ref like "openai/gpt" resolves only while exactly
// one openai provider exists, and adding a second one silently unresolves every
// tier that used it. The tiers must then get nothing rather than the default
// model's key, which would bill a different account's credentials for the call.
func TestSnapshotAliasAmbiguityStopsTierCredentialLeak(t *testing.T) {
	s := setupDBStore(t)
	ctx := testCtx()

	mkProvider := func(id, typ, key string) {
		t.Helper()
		if err := s.CreateProvider(ctx, config.Provider{
			ID: id, Type: typ, Name: id, Enabled: true,
			APIKey: key, BaseURL: "https://" + id + ".example.com",
		}); err != nil {
			t.Fatalf("CreateProvider %q: %v", id, err)
		}
	}
	mkProvider("anthropic-row", "anthropic", "ANTHROPIC_KEY")
	mkProvider("openai-row", "openai", "OPENAI_KEY")

	// Vision has no per-agent form: it only ever comes from the deployment default.
	if err := config.SaveDefaultModels(ctx, s, config.DefaultModels{ModelVision: "openai/gpt-vision"}); err != nil {
		t.Fatalf("SaveDefaultModels: %v", err)
	}
	if err := s.CreateAgent(ctx, config.Agent{
		ID: "ag", Name: "ag", Workspace: "/tmp/ag", Enabled: true,
		Model:       "anthropic-row/claude",
		ModelStrong: "openai/gpt-strong", // type alias, unique for now
		ModelFast:   "openai/gpt-fast",
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	snap, err := s.Snapshot(ctx, "ag")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, tier := range []string{config.ModelTierStrong, config.ModelTierFast, config.ModelTierVision} {
		provID, _ := config.ParseModelRef(snap.ResolveModelID(tier))
		if got := snap.ResolveProviderCreds(provID).APIKey; got != "OPENAI_KEY" {
			t.Errorf("with a unique alias, %s tier APIKey = %q, want OPENAI_KEY", tier, got)
		}
	}

	// A second provider of the same type makes "openai" ambiguous.
	mkProvider("openai-row-2", "openai", "OTHER_KEY")

	snap, err = s.Snapshot(ctx, "ag")
	if err != nil {
		t.Fatalf("Snapshot after ambiguity: %v", err)
	}
	if got := snap.ResolveProviderCreds("").APIKey; got != "ANTHROPIC_KEY" {
		t.Fatalf("default tier lost its own credentials: %q", got)
	}
	for _, tier := range []string{config.ModelTierStrong, config.ModelTierFast, config.ModelTierVision} {
		provID, _ := config.ParseModelRef(snap.ResolveModelID(tier))
		if got := snap.ResolveProviderCreds(provID); got != (config.ProviderCreds{}) {
			t.Errorf("ambiguous alias on %s tier resolved to %+v, want zero credentials", tier, got)
		}
	}
}
