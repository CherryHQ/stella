package embedding

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/lcm"
	cfgstore "github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/pkg/ai"
)

// dbEmbeddingSettings adapts the persisted application setting exactly as the
// stellad composition root does, while keeping this contract in the owning
// embedding package so it can invoke one worker pass deterministically.
type dbEmbeddingSettings struct {
	store config.SettingStore
}

func (p dbEmbeddingSettings) EmbeddingSettings(ctx context.Context) (Settings, error) {
	settings, err := config.LoadEmbeddingSettings(ctx, p.store)
	if err != nil {
		return Settings{}, err
	}
	return Settings{
		Enabled:   settings.Enabled,
		Model:     settings.Model,
		Dim:       settings.Dim,
		APIKey:    settings.APIKey,
		BaseURL:   settings.BaseURL,
		Normalize: settings.Normalize,
	}, nil
}

// TestEmbeddingSettingsBackfillRestartAndSemanticQuery is the deterministic
// lifecycle contract for the semantic lane: persisted settings configure an
// OpenAI-compatible fake, the worker backfills PostgreSQL, and a fresh service
// instance queries the same vector space after restart.
func TestEmbeddingSettingsBackfillRestartAndSemanticQuery(t *testing.T) {
	var apiCalls atomic.Int32
	api := newFakeEmbeddingAPI(t, &apiCalls)

	db := dbtest.New(t)
	store := cfgstore.NewDBStore(db)
	ctx := context.Background()
	if err := config.SaveEmbeddingSettings(ctx, store, config.EmbeddingSettings{
		Enabled:   true,
		Model:     "contract-embedding",
		Dim:       3,
		APIKey:    "contract-key",
		BaseURL:   api.URL + "/v1",
		Normalize: true,
	}); err != nil {
		t.Fatalf("save embedding settings: %v", err)
	}

	writer, err := lcm.New(db, nil, nil)
	if err != nil {
		t.Fatalf("new LCM writer: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	session := memory.Session{
		ID:      "embedding-contract-session",
		UserID:  "embedding-contract-user",
		AgentID: "embedding-contract-agent",
		Channel: "test",
	}
	if err := writer.Bootstrap(ctx, session); err != nil {
		t.Fatalf("bootstrap LCM: %v", err)
	}
	for _, content := range []string{
		"harbor archive with unrelated vocabulary",
		"garden ledger with ordinary records",
	} {
		if err := writer.Append(ctx, session, ai.UserMessage{Content: content}); err != nil {
			t.Fatalf("append %q: %v", content, err)
		}
	}

	settings := dbEmbeddingSettings{store: store}
	beforeRestart := Boot(BootConfig{DB: db, Settings: settings, BatchSize: 100})
	worker := &backfillWorker{
		svc: beforeRestart,
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if err := worker.Work(ctx, nil); err != nil {
		t.Fatalf("embedding worker: %v", err)
	}

	const space = "contract-embedding@3"
	var stored int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM ctx_message_embedding WHERE model = $1`, space).Scan(&stored); err != nil {
		t.Fatalf("count stored embeddings: %v", err)
	}
	if stored != 2 {
		t.Fatalf("stored embeddings = %d, want 2", stored)
	}

	// Constructing both services and the LCM provider again models a process
	// restart: no cached chain or indexer survives, only DB settings/vectors do.
	afterRestart := Boot(BootConfig{DB: db, Settings: settings, BatchSize: 100})
	reader, err := lcm.New(db, nil, nil, lcm.WithQueryEmbedder(afterRestart))
	if err != nil {
		t.Fatalf("new restarted LCM reader: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })

	results, err := reader.Search(ctx, session, memory.SearchQuery{
		Text:  "violet constellation",
		Scope: memory.SearchScopeMessages,
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("semantic search: %v", err)
	}
	var foundVectorOnly bool
	for _, result := range results {
		if strings.Contains(result.Content, "harbor archive") {
			foundVectorOnly = true
		}
	}
	if !foundVectorOnly {
		t.Fatalf("semantic search did not surface the non-lexical vector match: %+v", results)
	}
	if calls := apiCalls.Load(); calls < 2 {
		t.Fatalf("embedding API calls = %d, want document backfill plus restarted query", calls)
	}
}

// newFakeEmbeddingAPI implements the OpenAI embeddings wire contract and gives
// selected texts stable vector directions for a predictable semantic ranking.
func newFakeEmbeddingAPI(t *testing.T, calls *atomic.Int32) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/embeddings" {
			http.Error(w, "unexpected endpoint", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer contract-key" {
			http.Error(w, "missing contract auth", http.StatusUnauthorized)
			return
		}
		var request struct {
			Input      []string `json:"input"`
			Model      string   `json:"model"`
			Dimensions int      `json:"dimensions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, fmt.Sprintf("decode request: %v", err), http.StatusBadRequest)
			return
		}
		if request.Model != "contract-embedding" || request.Dimensions != 3 {
			http.Error(w, "unexpected model or dimensions", http.StatusBadRequest)
			return
		}

		data := make([]map[string]any, len(request.Input))
		for i, text := range request.Input {
			vector := []float64{0, 1, 0}
			if strings.Contains(text, "harbor archive") || strings.Contains(text, "violet constellation") {
				vector = []float64{1, 0, 0}
			}
			data[i] = map[string]any{
				"object":    "embedding",
				"embedding": vector,
				"index":     i,
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data":   data,
			"model":  request.Model,
			"usage": map[string]any{
				"prompt_tokens": len(request.Input),
				"total_tokens":  len(request.Input),
			},
		})
	}))
	t.Cleanup(server.Close)
	return server
}
