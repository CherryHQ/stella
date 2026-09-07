package host_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/plugin"
	pluginhost "github.com/CherryHQ/stella/internal/plugin/host"
	"github.com/CherryHQ/stella/internal/plugin/manifest"
	skillpkg "github.com/CherryHQ/stella/internal/skill"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/resources"
)

func TestSystemCLIResourcesStayOutsideScopedSelections(t *testing.T) {
	db := dbtest.New(t)
	definitions, err := manifest.BuiltinDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	const userID = "10000000-0000-0000-0000-000000000092"
	insertUser(t, db, userID)
	catalog := plugin.NewCatalog()
	var systemIDs []string
	for _, definition := range definitions {
		if !manifest.IsSystemPlugin(definition) {
			continue
		}
		if err := catalog.Register(definition); err != nil {
			t.Fatal(err)
		}
		insertDefinition(t, db, definition)
		if _, err := db.Exec(t.Context(), `INSERT INTO plugin_config (plugin_id, namespace, scope, enabled, config, credential_refs, revision) VALUES ($1, $2, 'system', TRUE, '{}'::jsonb, '{}'::jsonb, 1)`, definition.ID, definition.Namespace); err != nil {
			t.Fatal(err)
		}
		systemIDs = append(systemIDs, definition.ID)
	}
	if !slices.Contains(systemIDs, "system/stella") || !slices.Contains(systemIDs, "system/xberg") {
		t.Fatal("system skill owners are missing from the catalog")
	}
	service := plugin.NewService(db, nil, catalog, plugin.BackendPolicy{Transition: noopBackendTransition}, inlinePluginMutationFence)
	authority, err := authz.NewUserAuthority(authz.UserID(userID), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{true, false} {
		if _, err := db.Exec(t.Context(), `UPDATE plugin_config SET enabled = $1, revision = revision + 1`, enabled); err != nil {
			t.Fatal(err)
		}
		snapshot, err := service.ResolveSnapshot(t.Context(), authority, "")
		if err != nil {
			t.Fatal(err)
		}
		view, err := pluginhost.New(nil).SessionPluginView(snapshot)
		if err != nil {
			t.Fatalf("enabled=%v: %v", enabled, err)
		}
		if len(view.BinarySpecs) != 0 {
			t.Fatalf("system CLIs leaked into scoped installer: %+v", view.BinarySpecs)
		}
		for _, id := range systemIDs {
			if slices.Contains(view.ExposedPluginIDs, id) != enabled {
				t.Fatalf("owner %s exposure does not follow enabled=%v: %v", id, enabled, view.ExposedPluginIDs)
			}
		}
	}
}

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

	service := plugin.NewService(db, nil, catalog, plugin.BackendPolicy{Transition: noopBackendTransition}, inlinePluginMutationFence)
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

func TestAgentGuideVisibilityIsIndependentFromNativeAdmission(t *testing.T) {
	db := dbtest.New(t)
	definitions, err := manifest.BuiltinDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	catalog := plugin.NewCatalog()
	for _, definition := range definitions {
		if err := catalog.Register(definition); err != nil {
			t.Fatal(err)
		}
		insertDefinition(t, db, definition)
	}
	service := plugin.NewService(db, nil, catalog, plugin.BackendPolicy{Transition: noopBackendTransition}, inlinePluginMutationFence)
	if err := service.SyncBuiltinDefaults(t.Context()); err != nil {
		t.Fatalf("SyncBuiltinDefaults: %v", err)
	}
	authority, err := authz.NewUserAuthority("10000000-0000-0000-0000-000000000093", true)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.ResolveSnapshot(t.Context(), authority, "")
	if err != nil {
		t.Fatalf("ResolveSnapshot: %v", err)
	}

	bundled, err := resources.Default()
	if err != nil {
		t.Fatalf("resources.Default: %v", err)
	}
	if got := len(bundled.BuiltinSkills()); got != 10 {
		t.Fatalf("builtin skill count = %d, want 10", got)
	}
	host := pluginhost.New(nil)
	nativeStore := &nativeAdmissionStore{
		plugins:      map[string]config.Plugin{"system/email": {ID: "system/email", Enabled: true}},
		nativeDenies: map[string]map[string]bool{},
	}
	nativePolicy := plugin.NewNativePolicy(nativeStore, plugin.NativeRegistryMap{"system/email": true})
	host.SetNativePolicy(nativePolicy)
	view, err := host.SessionPluginView(snapshot)
	if err != nil {
		t.Fatalf("SessionPluginView: %v", err)
	}
	assertNoNativeIDs(t, view)
	assertBuiltinGuides(t, view, true)

	nativeStore.plugins["system/email"] = config.Plugin{ID: "system/email", Enabled: false}
	allowed, err := nativePolicy.Allows(t.Context(), "system/email", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("native global off was admitted")
	}
	viewOff, err := host.SessionPluginView(snapshot)
	if err != nil {
		t.Fatalf("SessionPluginView with native global off: %v", err)
	}
	assertBuiltinGuides(t, viewOff, true)

	nativeStore.plugins["system/email"] = config.Plugin{ID: "system/email", Enabled: true}
	nativeStore.nativeDenies["system/email"] = map[string]bool{"agent-1": true}
	allowed, err = nativePolicy.Allows(t.Context(), "system/email", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("native Agent deny was admitted")
	}
	viewDenied, err := host.SessionPluginView(snapshot)
	if err != nil {
		t.Fatalf("SessionPluginView with native Agent deny: %v", err)
	}
	assertBuiltinGuides(t, viewDenied, true)

	delete(nativeStore.nativeDenies, "system/email")
	allowed, err = nativePolicy.Allows(t.Context(), "system/email", "agent-1")
	if err != nil || !allowed {
		t.Fatalf("native policy before Agent guide disable = %v, %v; want allowed", allowed, err)
	}
	if _, err := db.Exec(t.Context(), `UPDATE plugin_config SET enabled = FALSE, revision = revision + 1 WHERE plugin_id = 'email' AND scope = 'system'`); err != nil {
		t.Fatal(err)
	}
	disabledSnapshot, err := service.ResolveSnapshot(t.Context(), authority, "")
	if err != nil {
		t.Fatalf("ResolveSnapshot with email Agent disabled: %v", err)
	}
	disabledView, err := host.SessionPluginView(disabledSnapshot)
	if err != nil {
		t.Fatalf("SessionPluginView with email Agent disabled: %v", err)
	}
	if !slices.Contains(disabledView.RegisteredPluginIDs, "email") || slices.Contains(disabledView.ExposedPluginIDs, "email") {
		t.Fatalf("email Agent registration/exposure = %v/%v", disabledView.RegisteredPluginIDs, disabledView.ExposedPluginIDs)
	}
	assertBuiltinGuides(t, disabledView, false)
	allowed, err = nativePolicy.Allows(t.Context(), "system/email", "agent-1")
	if err != nil || !allowed {
		t.Fatalf("native policy changed after email Agent disable = %v, %v", allowed, err)
	}
}

func assertNoNativeIDs(t *testing.T, view pkgplugins.SessionPluginView) {
	t.Helper()
	for _, nativeID := range []string{"system/email", "system/recally", "system/scheduler"} {
		if slices.Contains(view.RegisteredPluginIDs, nativeID) || slices.Contains(view.ExposedPluginIDs, nativeID) {
			t.Fatalf("Agent view leaked native ID %q: %+v", nativeID, view)
		}
	}
}

func assertBuiltinGuides(t *testing.T, view pkgplugins.SessionPluginView, wantEmail bool) {
	t.Helper()
	section, err := skillpkg.BuildAuthorizedPromptSection(t.Context(), pkgplugins.SystemPromptContext{
		RegisteredPluginIDs: view.RegisteredPluginIDs,
		EnabledPluginIDs:    view.ExposedPluginIDs,
	}, nil, emptySkillIdentityReader{}, allowAllGuideReads{})
	if err != nil {
		t.Fatalf("BuildAuthorizedPromptSection: %v", err)
	}
	for _, name := range []string{"html-artifact", "lark-cli", "python-script", "recally", "scheduler", "skill-creator", "stella", "web", "xberg"} {
		if !strings.Contains(section.Content, "<name>"+name+"</name>") {
			t.Fatalf("guide %q missing from prompt: %s", name, section.Content)
		}
	}
	emailVisible := strings.Contains(section.Content, "<name>email</name>")
	if emailVisible != wantEmail {
		t.Fatalf("email guide visible = %v, want %v: %s", emailVisible, wantEmail, section.Content)
	}
}

type emptySkillIdentityReader struct{}

func (emptySkillIdentityReader) GetIdentity(context.Context, string) (*skillpkg.Skill, error) {
	return nil, nil
}

func (emptySkillIdentityReader) ListIdentityVisible(context.Context, skillpkg.ViewContext) ([]skillpkg.Skill, error) {
	return nil, nil
}

func (emptySkillIdentityReader) ListIdentityByScope(context.Context, string, string, string) ([]skillpkg.Skill, error) {
	return nil, nil
}

func (emptySkillIdentityReader) ListIdentityCandidate(context.Context, string, skillpkg.ViewContext) ([]skillpkg.Skill, error) {
	return nil, nil
}

func (emptySkillIdentityReader) LoadCurrentRevision(context.Context, skillpkg.Skill) (skillpkg.ManagedRevision, error) {
	return skillpkg.ManagedRevision{}, fs.ErrNotExist
}

func (emptySkillIdentityReader) LoadExactRevision(context.Context, skillpkg.Skill, string) (skillpkg.ManagedRevision, error) {
	return skillpkg.ManagedRevision{}, fs.ErrNotExist
}

type allowAllGuideReads struct{}

func (allowAllGuideReads) BeginRead(context.Context) (skillpkg.SkillReadDecision, error) {
	return allowAllGuideReadDecision{}, nil
}

type allowAllGuideReadDecision struct{}

func (allowAllGuideReadDecision) AllowRead(context.Context, string, string, string, string) (bool, error) {
	return true, nil
}

type nativeAdmissionStore struct {
	plugins      map[string]config.Plugin
	nativeDenies map[string]map[string]bool
}

func (s *nativeAdmissionStore) GetPlugin(_ context.Context, id string) (config.Plugin, error) {
	return s.plugins[id], nil
}

func (s *nativeAdmissionStore) SetNativePluginEnabled(_ context.Context, id string, enabled bool) error {
	p := s.plugins[id]
	p.ID, p.Enabled = id, enabled
	s.plugins[id] = p
	return nil
}

func (s *nativeAdmissionStore) GetNativeAdmission(_ context.Context, nativeID, agentID string) (bool, bool, bool, error) {
	p, present := s.plugins[nativeID]
	return p.Enabled, present, s.nativeDenies[nativeID][agentID], nil
}

func (s *nativeAdmissionStore) IsNativeAgentDenied(_ context.Context, nativeID, agentID string) (bool, error) {
	return s.nativeDenies[nativeID][agentID], nil
}

func (s *nativeAdmissionStore) SetNativeAgentDeny(_ context.Context, nativeID, agentID string) error {
	if s.nativeDenies[nativeID] == nil {
		s.nativeDenies[nativeID] = map[string]bool{}
	}
	s.nativeDenies[nativeID][agentID] = true
	return nil
}

func (s *nativeAdmissionStore) DeleteNativeAgentDeny(_ context.Context, nativeID, agentID string) error {
	delete(s.nativeDenies[nativeID], agentID)
	return nil
}

func (s *nativeAdmissionStore) ListNativeAgentDenials(_ context.Context, nativeID string) ([]plugin.NativeAgentDeny, error) {
	var out []plugin.NativeAgentDeny
	for agentID, denied := range s.nativeDenies[nativeID] {
		if denied {
			out = append(out, plugin.NativeAgentDeny{NativeID: nativeID, AgentID: agentID})
		}
	}
	return out, nil
}

func inlinePluginMutationFence(_ context.Context, mutate func() error) error {
	return mutate()
}

func noopBackendTransition(context.Context, pgx.Tx, authz.Authority, plugin.MutationKind, plugin.Definition, *plugin.Config, *plugin.Config) error {
	return nil
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
	svc := plugin.NewService(db, nil, catalog, plugin.BackendPolicy{Transition: noopBackendTransition}, inlinePluginMutationFence)
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
