package db

import (
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/plugin"
)

func TestPreviewLegacyImportIsReadOnlyAndDoesNotWriteMarker(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()
	if _, err := db.Exec(ctx, `
		INSERT INTO plugin (id, kind, name, enabled, config)
		VALUES ('tool/test', 'tool', 'test', false, '{}'::jsonb)
	`); err != nil {
		t.Fatal(err)
	}
	catalog := plugin.NewCatalog()
	if err := catalog.Register(plugin.Definition{
		ID: "tool/test", Namespace: "test", DisplayName: "Test", Backend: plugin.BackendCLI,
		Source: plugin.SourceBuiltin, ImplementationKey: "tool/test", Spec: []byte(`{"name":"test"}`), DefaultEnabled: true, Revision: 1,
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := plugin.PreviewLegacyImport(ctx, db, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Configs) != 1 || plan.Configs[0].Enabled == nil || *plan.Configs[0].Enabled {
		t.Fatalf("normalized legacy config = %#v", plan.Configs)
	}
	var definitions, configs, markers int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM plugin_definition`).Scan(&definitions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM plugin_config`).Scan(&configs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app_setting WHERE key = 'plugin_cutover_v1'`).Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if definitions != 0 || configs != 0 || markers != 0 {
		t.Fatalf("preview wrote target state: definitions=%d configs=%d markers=%d", definitions, configs, markers)
	}
}

func TestPreviewLegacyImportRejectsUnexpectedMarkerAndMCPOverride(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()
	catalog := plugin.NewCatalog()
	if _, err := db.Exec(ctx, `INSERT INTO app_setting (key, value) VALUES ('plugin_cutover_v1', 'future')`); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.PreviewLegacyImport(ctx, db, catalog); !errors.Is(err, plugin.ErrLegacyMigrationConflict) {
		t.Fatalf("unexpected marker error = %v", err)
	}

	if _, err := db.Exec(ctx, `DELETE FROM app_setting WHERE key = 'plugin_cutover_v1'`); err != nil {
		t.Fatal(err)
	}
	const registrationID = "0198f9a4-1b2c-7def-8123-456789abcdef"
	if _, err := db.Exec(ctx, `
		INSERT INTO mcp_server (id, scope, name, url, transport, auth_type, enabled, metadata, tools, credential_mode)
		VALUES ($1, 'system', 'github', 'https://mcp.example.test', 'sse', 'none', true, '{}'::jsonb, '[{"name":"create_issue"}]'::jsonb, 'shared')
	`, registrationID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO tool_override (tool_name, scope, enabled)
		VALUES ('mcp__github__create_issue', 'system', false)
	`); err != nil {
		t.Fatal(err)
	}
	_, err := plugin.PreviewLegacyImport(ctx, db, catalog)
	if !errors.Is(err, plugin.ErrToolOverrideSchema) {
		t.Fatalf("MCP override error = %v", err)
	}
	var definitions, configs int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM plugin_definition`).Scan(&definitions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM plugin_config`).Scan(&configs); err != nil {
		t.Fatal(err)
	}
	if definitions != 0 || configs != 0 {
		t.Fatalf("failed preview wrote target state: definitions=%d configs=%d", definitions, configs)
	}
}
