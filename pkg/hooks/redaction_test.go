package hooks

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactToolTextPreservesStructuredJSON(t *testing.T) {
	input := `{"next_page_token":"eyJvIjoyfQ","access_token":"gho_12345678901234567890","webhook_secret":"opaque","bot_token":"opaque","db_password":"hunter2","nested":{"message":"Authorization: Bearer secret-value","html":"<article>","count":9007199254740993}}`
	got := RedactToolText(input)
	if !json.Valid([]byte(got)) {
		t.Fatalf("redacted JSON is invalid: %s", got)
	}
	var value struct {
		NextPageToken string `json:"next_page_token"`
		AccessToken   string `json:"access_token"`
		WebhookSecret string `json:"webhook_secret"`
		BotToken      string `json:"bot_token"`
		DBPassword    string `json:"db_password"`
		Nested        struct {
			Message string      `json:"message"`
			HTML    string      `json:"html"`
			Count   json.Number `json:"count"`
		} `json:"nested"`
	}
	decoder := json.NewDecoder(strings.NewReader(got))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	if value.NextPageToken != "eyJvIjoyfQ" {
		t.Fatalf("next_page_token = %q, want unchanged", value.NextPageToken)
	}
	if value.AccessToken != "[REDACTED]" || value.WebhookSecret != "[REDACTED]" || value.BotToken != "[REDACTED]" || value.DBPassword != "[REDACTED]" {
		t.Fatalf("secret keys were not redacted: %#v", value)
	}
	if strings.Contains(value.Nested.Message, "secret-value") || value.Nested.HTML != "<article>" || value.Nested.Count.String() != "9007199254740993" {
		t.Fatalf("nested value = %#v", value.Nested)
	}
}

func TestRedactToolTextMasksGitHubFineGrainedAndQuotedSecrets(t *testing.T) {
	for _, input := range []string{
		`github_pat_0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ`,
		`token="github_pat_0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"`,
	} {
		got := RedactToolText(input)
		if strings.Contains(got, "github_pat_") || !strings.Contains(got, "[REDACTED]") {
			t.Fatalf("RedactToolText(%q) = %q", input, got)
		}
	}
}
