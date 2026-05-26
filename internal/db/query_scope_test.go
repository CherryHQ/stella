package db_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrgOwnedQueriesMentionOrgID(t *testing.T) {
	queryDir := "queries"
	files := map[string][]string{
		"plugin_oauth_provider.sql": {"GetAuthOAuthProvider", "UpsertAuthOAuthProvider", "DeleteAuthOAuthProvider"},
		"settings_plugin_state.sql": {"GetPluginStateEntry", "UpsertPluginStateEntry", "DeletePluginStateEntry"},
	}
	for file, queries := range files {
		body, err := os.ReadFile(filepath.Join(queryDir, file))
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		text := string(body)
		for _, name := range queries {
			block := queryBlock(text, name)
			if block == "" {
				t.Fatalf("%s missing query %s", file, name)
			}
			if !strings.Contains(block, "org_id") {
				t.Fatalf("%s:%s does not scope by org_id:\n%s", file, name, block)
			}
		}
	}
}

func queryBlock(text, name string) string {
	marker := "-- name: " + name + " :"
	start := strings.Index(text, marker)
	if start == -1 {
		return ""
	}
	rest := text[start:]
	if next := strings.Index(rest[len(marker):], "\n-- name: "); next >= 0 {
		return rest[:len(marker)+next]
	}
	return rest
}
