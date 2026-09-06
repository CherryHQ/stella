package host_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/plugin"
	pluginhost "github.com/CherryHQ/stella/internal/plugin/host"
	"github.com/CherryHQ/stella/internal/plugin/manifest"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func TestSessionPluginViewRejectsIncompletePayloadAfterCapabilityLift(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	definition := plugin.Definition{
		ID: "tool/lift", Namespace: "lift", DisplayName: "Lift",
		Backend: plugin.BackendCLI, Source: plugin.SourceBuiltin,
		ImplementationKey: "tool/lift", DefaultEnabled: false, Revision: 1,
		Spec: json.RawMessage(`{"binaries":[{"name":"lift","tool":"github:owner/lift","version":"1.0.0"}]}`),
	}
	catalog := plugin.NewCatalog()
	if err := catalog.Register(definition); err != nil {
		t.Fatal(err)
	}
	insertDefinition(t, db, definition)
	insertUser(t, db, "10000000-0000-0000-0000-000000000001")
	insertConfig(t, db, "20000000-0000-0000-0000-000000000001", definition, "user", "10000000-0000-0000-0000-000000000001", false, `{"binaries":null}`)

	service := plugin.NewService(db, nil, catalog, nil, inlinePluginMutationFence)
	authority, err := authz.NewUserAuthority(authz.UserID("10000000-0000-0000-0000-000000000001"), false)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.ResolveSnapshot(ctx, authority, "")
	if err != nil {
		t.Fatalf("ResolveSnapshot disabled: %v", err)
	}
	view, err := pluginhost.New(nil).SessionPluginView(snapshot)
	if err != nil {
		t.Fatalf("SessionPluginView disabled: %v", err)
	}
	if slices.Contains(view.ExposedPluginIDs, definition.ID) {
		t.Fatalf("disabled plugin was exposed: %+v", view)
	}

	if _, err := db.Exec(ctx, `UPDATE plugin_config SET enabled = TRUE WHERE id = $1`, "20000000-0000-0000-0000-000000000001"); err != nil {
		t.Fatal(err)
	}
	snapshot, err = service.ResolveSnapshot(ctx, authority, "")
	if err != nil {
		t.Fatalf("ResolveSnapshot lifted: %v", err)
	}
	if _, err := pluginhost.New(nil).SessionPluginView(snapshot); err == nil {
		t.Fatal("capability lift exposed an incomplete CLI payload")
	}
}

func TestSessionPluginViewNamespaceWinnerHidesGoResources(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	goDefinition := plugin.Definition{
		ID: "builtin/email", Namespace: "email", DisplayName: "Email",
		Backend: plugin.BackendGo, Source: plugin.SourceBuiltin,
		ImplementationKey: "builtin/email", DefaultEnabled: true, Revision: 1,
		Spec: json.RawMessage(`{}`),
	}
	mcpDefinition := plugin.Definition{
		ID: "custom/email", Namespace: "email", DisplayName: "Email MCP",
		Backend: plugin.BackendMCP, Source: plugin.SourceCustom,
		ImplementationKey: "mcp", DefaultEnabled: false, Revision: 1,
		Spec: json.RawMessage(`{}`),
	}
	catalog := plugin.NewCatalog()
	for _, definition := range []plugin.Definition{goDefinition, mcpDefinition} {
		if err := catalog.Register(definition); err != nil {
			t.Fatal(err)
		}
		insertDefinition(t, db, definition)
	}
	userID := "10000000-0000-0000-0000-000000000002"
	insertUser(t, db, userID)
	insertConfig(t, db, "20000000-0000-0000-0000-000000000002", mcpDefinition, "user", userID, true, `{}`)

	host := pluginhost.New(nil)
	host.RegisterPluginID(goDefinition.ID)
	host.AddSessionEnv(pkgplugins.SessionEnvSpec{PluginID: goDefinition.ID, EnvVar: "EMAIL_TOKEN", Source: pkgplugins.SessionEnvSourceStatic, Value: "trusted"})
	host.AddBundledSkill(pkgplugins.BundledSkillSpec{PluginID: goDefinition.ID, Name: "email-skill"})
	service := plugin.NewService(db, nil, catalog, nil, inlinePluginMutationFence)
	authority, err := authz.NewUserAuthority(authz.UserID(userID), false)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.ResolveSnapshot(ctx, authority, "")
	if err != nil {
		t.Fatalf("ResolveSnapshot: %v", err)
	}
	view, err := host.SessionPluginView(snapshot)
	if err != nil {
		t.Fatalf("SessionPluginView: %v", err)
	}
	if !slices.Equal(view.RegisteredPluginIDs, []string{goDefinition.ID, mcpDefinition.ID}) {
		t.Fatalf("registered IDs = %v", view.RegisteredPluginIDs)
	}
	if !slices.Equal(view.ExposedPluginIDs, []string{mcpDefinition.ID}) {
		t.Fatalf("exposed IDs = %v, want MCP namespace winner only", view.ExposedPluginIDs)
	}
	if len(view.SessionEnvSpecs) != 0 || len(view.SkillSpecs) != 0 {
		t.Fatalf("Go resources leaked through MCP namespace winner: %+v", view)
	}
}

