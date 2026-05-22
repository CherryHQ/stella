package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	ucli "github.com/urfave/cli/v2"

	"github.com/CherryHQ/stella/internal/config"
)

func TestShareArtifactRequiresSessionID(t *testing.T) {
	t.Setenv("STELLA_AGENT_ID", "agent-1")
	app := ucli.NewApp()
	app.Commands = []*ucli.Command{shareCommand()}
	var out bytes.Buffer
	app.Writer = &out
	err := app.Run([]string{"stella", "share", "artifact", "report.html"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestShareArtifactPrintsURL(t *testing.T) {
	t.Setenv("STELLA_TOKEN", "test-token")
	t.Setenv("STELLA_AGENT_ID", "agent-1")
	t.Setenv("STELLA_SESSION_ID", "session-1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/shares" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("Authorization = %q", got)
		}
		var body struct {
			Source    string `json:"source"`
			AgentID   string `json:"agent_id"`
			SessionID string `json:"session_id"`
			Path      string `json:"path"`
			ExpiresIn string `json:"expires_in"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.Source != "artifact" || body.AgentID != "agent-1" || body.SessionID != "session-1" || body.Path != "report.html" || body.ExpiresIn != "1d" {
			t.Fatalf("unexpected body: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":"share-1","url":"https://example.com/s/token","title":"report.html","media_type":"text/html; charset=utf-8","created_at":"2026-05-19 00:00:00"}}`))
	}))
	defer server.Close()
	t.Setenv("STELLA_SERVER_URL", server.URL)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	app := ucli.NewApp()
	app.Commands = []*ucli.Command{shareCommand()}
	var out bytes.Buffer
	app.Writer = &out
	if err := app.Run([]string{"stella", "share", "artifact", "--expires-in", "1d", "report.html"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := out.String(); got != "https://example.com/s/token\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestShareArticlePrintsURL(t *testing.T) {
	t.Setenv("STELLA_TOKEN", "test-token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/shares" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Source    string `json:"source"`
			ArticleID string `json:"article_id"`
			ExpiresIn string `json:"expires_in"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.Source != "article" || body.ArticleID != "art-123" || body.ExpiresIn != "7d" {
			t.Fatalf("unexpected body: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":"share-2","url":"https://example.com/s/abc","title":"My Article","media_type":"text/markdown; charset=utf-8","created_at":"2026-05-20 00:00:00"}}`))
	}))
	defer server.Close()
	t.Setenv("STELLA_SERVER_URL", server.URL)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	app := ucli.NewApp()
	app.Commands = []*ucli.Command{shareCommand()}
	var out bytes.Buffer
	app.Writer = &out
	if err := app.Run([]string{"stella", "share", "article", "art-123"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := out.String(); got != "https://example.com/s/abc\n" {
		t.Fatalf("output = %q", got)
	}
}
