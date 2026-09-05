package db

import (
	"encoding/json"
	"io/fs"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestManifestPluginOwnershipMigrationMatrix(t *testing.T) {
	tests := []struct {
		name string
		rows []ownershipMigrationSeed
		want map[string]ownershipMigrationWant
	}{
		{
			name: "legacy and override conflicts plus legacy only",
			rows: []ownershipMigrationSeed{
				{ID: "tool/gh", Present: true, Enabled: false, Config: `{"legacy":"gh"}`, Override: &ownershipMigrationOverride{Enabled: boolPtr(true), Vault: "vault-gh", Config: `{"override":"gh"}`}},
				{ID: "tool/lark-cli", Present: true, Enabled: true, Config: `{"legacy":"lark"}`, Override: &ownershipMigrationOverride{Enabled: boolPtr(false), Vault: "vault-lark", Config: `{"override":"lark"}`}},
				{ID: "tool/mise", Present: true, Enabled: true, Config: `{"legacy":"mise"}`},
			},
			want: map[string]ownershipMigrationWant{
				"tool/gh":       {Legacy: &ownershipMigrationLegacy{Enabled: false, Config: `{"legacy":"gh"}`}, Override: &ownershipMigrationOverrideWant{Enabled: boolPtr(false), Vault: "vault-gh", Config: `{"override":"gh"}`}},
				"tool/lark-cli": {Legacy: &ownershipMigrationLegacy{Enabled: true, Config: `{"legacy":"lark"}`}, Override: &ownershipMigrationOverrideWant{Enabled: boolPtr(false), Vault: "vault-lark", Config: `{"override":"lark"}`}},
				"tool/mise":     {Legacy: &ownershipMigrationLegacy{Enabled: true, Config: `{"legacy":"mise"}`}, Override: &ownershipMigrationOverrideWant{Enabled: boolPtr(true)}},
			},
		},
		{
			name: "absent rows and existing override",
			rows: []ownershipMigrationSeed{
				{ID: "tool/gh", Override: &ownershipMigrationOverride{Enabled: boolPtr(false), Vault: "vault-gh", Config: `{"override":"gh"}`}},
				{ID: "tool/mise", Present: true, Enabled: true, Config: `{"legacy":"mise"}`, Override: &ownershipMigrationOverride{Vault: "vault-mise", Config: `{"override":"mise"}`}},
			},
			want: map[string]ownershipMigrationWant{
				"tool/gh":       {Override: &ownershipMigrationOverrideWant{Enabled: boolPtr(false), Vault: "vault-gh", Config: `{"override":"gh"}`}},
				"tool/lark-cli": {},
				"tool/mise":     {Legacy: &ownershipMigrationLegacy{Enabled: true, Config: `{"legacy":"mise"}`}, Override: &ownershipMigrationOverrideWant{Enabled: boolPtr(true), Vault: "vault-mise", Config: `{"override":"mise"}`}},
			},
		},
		{
			name: "legacy disabled without override",
			rows: []ownershipMigrationSeed{{ID: "tool/gh", Present: true, Enabled: false, Config: `{"legacy":"gh"}`}},
			want: map[string]ownershipMigrationWant{
				"tool/gh": {Legacy: &ownershipMigrationLegacy{Enabled: false, Config: `{"legacy":"gh"}`}, Override: &ownershipMigrationOverrideWant{Enabled: boolPtr(false)}},
			},
		},
		{
			name: "matching enabled values",
			rows: []ownershipMigrationSeed{
				{ID: "tool/gh", Present: true, Enabled: true, Config: `{"legacy":"gh"}`, Override: &ownershipMigrationOverride{Enabled: boolPtr(true)}},
				{ID: "tool/lark-cli", Present: true, Enabled: false, Config: `{"legacy":"lark"}`, Override: &ownershipMigrationOverride{Enabled: boolPtr(false)}},
			},
			want: map[string]ownershipMigrationWant{
				"tool/gh":       {Legacy: &ownershipMigrationLegacy{Enabled: true, Config: `{"legacy":"gh"}`}, Override: &ownershipMigrationOverrideWant{Enabled: boolPtr(true)}},
				"tool/lark-cli": {Legacy: &ownershipMigrationLegacy{Enabled: false, Config: `{"legacy":"lark"}`}, Override: &ownershipMigrationOverrideWant{Enabled: boolPtr(false)}},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := newTestDB(t)
			ctx := t.Context()
			sub, err := fs.Sub(MigrationsFS, "migrations")
			if err != nil {
				t.Fatalf("open migrations fs: %v", err)
			}
			sqlDB := stdlib.OpenDBFromPool(db)
			t.Cleanup(func() { _ = sqlDB.Close() })
			provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, sub)
			if err != nil {
				t.Fatalf("create migration provider: %v", err)
			}
			if _, err := provider.DownTo(ctx, sequentialAnchor+39); err != nil {
				t.Fatalf("goose down ownership migration: %v", err)
			}
			for _, row := range test.rows {
				if row.Present {
					configJSON := row.Config
					if configJSON == "" {
						configJSON = `{}`
					}
					if _, err := db.Exec(ctx, `
						INSERT INTO plugin (id, kind, name, enabled, config)
						VALUES ($1, 'tool', split_part($1, '/', 2), $2, $3)`, row.ID, row.Enabled, configJSON); err != nil {
						t.Fatalf("seed %s legacy row: %v", row.ID, err)
					}
				}
				if row.Override == nil {
					continue
				}
				if _, err := db.Exec(ctx, `
					INSERT INTO plugin_override (plugin_id, enabled, session_env_vault_key, config)
					VALUES ($1, $2, $3, $4)`, row.ID, row.Override.Enabled, row.Override.Vault, row.Override.Config); err != nil {
					t.Fatalf("seed %s override: %v", row.ID, err)
				}
			}
			if _, err := provider.UpTo(ctx, sequentialAnchor+40); err != nil {
				t.Fatalf("goose up ownership migration: %v", err)
			}

			for id, want := range test.want {
				t.Run(id, func(t *testing.T) {
					checkOwnershipMigrationRow(t, db, id, want)
				})
			}
		})
	}
}