func TestSessionPluginViewProjectsBundledRuntimeResources(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	definitions, err := pluginhost.BuiltinBundledRuntimeDefinitions()
	if err != nil {
		t.Fatalf("BuiltinBundledRuntimeDefinitions: %v", err)
	}
	if len(definitions) != 1 {
		t.Fatalf("bundled runtime definitions = %d", len(definitions))
	}
	definition := definitions[0]
	catalog := plugin.NewCatalog()
	if err := catalog.Register(definition); err != nil {
		t.Fatal(err)
	}
	insertDefinition(t, db, definition)
	if _, err := db.Exec(ctx, `INSERT INTO plugin_config (id, plugin_id, namespace, scope, enabled, config, credential_refs, revision) VALUES ($1, $2, $3, 'system', TRUE, '{}'::jsonb, '{}'::jsonb, 1)`, "20000000-0000-0000-0000-000000000003", definition.ID, definition.Namespace); err != nil {
		t.Fatal(err)
	}
	service := plugin.NewService(db, nil, catalog, nil, inlinePluginMutationFence)
	authority, err := authz.NewUserAuthority(authz.UserID("10000000-0000-0000-0000-000000000003"), false)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.ResolveSnapshot(ctx, authority, "")
	if err != nil {
		t.Fatalf("ResolveSnapshot: %v", err)
	}
	view, err := pluginhost.New(nil).SessionPluginView(snapshot)
	if err != nil {
		t.Fatalf("SessionPluginView: %v", err)
	}
	if !slices.Equal(view.ExposedPluginIDs, []string{definition.ID}) {
		t.Fatalf("exposed IDs = %v", view.ExposedPluginIDs)
	}
	if len(view.BundledBinarySpecs) != 1 || view.BundledBinarySpecs[0].Name != "xberg" {
		t.Fatalf("bundled binary view = %+v", view.BundledBinarySpecs)
	}
	if len(view.SkillSpecs) != 1 || view.SkillSpecs[0].Name != "xberg" {
		t.Fatalf("bundled skill view = %+v", view.SkillSpecs)
	}
}

