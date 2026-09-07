package db

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/plugin"
)

func TestUnifiedPluginSnapshotFiltersDormantAndForeignDefinitions(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()
	userA := insertPluginUser(t, db, "snapshot-a@example.test", false)
	userB := insertPluginUser(t, db, "snapshot-b@example.test", false)

	builtin := pluginDefinition("shared", true)
	dormant := pluginDefinition("dormant", true)
	custom := plugin.Definition{
		ID: "shared-a", DisplayName: "Shared A",
		Backend: plugin.BackendMCP, Source: plugin.SourceCustom, ImplementationKey: "mcp-a",
		Spec: json.RawMessage(`{"schema":1}`), Revision: 1, CreatorUserID: string(userA.UserID()),
	}
	hidden := plugin.Definition{
		ID: "hidden", DisplayName: "Hidden",
		Backend: plugin.BackendMCP, Source: plugin.SourceCustom, ImplementationKey: "mcp-hidden",
		Spec: json.RawMessage(`{}`), Revision: 1, CreatorUserID: string(userA.UserID()),
	}
	foreign := plugin.Definition{
		ID: "foreign", DisplayName: "Foreign",
		Backend: plugin.BackendMCP, Source: plugin.SourceCustom, ImplementationKey: "mcp-foreign",
		Spec: json.RawMessage(`{}`), Revision: 1, CreatorUserID: string(userB.UserID()),
	}
	negative := plugin.Definition{
		ID: "negative", DisplayName: "Negative",
		Backend: plugin.BackendMCP, Source: plugin.SourceCustom, ImplementationKey: "mcp-negative",
		Spec: json.RawMessage(`{}`), Revision: 1, CreatorUserID: string(userA.UserID()),
	}
	negativePayload := plugin.Definition{
		ID: "negative-payload", DisplayName: "Negative Payload",
		Backend: plugin.BackendMCP, Source: plugin.SourceCustom, ImplementationKey: "mcp-negative-payload",
		Spec: json.RawMessage(`{}`), Revision: 1, CreatorUserID: string(userA.UserID()),
	}
	privateNegative := plugin.Definition{
		ID: "private-negative", DisplayName: "Private Negative",
		Backend: plugin.BackendMCP, Source: plugin.SourceCustom, ImplementationKey: "mcp-private-negative",
		Spec: json.RawMessage(`{}`), Revision: 1, CreatorUserID: string(userB.UserID()),
	}
	sharedDenied := plugin.Definition{
		ID: "shared-denied", DisplayName: "Shared Denied",
		Backend: plugin.BackendMCP, Source: plugin.SourceCustom, ImplementationKey: "mcp-shared-denied",
		Spec: json.RawMessage(`{}`), Revision: 1, CreatorUserID: string(userA.UserID()),
	}

	for _, def := range []plugin.Definition{builtin, dormant, custom, hidden, foreign, negative, negativePayload, privateNegative, sharedDenied} {
		if _, err := db.Exec(ctx, `
			INSERT INTO plugin_definition (id, display_name, backend, source, implementation_key, spec, default_enabled, revision, creator_user_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, '')::uuid)
		`, def.ID, def.DisplayName, def.Backend, def.Source, def.ImplementationKey, def.Spec, def.DefaultEnabled, def.Revision, def.CreatorUserID); err != nil {
			t.Fatalf("insert definition %s: %v", def.ID, err)
		}
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO plugin_config (plugin_id, scope, user_id, enabled, config)
		VALUES ($1, 'user', $2, true, '{}'::jsonb)
	`, custom.ID, userA.UserID()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO plugin_config (plugin_id, scope, user_id, enabled, config)
		VALUES ($1, 'user', $2, true, '{}'::jsonb)
	`, foreign.ID, userB.UserID()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO plugin_config (plugin_id, scope, user_id, enabled, config)
		VALUES ($1, 'user', $2, false, NULL)
	`, negative.ID, userA.UserID()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO plugin_config (plugin_id, scope, user_id, enabled, config)
		VALUES ($1, 'user', $2, true, '{}'::jsonb)
	`, negativePayload.ID, userA.UserID()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO plugin_config (plugin_id, scope, enabled, config)
		VALUES ($1, 'system', false, NULL)
	`, privateNegative.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO plugin_config (plugin_id, scope, enabled, config)
		VALUES ($1, 'system', false, '{}'::jsonb)
	`, sharedDenied.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO plugin_config (plugin_id, scope, enabled, config)
		VALUES ($1, 'system', true, '{}'::jsonb)
	`, dormant.ID); err != nil {
		t.Fatalf("insert dormant config: %v", err)
	}

	catalog := plugin.NewCatalog()
	if err := catalog.Register(builtin); err != nil {
		t.Fatal(err)
	}
	service := plugin.NewService(db, nil, catalog, plugin.BackendPolicy{Validate: noopPluginValidator, Transition: noopBackendTransition}, inlinePluginMutationFence)
	snapshot, err := service.ResolveSnapshot(ctx, userA, "")
	if err != nil {
		t.Fatal(err)
	}

	selected, ok := snapshot.Get(custom.ID)
	if !ok || selected.Effective.PluginID != custom.ID || selected.Config == nil {
		t.Fatalf("custom winner = %#v, found=%v", selected, ok)
	}
	loser, ok := snapshot.Get(builtin.ID)
	if !ok || loser.Effective.PluginID != builtin.ID || !loser.Effective.IsEffectivelyEnabled {
		t.Fatalf("builtin by-id result = %#v, found=%v", loser, ok)
	}
	if _, ok := snapshot.Get(hidden.ID); ok {
		t.Fatal("custom definition without owned payload was exposed")
	}
	if _, ok := snapshot.Get(foreign.ID); ok {
		t.Fatal("foreign custom definition was exposed")
	}
	if _, ok := snapshot.Get(privateNegative.ID); ok {
		t.Fatal("ownerless system-negative custom definition was exposed")
	}
	if _, err := snapshot.Resolve(privateNegative.ID); !errors.Is(err, plugin.ErrNotFound) {
		t.Fatalf("ownerless system-negative Resolve = %v, want not found", err)
	}
	for _, def := range snapshot.Definitions() {
		if def.ID == privateNegative.ID {
			t.Fatal("ownerless system-negative custom definition was listed")
		}
	}
	if _, ok := snapshot.Get("dormant"); ok {
		t.Fatal("builtin absent from shipped catalog was exposed")
	}
	negativeResult, ok := snapshot.Get(negative.ID)
	if !ok || negativeResult.Effective.IsEffectivelyEnabled {
		t.Fatalf("custom own-negative by-id result = %#v, found=%v", negativeResult, ok)
	}
	sharedDeniedResult, ok := snapshot.Get(sharedDenied.ID)
	if !ok || sharedDeniedResult.Effective.IsEffectivelyEnabled {
		t.Fatalf("shared payload-negative by-id result = %#v, found=%v", sharedDeniedResult, ok)
	}
	resolved, err := snapshot.Resolve(sharedDenied.ID)
	if err != nil || resolved.PluginID != sharedDenied.ID || resolved.IsEffectivelyEnabled {
		t.Fatalf("shared payload-negative plugin = %#v, err=%v", resolved, err)
	}
	resolved, err = snapshot.Resolve(custom.ID)
	if err != nil || resolved.PluginID != custom.ID {
		t.Fatalf("shared plugin = %#v, err=%v", resolved, err)
	}
	resolved, err = snapshot.Resolve(negative.ID)
	if err != nil || resolved.PluginID != negative.ID || resolved.IsEffectivelyEnabled {
		t.Fatalf("negative plugin = %#v, err=%v", resolved, err)
	}
}
