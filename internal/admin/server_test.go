package admin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vaayne/anna/internal/admin"
	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/config"
	appdb "github.com/vaayne/anna/internal/db"
	"github.com/vaayne/anna/internal/memory"
)

type testEnv struct {
	srv       *admin.Server
	authStore auth.AuthStore
	adminUser auth.AuthUser
	sessionID string
}

func setupAdmin(t *testing.T) *testEnv {
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

	as := appdb.NewAuthStore(db)
	if err := auth.SeedPolicies(context.Background(), as); err != nil {
		t.Fatalf("SeedPolicies: %v", err)
	}

	engine, err := auth.NewEngine(context.Background(), as)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	mem := memory.NewEngineFromDB(db, nil)
	srv := admin.New(store, as, engine, mem, db, auth.NewLinkCodeStore())

	// Create an admin user for authenticated requests.
	hash, _ := auth.HashPassword("testpassword")
	user, err := as.CreateUser(context.Background(), "testadmin", hash)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	_ = as.UpdateUserRole(context.Background(), user.ID, auth.RoleAdmin)

	sessionID := auth.NewSessionID()
	_, err = as.CreateSession(context.Background(), auth.Session{
		ID:        sessionID,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(auth.SessionDuration),
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	return &testEnv{
		srv:       srv,
		authStore: as,
		adminUser: user,
		sessionID: sessionID,
	}
}

func doRequest(t *testing.T, env *testEnv, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return doRequestWithSession(t, env.srv, env.sessionID, method, path, body)
}

func doRequestWithSession(t *testing.T, srv *admin.Server, sessionID, method, path string, body any) *httptest.ResponseRecorder {
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
	if sessionID != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionID})
	}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

func doUnauthRequest(t *testing.T, srv *admin.Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return doRequestWithSession(t, srv, "", method, path, body)
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
	env := setupAdmin(t)

	rr := doRequest(t, env, "GET", "/api/providers", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	resp := parseResponse(t, rr)
	var providers []config.Provider
	if err := json.Unmarshal(resp.Data, &providers); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(providers) == 0 {
		t.Fatal("expected at least one provider")
	}
	if providers[0].ID != "anthropic" {
		t.Errorf("provider ID = %q, want %q", providers[0].ID, "anthropic")
	}
}

func TestCreateProvider(t *testing.T) {
	env := setupAdmin(t)

	body := map[string]any{
		"id":      "openai",
		"name":    "OpenAI",
		"api_key": "sk-test",
	}
	rr := doRequest(t, env, "POST", "/api/providers", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}

	// Verify it appears in list.
	rr = doRequest(t, env, "GET", "/api/providers", nil)
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
	env := setupAdmin(t)

	rr := doRequest(t, env, "GET", "/api/agents", nil)
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
	env := setupAdmin(t)

	body := config.Agent{
		ID:           "coder",
		Name:         "Coder",
		Model:        "anthropic/claude-sonnet-4-6",
		SystemPrompt: "You are a coding assistant.",
		Workspace:    "/tmp/coder",
		Enabled:      true,
	}
	rr := doRequest(t, env, "POST", "/api/agents", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}

	// Verify via get.
	rr = doRequest(t, env, "GET", "/api/agents/coder", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestRootRedirect(t *testing.T) {
	env := setupAdmin(t)

	// Authenticated admin -> /providers.
	rr := doRequest(t, env, "GET", "/", nil)
	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusFound)
	}
	loc := rr.Header().Get("Location")
	if loc != "/providers" {
		t.Errorf("Location = %q, want %q", loc, "/providers")
	}

	// Unauthenticated -> /login.
	rr = doUnauthRequest(t, env.srv, "GET", "/", nil)
	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusFound)
	}
	loc = rr.Header().Get("Location")
	if loc != "/login" {
		t.Errorf("Location = %q, want %q", loc, "/login")
	}
}

func TestPageRoutes(t *testing.T) {
	env := setupAdmin(t)

	pages := []string{
		"/providers", "/agents", "/channels",
		"/users", "/sessions", "/scheduler", "/settings",
	}
	for _, path := range pages {
		t.Run(path, func(t *testing.T) {
			rr := doRequest(t, env, "GET", path, nil)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
			}
			ct := rr.Header().Get("Content-Type")
			if ct != "text/html; charset=utf-8" {
				t.Errorf("Content-Type = %q, want %q", ct, "text/html; charset=utf-8")
			}
			body := rr.Body.String()
			if len(body) == 0 {
				t.Fatal("empty body")
			}
			if !strings.Contains(body, "Anna Admin") {
				t.Error("body missing page title")
			}
		})
	}
}

func TestUnknownPathReturns404(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "GET", "/nonexistent", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestCORSPreflight(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "OPTIONS", "/api/providers", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNoContent)
	}
	origin := rr.Header().Get("Access-Control-Allow-Origin")
	if origin == "" {
		t.Error("missing CORS origin header")
	}
	if rr.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Error("missing CORS credentials header")
	}
}

func TestLoginPageAccessible(t *testing.T) {
	env := setupAdmin(t)

	rr := doUnauthRequest(t, env.srv, "GET", "/login", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	ct := rr.Header().Get("Content-Type")
	if ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want %q", ct, "text/html; charset=utf-8")
	}
}

func TestUnauthenticatedAPIReturns401(t *testing.T) {
	env := setupAdmin(t)

	rr := doUnauthRequest(t, env.srv, "GET", "/api/agents", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusUnauthorized, rr.Body.String())
	}
}

func TestUnauthenticatedPageRedirectsToLogin(t *testing.T) {
	env := setupAdmin(t)

	rr := doUnauthRequest(t, env.srv, "GET", "/agents", nil)
	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusFound)
	}
	loc := rr.Header().Get("Location")
	if loc != "/login" {
		t.Errorf("Location = %q, want %q", loc, "/login")
	}
}

func TestNonAdminCannotAccessAdminRoutes(t *testing.T) {
	env := setupAdmin(t)

	// Create a non-admin user.
	hash, _ := auth.HashPassword("userpassword")
	user, err := env.authStore.CreateUser(context.Background(), "regularuser", hash)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	sessionID := auth.NewSessionID()
	_, err = env.authStore.CreateSession(context.Background(), auth.Session{
		ID:        sessionID,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(auth.SessionDuration),
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Admin-only API should return 403.
	rr := doRequestWithSession(t, env.srv, sessionID, "GET", "/api/providers", nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusForbidden, rr.Body.String())
	}

	// Admin-only page should redirect to /agents.
	rr = doRequestWithSession(t, env.srv, sessionID, "GET", "/providers", nil)
	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusFound)
	}
	loc := rr.Header().Get("Location")
	if loc != "/agents" {
		t.Errorf("Location = %q, want %q", loc, "/agents")
	}

	// Non-admin page should be accessible.
	rr = doRequestWithSession(t, env.srv, sessionID, "GET", "/agents", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
}
