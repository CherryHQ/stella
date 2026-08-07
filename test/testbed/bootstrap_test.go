package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBootstrapDrivesHTTPFlowAndWritesCredentials(t *testing.T) {
	t.Parallel()
	var calls []string
	var adminEmail, userEmail string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/readyz":
			if len(calls) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
		case "/api/auth/local/register":
			var body map[string]string
			decodeBody(t, r, &body)
			if body["email"] == "" || body["email"] != strings.ToLower(body["email"]) || body["password"] != body["confirm_password"] || len(body["password"]) < 8 {
				t.Fatal("registration body does not contain a canonical email and valid random password")
			}
			adminEmail = strings.ToLower(body["email"])
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "cookie", Path: "/"})
			writeJSON(w, map[string]string{"redirect_url": "/agents"})
		case "/api/auth/me":
			switch r.Header.Get("Authorization") {
			case "":
				requireCookie(t, r)
				writeJSON(w, map[string]any{"id": "admin-id", "email": adminEmail, "role": "admin", "is_admin": true})
			case "Bearer admin-secret":
				writeJSON(w, map[string]any{"id": "admin-id", "email": adminEmail, "role": "admin", "is_admin": true})
			case "Bearer user-secret":
				writeJSON(w, map[string]any{"id": "user-id", "email": userEmail, "role": "user", "is_admin": false})
			default:
				t.Fatalf("unexpected Authorization header")
			}
		case "/api/users/me/tokens":
			requireCookie(t, r)
			writeJSONStatus(w, http.StatusCreated, map[string]any{"token": "admin-secret", "personal_access_token": map[string]string{"id": "admin-pat"}})
		case "/api/admin/provisioning-tokens":
			requireCookie(t, r)
			if r.Method == http.MethodDelete {
				t.Fatal("revoke path must include the provisioning token id")
			}
			var body struct {
				ExpiresAt string `json:"expires_at"`
			}
			decodeBody(t, r, &body)
			expiresAt, err := time.Parse(time.RFC3339, body.ExpiresAt)
			if err != nil {
				t.Fatalf("parse provisioning expiry %q: %v", body.ExpiresAt, err)
			}
			if until := expiresAt.Sub(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)); until != provisioningTTL {
				t.Fatalf("provisioning expiry = %q, want five-minute TTL", body.ExpiresAt)
			}
			writeJSONStatus(w, http.StatusCreated, map[string]any{"token": "provisioning-secret", "provisioning_token": map[string]string{"id": "provisioning-id"}})
		case "/api/provisioned-users":
			if got := r.Header.Get("Authorization"); got != "Bearer provisioning-secret" {
				t.Fatalf("provisioned user Authorization = %q, want provisioning bearer", got)
			}
			if _, err := r.Cookie("session"); err == nil {
				t.Fatal("provisioned user request must not carry admin session cookie")
			}
			var body map[string]string
			decodeBody(t, r, &body)
			userEmail = body["email"]
			writeJSONStatus(w, http.StatusCreated, map[string]any{"token": "user-secret", "provisioned_user": map[string]any{"id": "provisioned-resource-id", "role": "user", "is_active": true}})
		case "/api/admin/provisioning-tokens/provisioning-id":
			requireCookie(t, r)
			if r.Method != http.MethodDelete {
				t.Fatalf("revoke method = %s, want DELETE", r.Method)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	home := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	path, reused, err := bootstrap(ctx, bootstrapConfig{
		BaseURL: server.URL, Home: home,
		Now: func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if reused {
		t.Fatal("fresh bootstrap reported reused credentials")
	}
	if got, want := calls, []string{
		"GET /readyz", "GET /readyz", "POST /api/auth/local/register", "GET /api/auth/me", "POST /api/users/me/tokens",
		"POST /api/admin/provisioning-tokens", "POST /api/provisioned-users", "DELETE /api/admin/provisioning-tokens/provisioning-id",
		"GET /api/auth/me", "GET /api/auth/me", "GET /api/auth/me",
	}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read credentials: %v", err)
	}
	var got credentials
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode credentials: %v", err)
	}
	if got.Version != 1 || got.Admin.ID != "admin-id" || got.Admin.Role != "admin" || got.Admin.Token != "admin-secret" || got.User.ID != "user-id" || got.User.Role != "user" || got.User.Token != "user-secret" || got.Admin.Password == "" {
		t.Fatalf("credentials = %#v", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("credentials mode = %o, want 600", info.Mode().Perm())
		}
	}
}

