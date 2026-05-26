package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
)

func setupAdminStore(t *testing.T) config.Store {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	orgID, err := appdb.EnsureDefaultOrg(context.Background(), db)
	if err != nil {
		t.Fatalf("EnsureDefaultOrg: %v", err)
	}
	store := config.NewDBStore(db)
	store.SetDefaultOrgID(orgID)
	return store
}

func TestListCachedModelsMergesCustomAndFetchedAndFiltersDisabled(t *testing.T) {
	config.ResetStellaHome()
	t.Setenv("STELLA_HOME", t.TempDir())
	t.Cleanup(config.ResetStellaHome)

	store := setupAdminStore(t)
	ctx := context.Background()

	if err := store.CreateProvider(ctx, config.Provider{
		ID:     "openai",
		Name:   "OpenAI",
		APIKey: "sk-test",
		Models: map[string]config.ProviderModel{
			"qwen3.6-plus":     {ID: "qwen3.6-plus", Name: "Qwen 3.6 Plus", Enabled: true},
			"custom-only":      {ID: "custom-only", Name: "Custom Only", Enabled: true},
			"disabled-fetched": {ID: "disabled-fetched", Name: "disabled-fetched", Enabled: false},
			"disabled-custom":  {ID: "disabled-custom", Name: "disabled-custom", Enabled: false},
		},
	}); err != nil {
		t.Fatalf("CreateProvider(openai): %v", err)
	}
	if err := store.CreateProvider(ctx, config.Provider{
		ID:     "anthropic",
		Name:   "Anthropic",
		APIKey: "sk-test",
		Models: map[string]config.ProviderModel{
			"disabled-custom": {ID: "disabled-custom", Name: "Disabled Custom", Enabled: false},
		},
	}); err != nil {
		t.Fatalf("CreateProvider(anthropic): %v", err)
	}

	if err := config.SaveModelsCache(&config.ModelsCache{Models: []config.CachedModel{
		{Provider: "openai", Model: "gpt-4.1"},
		{Provider: "openai", Model: "qwen3.6-plus"},
		{Provider: "openai", Model: "disabled-fetched"},
		{Provider: "anthropic", Model: "claude-sonnet-4-6"},
	}}); err != nil {
		t.Fatalf("SaveModelsCache: %v", err)
	}

	server := &Server{store: store}
	req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	rec := httptest.NewRecorder()

	server.ListModels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var models []config.CachedModel
	if err := json.Unmarshal(rec.Body.Bytes(), &models); err != nil {
		t.Fatalf("decode response: %v", err)
	}

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
