package db

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	splitToolOverridesBeforeMigration = 90000000000025
	splitToolOverridesMigration       = 90000000000026
)

// retiredToolNames are the names this change removes: the seven union tools and
// the four recally tools it renamed. The pre-#1171 "recally" union is not here,
// because #1171 retired it, not this change.
var retiredToolNames = []string{
	"goal", "scheduler", "workflow", "vault", "oauth", "share", "email",
	"recally_get_article", "recally_list_articles", "recally_save_article", "recally_digest",
}

// A tool_override row is keyed by name, so a row naming a retired tool matches
// nothing after the split: it hides neither the old capability nor any of the
// new ones, and it waits there for a future tool to reuse the name and inherit
// the setting. Stella is pre-production, so the rows go rather than being
// migrated forward — the capability falls back to its default visibility.
//
// The assertion that matters is the second one: deleting by name must not reach
// a name this change did not retire.
func TestSplitToolOverridesDeletesOnlyTheRetiredNames(t *testing.T) {
	db := newTestDB(t)
	provider, closeProvider := reflectWatermarkProvider(t, db)
	defer closeProvider()
	ctx := context.Background()

	if _, err := provider.DownTo(ctx, splitToolOverridesBeforeMigration); err != nil {
		t.Fatalf("restore pre-split schema: %v", err)
	}
	for _, name := range retiredToolNames {
		seedToolOverride(t, db, name, false)
	}
	// Two survivors: a name this change never touched, and one of the new names
	// an operator could already have written a row for.
	seedToolOverride(t, db, "memory", false)
	seedToolOverride(t, db, "goal_list", true)

	if _, err := provider.UpTo(ctx, splitToolOverridesMigration); err != nil {
		t.Fatalf("migrate tool overrides: %v", err)
	}

	assertExactSystemOverrides(t, db, map[string]bool{"memory": false, "goal_list": true})
}

const (
	settingsToolRenameBeforeMigration = 90000000000031
	settingsToolRenameMigration       = 90000000000032
)

var settingsToolNamesRetiredByRename = []string{
	"agent_list", "agent_get", "agent_create", "agent_update", "agent_delete",
	"agent_tool_list", "agent_tool_update", "agent_tool_delete",
	"library_file_list", "library_file_get", "library_file_upload", "library_file_delete",
	"skill_list", "skill_get", "skill_create", "skill_update", "skill_delete",
	"provider_list", "provider_get", "provider_create", "provider_update", "provider_delete",
	"default_model_get", "default_model_update",
	"embedding_setting_get", "embedding_setting_update",
	"plugin_list", "plugin_enable", "plugin_disable",
	"mcp_server_list", "mcp_server_get", "mcp_server_create", "mcp_server_update", "mcp_server_delete",
}

func TestSettingsToolRenameDeletesOnlyRetiredNames(t *testing.T) {
	db := newTestDB(t)
	provider, closeProvider := reflectWatermarkProvider(t, db)
	defer closeProvider()
	ctx := context.Background()

	if _, err := provider.DownTo(ctx, settingsToolRenameBeforeMigration); err != nil {
		t.Fatalf("restore pre-settings-rename schema: %v", err)
	}
	if got := len(settingsToolNamesRetiredByRename); got != 34 {
		t.Fatalf("retired Settings tool inventory = %d, want 34", got)
	}
	for _, name := range settingsToolNamesRetiredByRename {
		seedToolOverride(t, db, name, false)
	}
	seedToolOverride(t, db, "memory", false)
	seedToolOverride(t, db, "settings_agent_list", true)

	if _, err := provider.UpTo(ctx, settingsToolRenameMigration); err != nil {
		t.Fatalf("migrate settings tool names: %v", err)
	}

	assertExactSystemOverrides(t, db, map[string]bool{"memory": false, "settings_agent_list": true})
}

func seedToolOverride(t *testing.T, db *pgxpool.Pool, tool string, enabled bool) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `
		INSERT INTO tool_override (tool_name, scope, enabled) VALUES ($1, 'system', $2)
	`, tool, enabled); err != nil {
		t.Fatalf("seed tool override %q: %v", tool, err)
	}
}

// assertExactSystemOverrides compares the whole system-scoped table, not just
// the names the caller thought to name. A row that should have gone is as much
// a bug as one that should have stayed.
func assertExactSystemOverrides(t *testing.T, db *pgxpool.Pool, want map[string]bool) {
	t.Helper()
	rows, err := db.Query(context.Background(), `
		SELECT tool_name, enabled FROM tool_override WHERE scope = 'system'
	`)
	if err != nil {
		t.Fatalf("read system overrides: %v", err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var name string
		var enabled bool
		if err := rows.Scan(&name, &enabled); err != nil {
			t.Fatalf("scan system override: %v", err)
		}
		got[name] = enabled
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read system overrides: %v", err)
	}
	if !maps.Equal(got, want) {
		t.Errorf("system tool_override rows = %v, want %v", sortedPairs(got), sortedPairs(want))
	}
}

func sortedPairs(rows map[string]bool) []string {
	out := make([]string, 0, len(rows))
	for name, enabled := range rows {
		out = append(out, fmt.Sprintf("%s=%v", name, enabled))
	}
	sort.Strings(out)
	return out
}
