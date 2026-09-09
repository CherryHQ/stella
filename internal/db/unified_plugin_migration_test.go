package db

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/plugin"
)

func pluginDefinition(id string, enabled bool) plugin.Definition {
	return plugin.Definition{
		ID: id, DisplayName: id,
		Source:         plugin.SourceBuiltin,
		Spec:           publishedPluginSpec(`{"schema":1}`),
		DefaultEnabled: enabled, Revision: 1,
	}
}

func insertPluginUser(t *testing.T, db *pgxpool.Pool, email string, admin bool) authz.Authority {
	t.Helper()
	var id string
	if err := db.QueryRow(t.Context(), `INSERT INTO auth_user (email) VALUES ($1) RETURNING id`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	authority, err := authz.NewUserAuthority(authz.UserID(id), admin)
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

func TestUnifiedPluginConfigConstraints(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()
	insertDefinition := func(id string) {
		t.Helper()
		if _, err := db.Exec(ctx, `
			INSERT INTO plugin_definition (id, display_name, source, spec)
			VALUES ($1, $1, 'builtin', '{}'::jsonb)
		`, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{
		"owner",
		"payload",
		"refs",
		"null",
		"shared-payload",
		"shared-negative",
	} {
		insertDefinition(id)
	}
	owner := insertPluginUser(t, db, "plugin-constraint-owner@example.test", false)

	assertConstraint := func(name, expected, statement string, args ...any) {
		t.Helper()
		_, err := db.Exec(ctx, statement, args...)
		if err == nil {
			t.Fatalf("%s: invalid row was accepted", name)
		}
		pgErr := &pgconn.PgError{}
		ok := errors.As(err, &pgErr)
		if !ok || pgErr.Code != "23514" || pgErr.ConstraintName != expected {
			t.Fatalf("%s: error = %T %v, want check %s", name, err, err, expected)
		}
	}
	assertConstraint("owner tuple", "plugin_config_scope_owner_check", `
		INSERT INTO plugin_config (plugin_id, scope, user_id, enabled, config)
		VALUES ('owner', 'system', $1, false, NULL)
	`, owner.UserID())
	assertConstraint("enabled payload", "plugin_config_negative_check", `
		INSERT INTO plugin_config (plugin_id, scope, enabled, config)
		VALUES ('payload', 'system', true, NULL)
	`)
	assertConstraint("negative refs", "plugin_config_negative_refs_check", `
		INSERT INTO plugin_config (plugin_id, scope, enabled, config, credential_refs)
		VALUES ('refs', 'system', false, NULL, '{"vault":"key"}'::jsonb)
	`)
	assertConstraint("JSON null payload", "plugin_config_config_object_check", `
		INSERT INTO plugin_config (plugin_id, scope, enabled, config)
		VALUES ('null', 'system', false, 'null'::jsonb)
	`)
	if _, err := db.Exec(ctx, `
		INSERT INTO plugin_config (plugin_id, scope, enabled, config)
		VALUES ('shared-payload', 'system', true, '{}'::jsonb)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO plugin_config (plugin_id, scope, enabled, config)
		VALUES ('shared-negative', 'system', false, NULL)
	`); err != nil {
		t.Fatalf("negative row was rejected: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO plugin_config (plugin_id, scope, enabled, config, credential_refs)
		VALUES ('refs', 'system', true, '{}'::jsonb, '{"vault":"allowed"}'::jsonb)
	`); err != nil {
		t.Fatalf("payload credential refs rejected: %v", err)
	}
}

func syncPluginCatalog(t *testing.T, db *pgxpool.Pool, definitions ...plugin.Definition) (*plugin.LegacyService, *plugin.Catalog) {
	t.Helper()
	catalog := plugin.NewCatalog()
	for _, def := range definitions {
		if err := catalog.Register(def); err != nil {
			t.Fatal(err)
		}
	}
	service := plugin.NewLegacyService(db, catalog, nil, nil)
	if err := service.SyncBuiltinDefaults(t.Context()); err != nil {
		t.Fatal(err)
	}
	return service, catalog
}

func TestUnifiedPluginFreshSyncAndDefaultPreservesState(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()
	def := pluginDefinition("fresh", true)
	service, _ := syncPluginCatalog(t, db, def)

	var definitionCreated, definitionUpdated time.Time
	if err := db.QueryRow(ctx, `SELECT created_at, updated_at FROM plugin_definition WHERE id = $1`, def.ID).Scan(&definitionCreated, &definitionUpdated); err != nil {
		t.Fatal(err)
	}
	var configID string
	var created, updated time.Time
	var enabled pgtype.Bool
	var payload, refs []byte
	var revision int64
	if err := db.QueryRow(ctx, `
		SELECT id, enabled, config, credential_refs, revision, created_at, updated_at
		FROM plugin_config WHERE plugin_id = $1 AND scope = 'system'
	`, def.ID).Scan(&configID, &enabled, &payload, &refs, &revision, &created, &updated); err != nil {
		t.Fatal(err)
	}
	if enabled.Valid || string(payload) != `{}` || string(refs) != `{}` || revision != 1 {
		t.Fatalf("fresh system projection = enabled=%v payload=%s refs=%s revision=%d", enabled, payload, refs, revision)
	}

	if _, err := db.Exec(ctx, `
		UPDATE plugin_config
		SET enabled = false, config = '{"pin":true}'::jsonb, credential_refs = '{"vault":"key"}'::jsonb, revision = 7
		WHERE id = $1
	`, configID); err != nil {
		t.Fatal(err)
	}
	if err := service.SyncBuiltinDefaults(ctx); err != nil {
		t.Fatal(err)
	}

	var gotID string
	var gotEnabled bool
	var gotPayload, gotRefs []byte
	var gotRevision int64
	var gotCreated, gotUpdated time.Time
	if err := db.QueryRow(ctx, `
		SELECT id, enabled, config, credential_refs, revision, created_at, updated_at
		FROM plugin_config WHERE plugin_id = $1 AND scope = 'system'
	`, def.ID).Scan(&gotID, &gotEnabled, &gotPayload, &gotRefs, &gotRevision, &gotCreated, &gotUpdated); err != nil {
		t.Fatal(err)
	}
	if gotID != configID || gotEnabled || !jsonEqual(gotPayload, []byte(`{"pin":true}`)) || !jsonEqual(gotRefs, []byte(`{"vault":"key"}`)) || gotRevision != 7 || !gotCreated.Equal(created) || !gotUpdated.Equal(updated) {
		t.Fatalf("sync rewrote pinned projection: id=%s enabled=%v payload=%s refs=%s revision=%d", gotID, gotEnabled, gotPayload, gotRefs, gotRevision)
	}
	var gotDefinitionCreated, gotDefinitionUpdated time.Time
	if err := db.QueryRow(ctx, `SELECT created_at, updated_at FROM plugin_definition WHERE id = $1`, def.ID).Scan(&gotDefinitionCreated, &gotDefinitionUpdated); err != nil {
		t.Fatal(err)
	}
	if !gotDefinitionCreated.Equal(definitionCreated) || !gotDefinitionUpdated.Equal(definitionUpdated) {
		t.Fatalf("repeated sync changed definition timestamps: %s/%s -> %s/%s", definitionCreated, definitionUpdated, gotDefinitionCreated, gotDefinitionUpdated)
	}
}

func TestUnifiedPluginSyncFailureRollsBackEarlierDefinitions(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()
	if _, err := db.Exec(ctx, `
		INSERT INTO plugin_definition (id, display_name, source, spec)
		VALUES ('conflict', 'old', 'custom', '{}'::jsonb)
	`); err != nil {
		t.Fatal(err)
	}
	catalog := plugin.NewCatalog()
	for _, def := range []plugin.Definition{
		pluginDefinition("good", true),
		pluginDefinition("conflict", true),
	} {
		if err := catalog.Register(def); err != nil {
			t.Fatal(err)
		}
	}
	service := plugin.NewLegacyService(db, catalog, nil, nil)
	if err := service.SyncBuiltinDefaults(ctx); err == nil {
		t.Fatal("sync accepted an incompatible existing definition")
	}
	var definitions, configs int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM plugin_definition WHERE id = 'good'`).Scan(&definitions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM plugin_config WHERE plugin_id = 'good'`).Scan(&configs); err != nil {
		t.Fatal(err)
	}
	if definitions != 0 || configs != 0 {
		t.Fatalf("failed sync left partial state: definitions=%d configs=%d", definitions, configs)
	}
}

func jsonEqual(left, right []byte) bool {
	var leftValue, rightValue any
	return json.Unmarshal(left, &leftValue) == nil && json.Unmarshal(right, &rightValue) == nil &&
		reflect.DeepEqual(leftValue, rightValue)
}