type ownershipMigrationSeed struct {
	ID       string
	Present  bool
	Enabled  bool
	Config   string
	Override *ownershipMigrationOverride
}

type ownershipMigrationOverride struct {
	Enabled *bool
	Vault   string
	Config  string
}

type ownershipMigrationWant struct {
	Legacy   *ownershipMigrationLegacy
	Override *ownershipMigrationOverrideWant
}

type ownershipMigrationLegacy struct {
	Enabled bool
	Config  string
}

type ownershipMigrationOverrideWant struct {
	Enabled *bool
	Vault   string
	Config  string
}

func checkOwnershipMigrationRow(t *testing.T, db *pgxpool.Pool, id string, want ownershipMigrationWant) {
	t.Helper()
	if want.Legacy == nil {
		var count int
		if err := db.QueryRow(t.Context(), `SELECT count(*) FROM plugin WHERE id = $1`, id).Scan(&count); err != nil {
			t.Fatalf("count legacy row: %v", err)
		}
		if count != 0 {
			t.Fatalf("legacy row count = %d, want absent", count)
		}
	} else {
		var enabled bool
		var raw string
		if err := db.QueryRow(t.Context(), `SELECT enabled, config::text FROM plugin WHERE id = $1`, id).Scan(&enabled, &raw); err != nil {
			t.Fatalf("read legacy row: %v", err)
		}
		if enabled != want.Legacy.Enabled {
			t.Fatalf("legacy enabled = %v, want %v", enabled, want.Legacy.Enabled)
		}
		assertJSONEqual(t, raw, want.Legacy.Config)
	}

	if want.Override == nil {
		var count int
		if err := db.QueryRow(t.Context(), `SELECT count(*) FROM plugin_override WHERE plugin_id = $1`, id).Scan(&count); err != nil {
			t.Fatalf("count override row: %v", err)
		}
		if count != 0 {
			t.Fatalf("override row count = %d, want absent", count)
		}
		return
	}
	var enabled *bool
	var vault, raw string
	if err := db.QueryRow(t.Context(), `
		SELECT enabled, session_env_vault_key, config
		FROM plugin_override WHERE plugin_id = $1`, id).Scan(&enabled, &vault, &raw); err != nil {
		t.Fatalf("read override row: %v", err)
	}
	if (enabled == nil) != (want.Override.Enabled == nil) || enabled != nil && *enabled != *want.Override.Enabled {
		t.Fatalf("override enabled = %v, want %v", enabled, want.Override.Enabled)
	}
	if vault != want.Override.Vault {
		t.Fatalf("override vault = %q, want %q", vault, want.Override.Vault)
	}
	if want.Override.Config != "" && raw != want.Override.Config {
		t.Fatalf("override config = %q, want %q", raw, want.Override.Config)
	}
	if want.Override.Config == "" && raw != "" {
		t.Fatalf("override config = %q, want empty", raw)
	}
}

func assertJSONEqual(t *testing.T, got, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal([]byte(got), &gotValue); err != nil {
		t.Fatalf("decode got JSON %q: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("decode want JSON %q: %v", want, err)
	}
	if gotJSON, wantJSON := string(mustJSON(gotValue)), string(mustJSON(wantValue)); gotJSON != wantJSON {
		t.Fatalf("JSON = %s, want %s", gotJSON, wantJSON)
	}
}

func mustJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func boolPtr(value bool) *bool { return &value }
