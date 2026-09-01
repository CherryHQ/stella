package db

import (
	"context"
	"maps"
	"testing"
)

const (
	defaultStellaSettingsBeforeMigration = 90000000000031
	defaultStellaSettingsMigration       = 90000000000032
)

func TestDefaultStellaSettingsMigrationEnablesOnlyReservedAgent(t *testing.T) {
	db := newTestDB(t)
	provider, closeProvider := reflectWatermarkProvider(t, db)
	defer closeProvider()
	ctx := context.Background()

	if _, err := provider.DownTo(ctx, defaultStellaSettingsBeforeMigration); err != nil {
		t.Fatalf("restore pre-default-Stella-settings schema: %v", err)
	}
	for _, id := range []string{"stella", "custom"} {
		if _, err := db.Exec(ctx, `
			INSERT INTO agent (id, name, workspace, system_settings_tools_enabled)
			VALUES ($1, $1, '/tmp/' || $1, false)
		`, id); err != nil {
			t.Fatalf("seed Agent %q: %v", id, err)
		}
	}

	if _, err := provider.UpTo(ctx, defaultStellaSettingsMigration); err != nil {
		t.Fatalf("enable built-in Stella Settings tools: %v", err)
	}

	rows, err := db.Query(ctx, `
		SELECT id, system_settings_tools_enabled FROM agent ORDER BY id
	`)
	if err != nil {
		t.Fatalf("read Agent Settings policies: %v", err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var id string
		var enabled bool
		if err := rows.Scan(&id, &enabled); err != nil {
			t.Fatalf("scan Agent Settings policy: %v", err)
		}
		got[id] = enabled
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read Agent Settings policies: %v", err)
	}
	want := map[string]bool{"custom": false, "stella": true}
	if !maps.Equal(got, want) {
		t.Errorf("Agent Settings policies = %v, want %v", got, want)
	}
}
