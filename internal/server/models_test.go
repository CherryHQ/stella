package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	cfgstore "github.com/CherryHQ/stella/cmd/stellad/store"
	"github.com/CherryHQ/stella/internal/controlplane"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/platform/config"
)

type testEnv struct {
	store config.Store
}

func setupAdminStore(t *testing.T) testEnv {
	t.Helper()

	db := dbtest.New(t)
	store := cfgstore.NewDBStore(db)
	return testEnv{store: store}
}

func TestListCachedModelsMergesCustomAndFetchedAndFiltersDisabled(t *testing.T) {
	env := setupAdminStore(t)
	ctx := context.Background()

	if err := env.store.CreateProvider(ctx, config.Provider{
		ID:      "openai",
		Name:    "OpenAI",
		APIKey:  "sk-test",
		Enabled: true,
		Models: map[string]config.ProviderModelOverride{
			"qwen3.6-plus":     {Name: config.ValuePtr("Qwen 3.6 Plus"), Enabled: config.ValuePtr(true)},
			"custom-only":      {Name: config.ValuePtr("Custom Only"), Enabled: config.ValuePtr(true)},
			"disabled-fetched": {Name: config.ValuePtr("disabled-fetched"), Enabled: config.ValuePtr(false)},
			"disabled-custom":  {Name: config.ValuePtr("disabled-custom"), Enabled: config.ValuePtr(false)},
		},
	}); err != nil {
		t.Fatalf("CreateProvider(openai): %v", err)
	}
	if err := env.store.CreateProvider(ctx, config.Provider{
		ID:      "anthropic",
		Name:    "Anthropic",
		APIKey:  "sk-test",
		Enabled: true,
		Models: map[string]config.ProviderModelOverride{
			"disabled-custom": {Name: config.ValuePtr("Disabled Custom"), Enabled: config.ValuePtr(false)},
		},
	}); err != nil {
		t.Fatalf("CreateProvider(anthropic): %v", err)
	}
	if err := env.store.ReplaceCachedModels(ctx, "openai", []string{"gpt-4.1", "qwen3.6-plus", "disabled-fetched"}); err != nil {
		t.Fatalf("ReplaceCachedModels(openai): %v", err)
	}
	if err := env.store.ReplaceCachedModels(ctx, "anthropic", []string{"claude-sonnet-4-6"}); err != nil {
		t.Fatalf("ReplaceCachedModels(anthropic): %v", err)
	}

	server := &Server{controlPlane: controlplane.NewService(env.store, nil, nil, nil, nil)}
	req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	req = req.WithContext(withAuthInfo(req.Context(), &AuthInfo{
		UserID:  "test-user",
		IsAdmin: true,
	}))
	rec := httptest.NewRecorder()

	server.ListModels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp struct {
		Models []config.CachedModel `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	models := resp.Models

	got := make(map[string]bool, len(models))
	for _, model := range models {
		got[model.Provider+"/"+model.Model] = true
	}

	wantPresent := []string{
		"anthropic/claude-sonnet-4-6",
		"openai/custom-only",
		"openai/gpt-4.1",
		"openai/qwen3.6-plus",
	}
	for _, key := range wantPresent {
		if !got[key] {
			t.Errorf("expected model %q in response", key)
		}
	}

	wantAbsent := []string{
		"openai/disabled-fetched",
		"anthropic/disabled-custom",
	}
	for _, key := range wantAbsent {
		if got[key] {
			t.Errorf("did not expect model %q in response", key)
		}
	}

	if len(got) != len(wantPresent) {
		t.Fatalf("model count = %d, want %d (%v)", len(got), len(wantPresent), models)
	}
}

// TestReplaceCachedModelsIsPerProvider proves the durable cache keeps the disk
// cache's merge semantics: a second "fetch" replaces only its provider's model
// set (stale IDs drop out), and never touches another provider's cached models.
func TestReplaceCachedModelsIsPerProvider(t *testing.T) {
	env := setupAdminStore(t)
	ctx := context.Background()

	if err := env.store.ReplaceCachedModels(ctx, "anthropic", []string{"claude-sonnet-4-6"}); err != nil {
		t.Fatalf("ReplaceCachedModels(anthropic): %v", err)
	}
	// First fetch for openai.
	if err := env.store.ReplaceCachedModels(ctx, "openai", []string{"gpt-4.1", "stale"}); err != nil {
		t.Fatalf("ReplaceCachedModels(openai) first: %v", err)
	}
	// Second fetch replaces the openai set: "stale" is gone, "gpt-4.2" is new.
	if err := env.store.ReplaceCachedModels(ctx, "openai", []string{"gpt-4.1", "gpt-4.2"}); err != nil {
		t.Fatalf("ReplaceCachedModels(openai) second: %v", err)
	}

	cached, err := env.store.ListCachedModels(ctx)
	if err != nil {
		t.Fatalf("ListCachedModels: %v", err)
	}
	got := make(map[string]bool, len(cached))
	for _, m := range cached {
		got[m.Provider+"/"+m.Model] = true
	}

	want := map[string]bool{
		"anthropic/claude-sonnet-4-6": true,
		"openai/gpt-4.1":              true,
		"openai/gpt-4.2":              true,
	}
	for key := range want {
		if !got[key] {
			t.Errorf("expected %q in cache", key)
		}
	}
	if got["openai/stale"] {
		t.Errorf("stale openai model should have been replaced")
	}
	if len(got) != len(want) {
		t.Fatalf("cached count = %d, want %d (%v)", len(got), len(want), cached)
	}
}