func TestBootstrapReusesAuthenticatedCredentials(t *testing.T) {
	t.Parallel()
	creds := validCredentials("")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/readyz":
			w.WriteHeader(http.StatusOK)
		case "/api/auth/me":
			switch r.Header.Get("Authorization") {
			case "Bearer admin-token":
				writeJSON(w, map[string]any{"id": creds.Admin.ID, "email": creds.Admin.Email, "role": "admin", "is_admin": true})
			case "Bearer user-token":
				writeJSON(w, map[string]any{"id": creds.User.ID, "email": creds.User.Email, "role": "user", "is_admin": false})
			default:
				t.Fatalf("unexpected request credentials")
			}
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	creds.BaseURL = server.URL
	home := t.TempDir()
	path := filepath.Join(home, credentialsFilename)
	if err := writeCredentials(path, creds); err != nil {
		t.Fatalf("write existing credentials: %v", err)
	}
	got, reused, err := bootstrap(context.Background(), bootstrapConfig{BaseURL: creds.BaseURL, Home: home})
	if err != nil || !reused || got != path {
		t.Fatalf("bootstrap = (%q, %v, %v), want (%q, true, nil)", got, reused, err, path)
	}
}

func TestBootstrapRefusesStaleCredentialsWithoutOverwrite(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/readyz":
			w.WriteHeader(http.StatusOK)
		case "/api/auth/me":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	home := t.TempDir()
	path := filepath.Join(home, credentialsFilename)
	creds := validCredentials(server.URL)
	if err := writeCredentials(path, creds); err != nil {
		t.Fatalf("write existing credentials: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = bootstrap(context.Background(), bootstrapConfig{BaseURL: server.URL, Home: home})
	if err == nil || !strings.Contains(err.Error(), "fresh STELLA_HOME") {
		t.Fatalf("bootstrap error = %v, want actionable stale-credential failure", err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("stale credentials changed: read error=%v", readErr)
	}
}

func TestNormalizeBaseURLRejectsSecretOrAmbiguousURLs(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"stella.example.test",
		"ftp://stella.example.test",
		"https://user:secret@stella.example.test",
		"https://stella.example.test/prefix",
		"https://stella.example.test?tenant=test",
		"https://stella.example.test#fragment",
	} {
		if _, err := normalizeBaseURL(raw); err == nil {
			t.Errorf("normalizeBaseURL(%q) unexpectedly succeeded", raw)
		}
	}
	if got, err := normalizeBaseURL("https://stella.example.test/"); err != nil || got != "https://stella.example.test" {
		t.Fatalf("normalizeBaseURL(valid) = %q, %v", got, err)
	}
}

func TestBootstrapRefusesUnsafeExistingCredentials(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{name: "invalid JSON", setup: func(t *testing.T, path string) { writeSecretFile(t, path, []byte("not json"), 0o600) }},
		{name: "world readable", setup: func(t *testing.T, path string) { writeSecretFile(t, path, []byte(`{"version":1}`), 0o644) }},
		{name: "symlink", setup: func(t *testing.T, path string) {
			if runtime.GOOS == "windows" {
				t.Skip("symlink setup requires Windows privileges")
			}
			target := filepath.Join(t.TempDir(), "target")
			writeSecretFile(t, target, []byte(`{"version":1}`), 0o600)
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, credentialsFilename)
			tc.setup(t, path)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = bootstrap(context.Background(), bootstrapConfig{BaseURL: "http://127.0.0.1:25678", Home: home})
			if err == nil {
				t.Fatal("bootstrap accepted unsafe existing credentials")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(before) {
				t.Fatalf("unsafe credentials changed: before=%q after=%q err=%v", before, after, err)
			}
		})
	}
}

func TestBootstrapFailureDoesNotPublishCredentials(t *testing.T) {
	t.Parallel()
	var adminEmail string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/readyz":
			w.WriteHeader(http.StatusOK)
		case "/api/auth/local/register":
			var body map[string]string
			decodeBody(t, r, &body)
			adminEmail = body["email"]
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "cookie", Path: "/"})
			writeJSON(w, map[string]string{"redirect_url": "/agents"})
		case "/api/auth/me":
			writeJSON(w, map[string]any{"id": "admin-id", "email": adminEmail, "role": "admin", "is_admin": true})
		case "/api/users/me/tokens":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	home := t.TempDir()
	_, _, err := bootstrap(context.Background(), bootstrapConfig{BaseURL: server.URL, Home: home})
	if err == nil {
		t.Fatal("bootstrap unexpectedly succeeded")
	}
	if _, statErr := os.Stat(filepath.Join(home, credentialsFilename)); !os.IsNotExist(statErr) {
		t.Fatalf("credentials file exists after failed bootstrap: %v", statErr)
	}
}

func decodeBody(t *testing.T, r *http.Request, out any) {
	t.Helper()
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		t.Fatal(err)
	}
}

func requireCookie(t *testing.T, r *http.Request) {
	t.Helper()
	cookie, err := r.Cookie("session")
	if err != nil || cookie.Value != "cookie" {
		t.Fatalf("missing session cookie: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	writeJSONStatus(w, http.StatusOK, value)
}

func writeJSONStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func validCredentials(baseURL string) credentials {
	creds := credentials{Version: 1, BaseURL: baseURL}
	creds.Admin.ID, creds.Admin.Email, creds.Admin.Role = "admin-id", "admin@example.test", "admin"
	creds.Admin.Password, creds.Admin.Token = "password", "admin-token"
	creds.User.ID, creds.User.Email, creds.User.Role, creds.User.Token = "user-id", "user@example.test", "user", "user-token"
	return creds
}

func writeSecretFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
