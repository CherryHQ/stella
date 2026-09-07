package db

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const pluginRetirementMigration42 = int64(90000000000042)

var retiredBuiltinTools = []struct {
	id   string
	name string
}{
	{id: "tool/mise", name: "mise"},
	{id: "tool/xberg", name: "xberg"},
	{id: "tool/fd", name: "fd"},
	{id: "tool/rg", name: "rg"},
}

func TestRetireLegacyBuiltinToolsMigration(t *testing.T) {
	db, provider := newTestDBAtMigration(t, pluginCutoverMigration41)
	ctx := t.Context()
	seedRetiredBuiltinFixture(t, db)

	if _, err := provider.UpTo(ctx, pluginRetirementMigration42); err != nil {
		t.Fatalf("apply builtin retirement migration: %v", err)
	}

	for _, tool := range retiredBuiltinTools {
		for table, query := range map[string]string{
			"plugin":          `SELECT count(*) FROM plugin WHERE id = $1`,
			"plugin_override": `SELECT count(*) FROM plugin_override WHERE plugin_id = $1`,
			"plugin_state":    `SELECT count(*) FROM plugin_state WHERE plugin_id = $1`,
		} {
			var count int
			if err := db.QueryRow(ctx, query, tool.id).Scan(&count); err != nil {
				t.Fatalf("count retired %s in %s: %v", tool.id, table, err)
			}
			if count != 0 {
				t.Errorf("retired %s rows in %s = %d, want 0", tool.id, table, count)
			}
		}
	}

	var kept int
	if err := db.QueryRow(ctx, `
		SELECT count(*) FROM plugin
		WHERE id = 'tool/keep'
	`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 1 {
		t.Fatalf("unrelated legacy plugin rows = %d, want 1", kept)
	}
	for table, query := range map[string]string{
		"plugin_override": `SELECT count(*) FROM plugin_override WHERE plugin_id = 'tool/keep'`,
		"plugin_state":    `SELECT count(*) FROM plugin_state WHERE plugin_id = 'tool/keep'`,
	} {
		if err := db.QueryRow(ctx, query).Scan(&kept); err != nil {
			t.Fatalf("count unrelated %s: %v", table, err)
		}
		if kept != 1 {
			t.Fatalf("unrelated %s rows = %d, want 1", table, kept)
		}
	}
	var latest int64
	if err := db.QueryRow(ctx, `SELECT version_id FROM goose_db_version ORDER BY id DESC LIMIT 1`).Scan(&latest); err != nil {
		t.Fatal(err)
	}
	if latest != pluginRetirementMigration42 {
		t.Fatalf("latest migration = %d, want 42", latest)
	}
}

func TestRetireLegacyBuiltinToolsMigrationFreshDatabase(t *testing.T) {
	db, provider := newTestDBAtMigration(t, pluginCutoverMigration40)
	ctx := t.Context()
	if _, err := provider.UpTo(ctx, pluginRetirementMigration42); err != nil {
		t.Fatalf("apply migrations 41-42 on fresh historical database: %v", err)
	}

	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM plugin WHERE id = ANY($1::text[])`, retiredBuiltinIDs()).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("fresh database has retired legacy plugins = %d, want 0", count)
	}
	if err := db.QueryRow(ctx, `SELECT version_id FROM goose_db_version ORDER BY id DESC LIMIT 1`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if int64(count) != pluginRetirementMigration42 {
		t.Fatalf("fresh latest migration = %d, want 42", count)
	}
}

func TestRetireLegacyBuiltinToolsMigrationGuardsRollback(t *testing.T) {
	cases := []struct {
		name string
		seed func(*testing.T, *pgxpool.Pool)
	}{
		{
			name: "manifest vault locator",
			seed: func(t *testing.T, db *pgxpool.Pool) {
				_, err := db.Exec(t.Context(), `UPDATE plugin_override SET session_env_vault_key = 'legacy-vault-key' WHERE plugin_id = 'tool/mise'`)
				if err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "manifest invalid JSON",
			seed: func(t *testing.T, db *pgxpool.Pool) {
				_, err := db.Exec(t.Context(), `UPDATE plugin_override SET config = '{invalid-json}' WHERE plugin_id = 'tool/mise'`)
				if err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "wrong legacy shape",
			seed: func(t *testing.T, db *pgxpool.Pool) {
				_, err := db.Exec(t.Context(), `UPDATE plugin SET kind = 'provider' WHERE id = 'tool/mise'`)
				if err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, provider := newTestDBAtMigration(t, pluginCutoverMigration41)
			ctx := t.Context()
			seedRetiredBuiltinFixture(t, db)
			tc.seed(t, db)

			if _, err := provider.UpTo(ctx, pluginRetirementMigration42); err == nil {
				t.Fatal("retirement migration unexpectedly succeeded")
			}
			var version int64
			var applied bool
			if err := db.QueryRow(ctx, `SELECT version_id, is_applied FROM goose_db_version ORDER BY id DESC LIMIT 1`).Scan(&version, &applied); err != nil {
				t.Fatal(err)
			}
			if version != pluginCutoverMigration41 || !applied {
				t.Fatalf("migration ledger = %d applied=%v, want 41/applied", version, applied)
			}
			var count int
			if err := db.QueryRow(ctx, `SELECT count(*) FROM plugin WHERE id = 'tool/mise'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("rollback lost legacy target plugin, rows = %d", count)
			}
		})
	}
}

func seedRetiredBuiltinFixture(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	ctx := t.Context()
	for _, tool := range retiredBuiltinTools {
		if _, err := db.Exec(ctx, `
			INSERT INTO plugin (id, kind, name, enabled, config)
			VALUES ($1, 'tool', $2, true, '{}'::jsonb)
		`, tool.id, tool.name); err != nil {
			t.Fatalf("seed legacy plugin %s: %v", tool.id, err)
		}
		if _, err := db.Exec(ctx, `
			INSERT INTO plugin_override (plugin_id, enabled, config)
			VALUES ($1, false, '{"$sparse":true}'::text)
		`, tool.id); err != nil {
			t.Fatalf("seed legacy override %s: %v", tool.id, err)
		}
		if _, err := db.Exec(ctx, `
			INSERT INTO plugin_state (plugin_id, scope_kind, scope_id, state_key, value)
			VALUES ($1, 'system', '', 'retired-state', '{}'::jsonb)
		`, tool.id); err != nil {
			t.Fatalf("seed legacy state %s: %v", tool.id, err)
		}
	}

	if _, err := db.Exec(ctx, `INSERT INTO plugin (id, kind, name, enabled, config) VALUES ('tool/keep', 'tool', 'keep', true, '{}'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO plugin_override (plugin_id, enabled, config) VALUES ('tool/keep', true, '{"keep":true}'::text)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO plugin_state (plugin_id, scope_kind, scope_id, state_key, value) VALUES ('tool/keep', 'system', '', 'keep', '{}'::jsonb)`); err != nil {
		t.Fatal(err)
	}
}

func retiredBuiltinIDs() []string {
	ids := make([]string, 0, len(retiredBuiltinTools))
	for _, tool := range retiredBuiltinTools {
		ids = append(ids, tool.id)
	}
	return ids
}
