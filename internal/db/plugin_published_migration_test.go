package db

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/plugin"
)

func TestMigratePublishedStatePreservesIDsAndIsRepeatable(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()
	const configID = "0198f9a4-1b2c-7def-8123-456789abcdef"
	if _, err := db.Exec(ctx, `
		INSERT INTO plugin_definition (id, display_name, source, spec, default_enabled, revision)
		VALUES ('legacy-remote', 'Legacy remote', 'custom',
			'{"origin":"remote_mcp"}'::jsonb,
			false, 7)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO plugin_config (id, plugin_id, scope, enabled, config, credential_refs, revision)
		VALUES ($1, 'legacy-remote', 'system', true,
			'{"url":"https://mcp.example.test","transport":"sse","auth_type":"none"}'::jsonb,
			'{}'::jsonb, 4)
	`, configID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO plugin_config_mcp_server (id, config_id, server_key)
		VALUES ($1::uuid, $1::uuid, 'main')
	`, configID); err != nil {
		t.Fatal(err)
	}

	if err := plugin.MigratePublishedState(ctx, db, plugin.NewCatalog()); err != nil {
		t.Fatal(err)
	}
	var spec json.RawMessage
	if err := db.QueryRow(ctx, `SELECT spec FROM plugin_definition WHERE id='legacy-remote'`).Scan(&spec); err != nil {
		t.Fatal(err)
	}
	if err := plugin.ValidateDefinitionSpecDigest(spec); err != nil {
		t.Fatalf("migrated definition digest: %v", err)
	}
	var gotConfig json.RawMessage
	if err := db.QueryRow(ctx, `SELECT config FROM plugin_config WHERE id=$1`, configID).Scan(&gotConfig); err != nil {
		t.Fatal(err)
	}
	var configObject map[string]map[string]map[string]any
	if err := json.Unmarshal(gotConfig, &configObject); err != nil {
		t.Fatal(err)
	}
	if configObject["mcp_servers"]["main"]["url"] != "https://mcp.example.test" {
		t.Fatalf("migrated config = %s", gotConfig)
	}
	if _, ok := configObject["url"]; ok {
		t.Fatalf("flat URL survived migration: %s", gotConfig)
	}
	var refs json.RawMessage
	if err := db.QueryRow(ctx, `SELECT credential_refs FROM plugin_config WHERE id=$1`, configID).Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(refs, []byte(`{"mcp_servers":{"main":{}}}`)) {
		t.Fatalf("migrated refs = %s", refs)
	}
	var childID string
	if err := db.QueryRow(ctx, `SELECT id::text FROM plugin_config_mcp_server WHERE config_id=$1`, configID).Scan(&childID); err != nil {
		t.Fatal(err)
	}
	if childID != configID {
		t.Fatalf("migration did not preserve child identity: %s", childID)
	}
	var configRevision int64
	if err := db.QueryRow(ctx, `SELECT revision FROM plugin_config WHERE id=$1`, configID).Scan(&configRevision); err != nil {
		t.Fatal(err)
	}
	if configRevision != 4 {
		t.Fatalf("config revision = %d, want 4", configRevision)
	}
	var revision int64
	if err := db.QueryRow(ctx, `SELECT revision FROM plugin_definition WHERE id='legacy-remote'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 7 {
		t.Fatalf("definition revision = %d, want 7", revision)
	}
	if err := plugin.MigratePublishedState(ctx, db, plugin.NewCatalog()); !errors.Is(err, plugin.ErrImportComplete) {
		t.Fatalf("repeat migration error = %v, want ErrImportComplete", err)
	}
}

func TestMigratePublishedStateRollsBackBeforeMarkerAndCanRetry(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()
	const configID = "0198f9a4-1b2c-7def-8123-456789abcdee"
	if _, err := db.Exec(ctx, `
		INSERT INTO plugin_definition (id, display_name, source, spec, default_enabled, revision)
		VALUES ('retry-remote', 'Retry remote', 'custom',
			'{"origin":"remote_mcp","mcp_servers":{"main":{"url":"https://mcp.example.test","transport":"sse"}}}'::jsonb,
			false, 2)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO plugin_config (id, plugin_id, scope, enabled, config, revision)
		VALUES ($1, 'retry-remote', 'system', true,
			'{"mcp_servers":{"missing":{"url":"https://mcp.example.test"}}}'::jsonb, 1)
	`, configID); err != nil {
		t.Fatal(err)
	}
	if err := plugin.MigratePublishedState(ctx, db, plugin.NewCatalog()); err == nil {
		t.Fatal("invalid child selection unexpectedly migrated")
	}
	var markers int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM app_setting WHERE key='plugin_published_v1'`).Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if markers != 0 {
		t.Fatalf("failed migration wrote marker: %d", markers)
	}
	if _, err := db.Exec(ctx, `UPDATE plugin_config SET config='{"mcp_servers":{"main":{"url":"https://mcp.example.test"}}}'::jsonb WHERE id=$1`, configID); err != nil {
		t.Fatal(err)
	}
	if err := plugin.MigratePublishedState(ctx, db, plugin.NewCatalog()); err != nil {
		t.Fatal(err)
	}
}

func TestMigratePublishedStateUnifiesMCPKeysWithoutCrossConfigChildren(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()
	const userID = "0198f9a4-1b2c-7def-8123-456789abcdea"
	const systemID = "0198f9a4-1b2c-7def-8123-456789abcdeb"
	const userConfigID = "0198f9a4-1b2c-7def-8123-456789abcdec"
	if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, 'published-migration@test')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO plugin_definition (id, display_name, source, spec, default_enabled, revision)
		VALUES ('mixed-remote', 'Mixed remote', 'custom',
			'{"binaries":[{"name":"tool","tool":"uv","version":"1"}],"oauth_provider":"feishu","session_env":[{"env_var":"FEISHU_TOKEN","source":"oauth.access_token","required":true}]}'::jsonb,
			false, 9)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO plugin_config (id, plugin_id, scope, user_id, enabled, config, credential_refs, revision)
		VALUES ($1, 'mixed-remote', 'system', NULL, true,
			'{"binaries":[{"name":"tool","version":"2"}],"url":"https://main.example","transport":"sse","auth_type":"bearer","description":"main endpoint"}'::jsonb,
			'{"bearer":{"name":"MCP_TOKEN_0198F9A4_1B2C_7DEF_8123_456789ABCDEB","scope":"system","user_id":"","agent_id":""}}'::jsonb, 6),
		($2, 'mixed-remote', 'user', $3::uuid, true,
			'{"mcp_servers":{"search":{"url":"https://search.example","transport":"sse","auth_type":"none"}}}'::jsonb,
			'{"mcp_servers":{"search":{}}}'::jsonb, 8)
	`, systemID, userConfigID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO plugin_config_mcp_server (id, config_id, server_key)
		VALUES ($1::uuid, $1::uuid, 'main'), ('0198f9a4-1b2c-7def-8123-456789abcded', $2::uuid, 'search')
	`, systemID, userConfigID); err != nil {
		t.Fatal(err)
	}

	if err := plugin.MigratePublishedState(ctx, db, plugin.NewCatalog()); err != nil {
		t.Fatal(err)
	}
	var spec json.RawMessage
	if err := db.QueryRow(ctx, `SELECT spec FROM plugin_definition WHERE id='mixed-remote'`).Scan(&spec); err != nil {
		t.Fatal(err)
	}
	var specObject map[string]json.RawMessage
	if err := json.Unmarshal(spec, &specObject); err != nil {
		t.Fatal(err)
	}
	var specServers map[string]json.RawMessage
	if err := json.Unmarshal(specObject["mcp_servers"], &specServers); err != nil {
		t.Fatal(err)
	}
	if len(specServers) != 2 {
		t.Fatalf("canonical MCP keys = %#v", specServers)
	}
	definitionPayload, err := plugin.DecodeResourcePayload(spec, "mixed-remote")
	if err != nil {
		t.Fatalf("canonical OAuth declaration: %v", err)
	}
	if len(definitionPayload.OAuth) != 1 || definitionPayload.OAuth[0].Provider != "feishu" || len(definitionPayload.OAuth[0].Bindings) != 1 || definitionPayload.OAuth[0].Bindings[0].Credential != "access_token" || definitionPayload.OAuth[0].Bindings[0].EnvVar != "FEISHU_TOKEN" {
		t.Fatalf("canonical OAuth declaration = %#v", definitionPayload.OAuth)
	}
	var systemConfig, userConfig, systemRefs json.RawMessage
	if err := db.QueryRow(ctx, `SELECT config, credential_refs FROM plugin_config WHERE id=$1`, systemID).Scan(&systemConfig, &systemRefs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT config FROM plugin_config WHERE id=$1`, userConfigID).Scan(&userConfig); err != nil {
		t.Fatal(err)
	}
	var systemObject, userObject map[string]map[string]json.RawMessage
	if err := json.Unmarshal(systemConfig, &systemObject); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(userConfig, &userObject); err != nil {
		t.Fatal(err)
	}
	if len(systemObject["mcp_servers"]) != 1 || len(userObject["mcp_servers"]) != 1 {
		t.Fatalf("cross-config MCP keys: system=%s user=%s", systemConfig, userConfig)
	}
	if _, ok := systemObject["mcp_servers"]["main"]; !ok {
		t.Fatalf("system MCP config = %s", systemConfig)
	}
	var mainServer map[string]any
	if err := json.Unmarshal(systemObject["mcp_servers"]["main"], &mainServer); err != nil {
		t.Fatal(err)
	}
	if mainServer["description"] != "main endpoint" {
		t.Fatalf("system MCP description = %#v", mainServer["description"])
	}
	var binaryPin map[string]any
	if err := json.Unmarshal(systemObject["binaries"]["tool"], &binaryPin); err != nil || binaryPin["version"] != "2" {
		t.Fatalf("binary pin = %#v, err=%v", binaryPin, err)
	}
	if _, ok := userObject["mcp_servers"]["search"]; !ok {
		t.Fatalf("user MCP config = %s", userConfig)
	}
	if !jsonEqual(systemRefs, []byte(`{"mcp_servers":{"main":{"bearer":{"name":"MCP_TOKEN_0198F9A4_1B2C_7DEF_8123_456789ABCDEB","scope":"system","user_id":"","agent_id":""}}}}`)) {
		t.Fatalf("system refs = %s", systemRefs)
	}
	var childCount int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM plugin_config_mcp_server WHERE config_id IN ($1::uuid,$2::uuid)`, systemID, userConfigID).Scan(&childCount); err != nil {
		t.Fatal(err)
	}
	if childCount != 2 {
		t.Fatalf("child count = %d, want 2", childCount)
	}
	var systemRevision, userRevision int64
	if err := db.QueryRow(ctx, `SELECT revision FROM plugin_config WHERE id=$1`, systemID).Scan(&systemRevision); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT revision FROM plugin_config WHERE id=$1`, userConfigID).Scan(&userRevision); err != nil {
		t.Fatal(err)
	}
	if systemRevision != 6 || userRevision != 8 {
		t.Fatalf("config revisions = %d/%d", systemRevision, userRevision)
	}
}
