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

func runRecallyApp(t *testing.T, args ...string) string {
	t.Helper()
	out := &bytes.Buffer{}
	app := ucli.NewApp()
	app.Writer = out
	app.Commands = []*ucli.Command{recallyCommand()}
	if err := app.Run(append([]string{"stella"}, args...)); err != nil {
		t.Fatalf("Run %v: %v", args, err)
	}
	return out.String()
}

func TestRecallySaveUsesPositionalURL(t *testing.T) {
	t.Setenv("STELLA_TOKEN", "test-token")
	var gotURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/recally/articles" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		gotURL = body.URL
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"art-1","file_path":"/lib/art-1.md","url":"https://example.com","title":"X","status":"unread","source_type":"web","starred":false,"saved_at":"2026-06-02T00:00:00Z"}`))
	}))
	defer server.Close()
	t.Setenv("STELLA_SERVER_URL", server.URL)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	out := runRecallyApp(t, "recally", "save", "--json", "https://example.com")
	if gotURL != "https://example.com" {
		t.Fatalf("server got url %q, want https://example.com", gotURL)
	}
	var res struct {
		ID         string `json:"id"`
		URL        string `json:"url"`
		Status     string `json:"status"`
		SourceType string `json:"source_type"`
		Created    *bool  `json:"created"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("output not JSON: %v (%q)", err, out)
	}
	if res.ID != "art-1" || res.URL != "https://example.com" || res.Status != "unread" || res.SourceType != "web" {
		t.Fatalf("got %+v", res)
	}
	if res.Created != nil {
		t.Fatalf("save --json must return the API Article shape without CLI-only created field: %+v", res)
	}
}

func TestRecallySaveRequiresURL(t *testing.T) {
	app := ucli.NewApp()
	app.Commands = []*ucli.Command{recallyCommand()}
	if err := app.Run([]string{"stella", "recally", "save"}); err == nil {
		t.Fatal("expected error when no url given")
	}
}

func TestRecallySaveRejectsFlagsAfterURL(t *testing.T) {
	app := ucli.NewApp()
	app.Commands = []*ucli.Command{recallyCommand()}
	err := app.Run([]string{"stella", "recally", "save", "https://example.com", "--title", "Example"})
	if err == nil {
		t.Fatal("expected error for flags after url")
	}
	if got := err.Error(); got != "flags must be placed before positional arguments; usage: stella recally save [options] <url>" {
		t.Fatalf("error = %q", got)
	}
}

func TestRecallyFeedMarkRejectsFlagsAfterArgs(t *testing.T) {
	app := ucli.NewApp()
	app.Commands = []*ucli.Command{recallyCommand()}
	err := app.Run([]string{"stella", "recally", "feed", "mark", "feed-1", "entry-1", "--status", "saved"})
	if err == nil {
		t.Fatal("expected error for flags after feed entry args")
	}
	if got := err.Error(); got != "flags must be placed before positional arguments; usage: stella recally feed mark [options] <feed-id> <entry-id>" {
		t.Fatalf("error = %q", got)
	}
}

func TestRecallyFeedMarkRequiresStatus(t *testing.T) {
	app := ucli.NewApp()
	app.Commands = []*ucli.Command{recallyCommand()}
	err := app.Run([]string{"stella", "recally", "feed", "mark", "feed-1", "entry-1"})
	if err == nil {
		t.Fatal("expected error when status is missing")
	}
	if got := err.Error(); got != "--status is required" {
		t.Fatalf("error = %q", got)
	}
}

func TestRecallyDigestSaveRouting(t *testing.T) {
	t.Setenv("STELLA_TOKEN", "test-token")
	var hitPath, hitMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitPath, hitMethod = r.URL.Path, r.Method
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"d-1","date":"2026-06-02","narrative":"n","created_at":"2026-06-02T00:00:00Z","updated_at":"2026-06-02T00:00:00Z","total_articles":0,"unread_count":0,"read_count":0,"archived_count":0,"starred_count":0,"saved_yesterday_count":0,"worth_revisiting_count":0,"saved_yesterday":[],"worth_revisiting":[],"top_tags":[]}`))
	}))
	defer server.Close()
	t.Setenv("STELLA_SERVER_URL", server.URL)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	runRecallyApp(t, "recally", "digest", "save", "--narrative", "n")
	if hitMethod != http.MethodPost || hitPath != "/api/recally/digests" {
		t.Fatalf("digest save hit %s %s, want POST /api/recally/digests", hitMethod, hitPath)
	}
}

func TestRecallyDigestDefaultRouting(t *testing.T) {
	t.Setenv("STELLA_TOKEN", "test-token")
	var hitPath, hitMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitPath, hitMethod = r.URL.Path, r.Method
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"date":"2026-06-02T00:00:00Z","total_articles":0,"unread_count":0,"read_count":0,"archived_count":0,"starred_count":0,"saved_yesterday_count":0,"worth_revisiting_count":0,"saved_yesterday":[],"worth_revisiting":[],"top_tags":[]}`))
	}))
	defer server.Close()
	t.Setenv("STELLA_SERVER_URL", server.URL)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	runRecallyApp(t, "recally", "digest")
	if hitMethod != http.MethodGet || hitPath != "/api/recally/digests/today" {
		t.Fatalf("digest hit %s %s, want GET /api/recally/digests/today", hitMethod, hitPath)
	}
}
