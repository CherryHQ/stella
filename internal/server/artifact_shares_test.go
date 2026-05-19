package server_test

import (
	"context"
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
