package admin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/vaayne/anna/internal/admin"
	"github.com/vaayne/anna/internal/config"
	appdb "github.com/vaayne/anna/internal/db"
	"github.com/vaayne/anna/internal/memory"
)

func setupAdmin(t *testing.T) *admin.Server {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store := config.NewDBStore(db)
	if err := store.SeedDefaults(context.Background()); err != nil {
		t.Fatalf("SeedDefaults: %v", err)
	}

	mem := memory.NewEngineFromDB(db, nil)
	return admin.New(store, mem, db)
}

func doRequest(t *testing.T, srv *admin.Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

type apiResponse struct {
	Data  json.RawMessage `json:"data"`
	Error string          `json:"error"`
}

func parseResponse(t *testing.T, rr *httptest.ResponseRecorder) apiResponse {
	t.Helper()
	var resp apiResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v (body: %s)", err, rr.Body.String())
	}
	return resp
}

func TestListProviders(t *testing.T) {
	srv := setupAdmin(t)

	rr := doRequest(t, srv, "GET", "/api/providers", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	resp := parseResponse(t, rr)
	var providers []config.Provider
	if err := json.Unmarshal(resp.Data, &providers); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// SeedDefaults creates "anthropic" provider.
	if len(providers) == 0 {
		t.Fatal("expected at least one provider")
	}
	if providers[0].ID != "anthropic" {
		t.Errorf("provider ID = %q, want %q", providers[0].ID, "anthropic")
	}
}

func TestCreateProvider(t *testing.T) {
	srv := setupAdmin(t)

	body := map[string]any{
		"id":      "openai",
		"name":    "OpenAI",
		"api_key": "sk-test",
	}
	rr := doRequest(t, srv, "POST", "/api/providers", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}

	// Verify it appears in list.
	rr = doRequest(t, srv, "GET", "/api/providers", nil)
	resp := parseResponse(t, rr)
	var providers []config.Provider
	_ = json.Unmarshal(resp.Data, &providers)
	found := false
	for _, p := range providers {
		if p.ID == "openai" {
			found = true
		}
	}
	if !found {
		t.Error("created provider not found in list")
	}
}

func TestListAgents(t *testing.T) {
	srv := setupAdmin(t)

	rr := doRequest(t, srv, "GET", "/api/agents", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	resp := parseResponse(t, rr)
	var agents []config.Agent
	if err := json.Unmarshal(resp.Data, &agents); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(agents) == 0 {
		t.Fatal("expected at least one agent")
	}
	if agents[0].ID != "anna" {
		t.Errorf("agent ID = %q, want %q", agents[0].ID, "anna")
	}
}

func TestCreateAgent(t *testing.T) {
	srv := setupAdmin(t)

	body := config.Agent{
		ID:           "coder",
		Name:         "Coder",
		Model:        "anthropic/claude-sonnet-4-6",
		SystemPrompt: "You are a coding assistant.",
		Workspace:    "/tmp/coder",
		Enabled:      true,
	}
	rr := doRequest(t, srv, "POST", "/api/agents", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}

	// Verify via get.
	rr = doRequest(t, srv, "GET", "/api/agents/coder", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestCORSPreflight(t *testing.T) {
	srv := setupAdmin(t)

	rr := doRequest(t, srv, "OPTIONS", "/api/providers", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNoContent)
	}
	if rr.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("missing CORS header")
	}
}
