package db

import (
	"testing"

	"github.com/CherryHQ/stella/internal/plugin"
)

func TestNativeImportPreservesNativeRows(t *testing.T) {
	for _, globalEnabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[globalEnabled], func(t *testing.T) {
			database := newTestDBAtMigrationOnly(t, pluginCutoverMigration41)
			ctx := t.Context()
			preparePluginPolicyCutoverSchema(t, database)

			if _, err := database.Exec(ctx, `
				INSERT INTO agent(id, name, workspace)
				VALUES ('native-agent-a', 'Native Agent A', ''), ('native-agent-b', 'Native Agent B', '')
			`); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(ctx, `
				INSERT INTO channel(id, name, type, agent_id, enabled, config)
				VALUES
					('native-channel-a', 'Native A', 'feishu', 'native-agent-a', true,
						'{"app_id":"fixture-a","app_secret":"fixture-secret-a"}'),
					('native-channel-b', 'Native B', 'feishu', 'native-agent-b', false,
						'{"app_id":"fixture-b","app_secret":"fixture-secret-b"}')
			`); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(ctx, `
				INSERT INTO plugin(id, kind, name, enabled, config)
				VALUES ('channel/feishu', 'channel', 'feishu', $1,
					'{"app_id":"mirror","app_secret":"mirror-secret"}'::jsonb)
			`, globalEnabled); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(ctx, `
				INSERT INTO plugin_override(plugin_id, enabled, session_env_vault_key, config)
				VALUES ('channel/feishu', false, 'native-vault-key', '{"source":"legacy"}')
			`); err != nil {
				t.Fatal(err)
			}

			snapshot := func(table, orderBy string) string {
				t.Helper()
				var value string
				query := `SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY row.` + orderBy + `), '[]'::jsonb)::text FROM (SELECT * FROM ` + table + `) AS row`
				if err := database.QueryRow(ctx, query).Scan(&value); err != nil {
					t.Fatalf("snapshot %s: %v", table, err)
				}
				return value
			}
			beforeChannels := snapshot("channel", "id")
			beforePlugins := snapshot("plugin", "id")
			beforeOverrides := snapshot("plugin_override", "plugin_id")

			if err := plugin.ImportLegacyState(ctx, database, plugin.NewCatalog(), plugin.NativeRegistryMap{"channel/feishu": false}, nil); err != nil {
				t.Fatalf("import native state: %v", err)
			}

			if after := snapshot("channel", "id"); after != beforeChannels {
				t.Fatalf("channel rows changed: before=%s after=%s", beforeChannels, after)
			}
			if after := snapshot("plugin", "id"); after != beforePlugins {
				t.Fatalf("plugin rows changed: before=%s after=%s", beforePlugins, after)
			}
			if after := snapshot("plugin_override", "plugin_id"); after != beforeOverrides {
				t.Fatalf("plugin_override rows changed: before=%s after=%s", beforeOverrides, after)
			}

			var catalogRows int
			if err := database.QueryRow(ctx, `
				SELECT count(*) FROM plugin_definition WHERE id = 'channel/feishu'
			`).Scan(&catalogRows); err != nil {
				t.Fatal(err)
			}
			if catalogRows != 0 {
				t.Fatalf("native plugin entered Agent definitions: %d rows", catalogRows)
			}
			if err := database.QueryRow(ctx, `
				SELECT count(*) FROM plugin_config WHERE plugin_id = 'channel/feishu'
			`).Scan(&catalogRows); err != nil {
				t.Fatal(err)
			}
			if catalogRows != 0 {
				t.Fatalf("native plugin entered Agent configs: %d rows", catalogRows)
			}
		})
	}
}
