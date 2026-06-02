package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
)

// emailVaultServer serves the EMAIL_CONFIG vault entry holding a single "work"
// account whose password is "supersecret".
func emailVaultServer(t *testing.T) {
	t.Helper()
	cfgValue := `{"default":"work","accounts":{"work":{"imap_host":"imap.example.com","smtp_host":"smtp.example.com","username":"u@example.com","password":"supersecret","from":"u@example.com"}}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := map[string]any{"data": map[string]any{"name": emailConfigKey, "value": cfgValue}}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(server.Close)
	t.Setenv("STELLA_TOKEN", "test-token")
	t.Setenv("STELLA_SERVER_URL", server.URL)
	t.Setenv("EMAIL_CONFIG", "") // force vault fallback path off for config cmds (they use vault directly)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)
}

func TestEmailConfigGetRenamedFromShow(t *testing.T) {
	emailVaultServer(t)
	out, _ := runApp(t, emailCommand(), "email", "config", "get", "work")
	if !strings.Contains(out, "IMAP Host: imap.example.com") {
		t.Fatalf("expected account details, got %q", out)
	}
	if strings.Contains(out, "supersecret") {
		t.Fatalf("password must be masked in human output: %q", out)
	}
	if !strings.Contains(out, "Password:  ****") {
		t.Fatalf("expected masked password, got %q", out)
	}
}

func TestEmailConfigGetJSONMasksPassword(t *testing.T) {
	emailVaultServer(t)
	out, _ := runApp(t, emailCommand(), "email", "config", "get", "--json", "work")
	if strings.Contains(out, "supersecret") {
		t.Fatalf("JSON output leaked password: %q", out)
	}
	var acct struct {
		Password string `json:"password"`
		IMAPHost string `json:"imap_host"`
	}
	if err := json.Unmarshal([]byte(out), &acct); err != nil {
		t.Fatalf("output not JSON: %v (%q)", err, out)
	}
	if acct.Password != "****" || acct.IMAPHost != "imap.example.com" {
		t.Fatalf("got %+v", acct)
	}
}

func TestEmailConfigListJSONMasksPassword(t *testing.T) {
	emailVaultServer(t)
	out, _ := runApp(t, emailCommand(), "email", "config", "list", "--json")
	if strings.Contains(out, "supersecret") {
		t.Fatalf("JSON list leaked password: %q", out)
	}
	if !strings.Contains(out, "****") {
		t.Fatalf("expected masked password in JSON list, got %q", out)
	}
}