func TestSessionPluginViewProjectsAuthoredMiseBundle(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	definitions, err := manifest.BuiltinDefinitions()
	if err != nil {
		t.Fatalf("BuiltinDefinitions: %v", err)
	}
	var definition plugin.Definition
	for _, candidate := range definitions {
		if candidate.ID == "tool/mise" {
			definition = candidate
			break
		}
	}
	if definition.ID == "" {
		t.Fatal("tool/mise definition missing")
	}
	catalog := plugin.NewCatalog()
	if err := catalog.Register(definition); err != nil {
		t.Fatal(err)
	}
	insertDefinition(t, db, definition)
	if _, err := db.Exec(ctx, `INSERT INTO plugin_config (id, plugin_id, namespace, scope, enabled, config, credential_refs, revision) VALUES ($1, $2, $3, 'system', TRUE, '{}'::jsonb, '{}'::jsonb, 1)`, "20000000-0000-0000-0000-000000000004", definition.ID, definition.Namespace); err != nil {
		t.Fatal(err)
	}
	service := plugin.NewService(db, nil, catalog, nil, inlinePluginMutationFence)
	authority, err := authz.NewUserAuthority(authz.UserID("10000000-0000-0000-0000-000000000004"), false)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.ResolveSnapshot(ctx, authority, "")
	if err != nil {
		t.Fatalf("ResolveSnapshot: %v", err)
	}
	view, err := pluginhost.New(nil).SessionPluginView(snapshot)
	if err != nil {
		t.Fatalf("SessionPluginView: %v", err)
	}
	if len(view.BundledBinarySpecs) != 1 || view.BundledBinarySpecs[0].PluginID != definition.ID || view.BundledBinarySpecs[0].Name != "mise" {
		t.Fatalf("authored mise bundle = %+v", view.BundledBinarySpecs)
	}
}

func inlinePluginMutationFence(_ context.Context, mutate func() error) error {
	return mutate()
}

func insertUser(t *testing.T, db *pgxpool.Pool, id string) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `INSERT INTO auth_user (id, email) VALUES ($1, $2)`, id, id+"@test.invalid"); err != nil {
		t.Fatal(err)
	}
}

func insertDefinition(t *testing.T, db *pgxpool.Pool, definition plugin.Definition) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `INSERT INTO plugin_definition (id, namespace, display_name, backend, source, implementation_key, spec, default_enabled, revision) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, definition.ID, definition.Namespace, definition.DisplayName, definition.Backend, definition.Source, definition.ImplementationKey, definition.Spec, definition.DefaultEnabled, definition.Revision); err != nil {
		t.Fatal(err)
	}
}

func insertConfig(t *testing.T, db *pgxpool.Pool, id string, definition plugin.Definition, scope, userID string, enabled bool, payload string) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `INSERT INTO plugin_config (id, plugin_id, namespace, scope, user_id, enabled, config, credential_refs, revision) VALUES ($1, $2, $3, $4, $5, $6, $7, '{}'::jsonb, 1)`, id, definition.ID, definition.Namespace, scope, userID, enabled, payload); err != nil {
		t.Fatal(err)
	}
}

func TestPromptUsesFrozenCLIConfig(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	def := plugin.Definition{ID: "tool/prompted", Namespace: "prompted", DisplayName: "Prompted", Backend: plugin.BackendCLI, Source: plugin.SourceBuiltin, ImplementationKey: "tool/prompted", DefaultEnabled: true, Revision: 1, Spec: []byte(`{"prompt":"shipped guidance"}`)}
	catalog := plugin.NewCatalog()
	if err := catalog.Register(def); err != nil {
		t.Fatal(err)
	}
	insertDefinition(t, db, def)
	const userID = "10000000-0000-0000-0000-000000000091"
	const configID = "20000000-0000-0000-0000-000000000091"
	insertUser(t, db, userID)
	insertConfig(t, db, configID, def, "user", userID, true, `{}`)
	svc := plugin.NewService(db, nil, catalog, nil, inlinePluginMutationFence)
	authority, err := authz.NewUserAuthority(authz.UserID(userID), false)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := svc.ResolveSnapshot(ctx, authority, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE plugin_config SET enabled=false,revision=revision+1 WHERE id=$1`, configID); err != nil {
		t.Fatal(err)
	}
	host := pluginhost.New(nil)
	sections, err := host.SystemPromptSections(ctx, pkgplugins.SystemPromptContext{}, snapshot)
	if err != nil || len(sections) != 1 || sections[0].Content != "shipped guidance" {
		t.Fatalf("frozen prompt = %+v, %v", sections, err)
	}
	next, err := svc.ResolveSnapshot(ctx, authority, "")
	if err != nil {
		t.Fatal(err)
	}
	sections, err = host.SystemPromptSections(ctx, pkgplugins.SystemPromptContext{}, next)
	if err != nil || len(sections) != 0 {
		t.Fatalf("disabled prompt = %+v, %v", sections, err)
	}
}
