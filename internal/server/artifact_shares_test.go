package server_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestArtifactShareLifecycle(t *testing.T) {
	env := setupArtifactShareWorkspace(t)

	rr := doRequest(t, env, http.MethodPost, "/api/artifact-shares", map[string]any{
		"session_id": "artifact-session",
		"path":       "report.html",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	var envelope struct {
		Data struct {
			ID              string  `json:"id"`
			URL             string  `json:"url"`
			Title           string  `json:"title"`
			SourceSessionID string  `json:"source_session_id"`
			SourcePath      string  `json:"source_path"`
			MediaType       string  `json:"media_type"`
			Kind            string  `json:"kind"`
			SizeBytes       int64   `json:"size_bytes"`
			ExpiresAt       *string `json:"expires_at"`
			Revoked         bool    `json:"revoked"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data.ID == "" || envelope.Data.URL == "" {
		t.Fatalf("missing share id/url: %+v", envelope.Data)
	}
	if envelope.Data.Title != "report.html" || envelope.Data.Kind != "html" || envelope.Data.MediaType != "text/html; charset=utf-8" {
		t.Fatalf("unexpected share metadata: %+v", envelope.Data)
	}
	if envelope.Data.ExpiresAt == nil {
		t.Fatal("default expiry should be set")
	}
	if _, err := time.Parse("2006-01-02 15:04:05", *envelope.Data.ExpiresAt); err != nil {
		t.Fatalf("expiry format: %v", err)
	}
	if !strings.HasPrefix(envelope.Data.URL, "http://example.com/s/") {
		t.Fatalf("unexpected share URL %q", envelope.Data.URL)
	}

	list := doRequest(t, env, http.MethodGet, "/api/artifact-shares", nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d body = %s", list.Code, list.Body.String())
	}
	if !strings.Contains(list.Body.String(), envelope.Data.ID) {
		t.Fatalf("list response missing share: %s", list.Body.String())
	}

	revoke := doRequest(t, env, http.MethodDelete, "/api/artifact-shares/"+url.PathEscape(envelope.Data.ID), nil)
	if revoke.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d body = %s", revoke.Code, revoke.Body.String())
	}
}

func TestPublicArtifactShareAccess(t *testing.T) {
	env := setupArtifactShareWorkspace(t)
	create := doRequest(t, env, http.MethodPost, "/api/artifact-shares", map[string]any{
		"session_id": "artifact-session",
		"path":       "report.html",
	})
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %s", create.Code, create.Body.String())
	}
	var envelope struct {
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	token := strings.TrimPrefix(envelope.Data.URL, "http://example.com/s/")
	if token == envelope.Data.URL || token == "" {
		t.Fatalf("could not extract token from %q", envelope.Data.URL)
	}

	metadata := doUnauthRequest(t, env.srv, http.MethodGet, "/api/public/artifact-shares/"+url.PathEscape(token), nil)
	if metadata.Code != http.StatusOK {
		t.Fatalf("metadata status = %d body = %s", metadata.Code, metadata.Body.String())
	}
	if !strings.Contains(metadata.Body.String(), "content_url") {
		t.Fatalf("metadata missing content_url: %s", metadata.Body.String())
	}

	content := doUnauthRequest(t, env.srv, http.MethodGet, "/api/public/artifact-shares/"+url.PathEscape(token)+"/content", nil)
	if content.Code != http.StatusOK {
		t.Fatalf("content status = %d body = %s", content.Code, content.Body.String())
	}
	if content.Header().Get("Content-Security-Policy") == "" || !strings.Contains(content.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("missing HTML sandbox CSP: %s", content.Header().Get("Content-Security-Policy"))
	}
	if content.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("missing nosniff header")
	}
	if !strings.Contains(content.Body.String(), "<h1>Hello</h1>") {
		t.Fatalf("content body = %s", content.Body.String())
	}

	hash := sha256.Sum256([]byte(token))
	share, err := sqlc.New(env.db).GetArtifactShareByTokenHash(context.Background(), hex.EncodeToString(hash[:]))
	if err != nil {
		t.Fatalf("lookup share by hash: %v", err)
	}
	if string(share.Content) == token || share.TokenHash == token {
		t.Fatal("plaintext token should not be stored")
	}
}

func TestPublicArtifactShareExpiredOrRevokedReturnsNotFound(t *testing.T) {
	env := setupArtifactShareWorkspace(t)
	create := doRequest(t, env, http.MethodPost, "/api/artifact-shares", map[string]any{
		"session_id": "artifact-session",
		"path":       "report.html",
	})
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %s", create.Code, create.Body.String())
	}
	var envelope struct {
		Data struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	token := strings.TrimPrefix(envelope.Data.URL, "http://example.com/s/")
	revoke := doRequest(t, env, http.MethodDelete, "/api/artifact-shares/"+url.PathEscape(envelope.Data.ID), nil)
	if revoke.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d body = %s", revoke.Code, revoke.Body.String())
	}
	metadata := doUnauthRequest(t, env.srv, http.MethodGet, "/api/public/artifact-shares/"+url.PathEscape(token), nil)
	if metadata.Code != http.StatusNotFound {
		t.Fatalf("metadata status = %d body = %s", metadata.Code, metadata.Body.String())
	}
}

func TestCreateArtifactShareRejectsUnsupportedType(t *testing.T) {
	env := setupArtifactShareWorkspace(t)
	rr := doRequest(t, env, http.MethodPost, "/api/artifact-shares", map[string]any{
		"session_id": "artifact-session",
		"path":       "notes.txt",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
}

func TestCreateArtifactShareRejectsOtherOwnerSession(t *testing.T) {
	env := setupArtifactShareWorkspace(t)
	sm := env.mem.(memory.SessionManager)
	if err := sm.SaveInfo(context.Background(), memory.SessionInfo{
		ID:         "other-session",
		AgentID:    "test-agent",
		UserID:     "other-user",
		Channel:    "web",
		Kind:       "main",
		Title:      "Other",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
	}); err != nil {
		t.Fatalf("SaveInfo other session: %v", err)
	}
	rr := doRequest(t, env, http.MethodPost, "/api/artifact-shares", map[string]any{
		"session_id": "other-session",
		"path":       "report.html",
	})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
}

func TestCreateArtifactShareNeverExpires(t *testing.T) {
	env := setupArtifactShareWorkspace(t)
	rr := doRequest(t, env, http.MethodPost, "/api/artifact-shares", map[string]any{
		"session_id": "artifact-session",
		"path":       "report.html",
		"expires_in": "never",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "expires_at") {
		t.Fatalf("never-expiring share should omit expires_at: %s", rr.Body.String())
	}
}

func setupArtifactShareWorkspace(t *testing.T) *testEnv {
	t.Helper()
	env := setupAdmin(t)
	sm := env.mem.(memory.SessionManager)
	if err := sm.SaveInfo(context.Background(), memory.SessionInfo{
		ID:         "artifact-session",
		AgentID:    "test-agent",
		UserID:     env.adminUser.ID,
		Channel:    "web",
		Kind:       "main",
		Title:      "Artifacts",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
	}); err != nil {
		t.Fatalf("SaveInfo: %v", err)
	}
	root, err := agent.SetupUserWorkspace("test-agent", config.StellaHome(), env.adminUser.ID)
	if err != nil {
		t.Fatalf("SetupUserWorkspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "report.html"), []byte("<h1>Hello</h1>"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write notes: %v", err)
	}
	return env
}
