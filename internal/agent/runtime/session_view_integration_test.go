package runtime_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/plugin"
	pluginhost "github.com/CherryHQ/stella/internal/plugin/host"
	skillpkg "github.com/CherryHQ/stella/internal/skill"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/resources"
)

func TestEmbeddedCompanionPackagesHaveNoScopedBinaries(t *testing.T) {
	db := dbtest.New(t)
	definitions, err := plugin.BuiltinDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	const userID = "10000000-0000-0000-0000-000000000092"
	insertUser(t, db, userID)
	catalog := plugin.NewCatalog()
	var systemIDs []string
	for _, definition := range definitions {
		if !slices.Contains([]string{"mise", "xberg", "stella"}, definition.ID) {
			continue
		}
		if err := catalog.Register(definition); err != nil {
			t.Fatal(err)
		}
		insertDefinition(t, db, definition)
		if _, err := db.Exec(t.Context(), `INSERT INTO plugin_config (plugin_id, scope, enabled, config, credential_refs, revision) VALUES ($1, 'system', TRUE, '{}'::jsonb, '{}'::jsonb, 1)`, definition.ID); err != nil {
			t.Fatal(err)
		}
		systemIDs = append(systemIDs, definition.ID)
	}
	if !slices.Contains(systemIDs, "stella") || !slices.Contains(systemIDs, "xberg") {
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
		view, err := sessionPluginView(snapshot)
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
		ID: "lift", DisplayName: "Lift",
		Source: plugin.SourceBuiltin, DefaultEnabled: false, Revision: 1,
		Spec: publishedRuntimeSpec(t, `{"binaries":[{"name":"lift","tool":"github:owner/lift","version":"1.0.0"}]}`),
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
	view, err := sessionPluginView(snapshot)
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
	view, err = sessionPluginView(snapshot)
	if err != nil {
		t.Fatalf("SessionPluginView lifted: %v", err)
	}
	if status := view.PackageResults.Status(definition.ID); status.Ready || status.Reason == "" {
		t.Fatalf("incomplete package status = %+v, want bounded failure", status)
	}
	if slices.Contains(view.ExposedPluginIDs, definition.ID) || len(view.BinarySpecs) != 0 {
		t.Fatalf("incomplete package resources leaked: %+v", view)
	}
}

func TestIncompatiblePackagePinIsMaskedWithoutBlockingHealthyPackage(t *testing.T) {
	db := dbtest.New(t)
	const userID = "10000000-0000-0000-0000-000000000081"
	insertUser(t, db, userID)
	a := plugin.Definition{
		ID: "healthy", DisplayName: "Healthy", Source: plugin.SourceBuiltin, Revision: 1,
		Spec: publishedRuntimeSpec(t, `{"binaries":[{"name":"healthy","tool":"github:owner/healthy","version":"1.0.0"}],"skills":[{"name":"healthy-skill"}],"session_env":[{"env_var":"HEALTHY_TOKEN","source":"static"}]}`),
	}
	b := plugin.Definition{
		ID: "pinned", DisplayName: "Pinned", Source: plugin.SourceBuiltin, Revision: 1,
		Spec: publishedRuntimeSpec(t, `{"binaries":[{"name":"old","tool":"github:owner/pinned","version":"1.0.0"}],"skills":[{"name":"shared-skill"}],"session_env":[{"env_var":"PINNED_TOKEN","source":"oauth.access_token"}],"oauth":[{"provider":"demo","scopes":["read"],"bindings":[{"credential":"access_token","env_var":"PINNED_TOKEN"}]}]}`),
	}
	catalog := plugin.NewCatalog()
	for _, definition := range []plugin.Definition{a, b} {
		if err := catalog.Register(definition); err != nil {
			t.Fatal(err)
		}
		insertDefinition(t, db, definition)
	}
	insertConfig(t, db, "20000000-0000-0000-0000-000000000080", a, "user", userID, true, `{}`)
	insertConfig(t, db, "20000000-0000-0000-0000-000000000081", b, "user", userID, true, `{"binaries":{"old":{"version":"1.0.0"}}}`)
	service := plugin.NewService(db, nil, catalog, plugin.BackendPolicy{Transition: noopBackendTransition}, inlinePluginMutationFence)
	authority, err := authz.NewUserAuthority(authz.UserID(userID), false)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.ResolveSnapshot(t.Context(), authority, "")
	if err != nil {
		t.Fatal(err)
	}
	v1Context, err := agentruntime.NewPluginContext(snapshot)
	if err != nil {
		t.Fatalf("v1 context: %v", err)
	}
	v1View := v1Context.SessionPluginView()
	if status := v1View.PackageResults.Status(b.ID); !status.Ready {
		t.Fatalf("v1 pinned package status = %+v, want ready", status)
	}
	if !slices.ContainsFunc(v1View.BinarySpecs, func(spec pkgplugins.PluginBinarySpec) bool {
		return spec.PluginID == b.ID && spec.Name == "old" && spec.Version == "1.0.0"
	}) {
		t.Fatalf("v1 pinned binary missing: %+v", v1View.BinarySpecs)
	}

	v2 := publishedRuntimeSpec(t, `{"binaries":[{"name":"new","tool":"github:owner/pinned","version":"2.0.0"}],"skills":[{"name":"shared-skill"}],"session_env":[{"env_var":"PINNED_TOKEN","source":"oauth.access_token"}],"oauth":[{"provider":"demo","scopes":["read"],"bindings":[{"credential":"access_token","env_var":"PINNED_TOKEN"}]}]}`)
	if _, err := db.Exec(t.Context(), `UPDATE plugin_definition SET spec=$1, revision=2 WHERE id='pinned'`, v2); err != nil {
		t.Fatal(err)
	}
	snapshot, err = service.ResolveSnapshot(t.Context(), authority, "")
	if err != nil {
		t.Fatal(err)
	}
	context, err := agentruntime.NewPluginContext(snapshot)
	if err != nil {
		t.Fatalf("v2 context: %v", err)
	}
	view := context.SessionPluginView()
	if status := view.PackageResults.Status(a.ID); !status.Ready {
		t.Fatalf("healthy package status = %+v, want ready", status)
	}
	if !slices.Contains(view.ExposedPluginIDs, a.ID) || slices.Contains(view.ExposedPluginIDs, b.ID) {
		t.Fatalf("exposed packages = %v, want only healthy", view.ExposedPluginIDs)
	}
	if len(view.BinarySpecs) != 1 || view.BinarySpecs[0].PluginID != a.ID {
		t.Fatalf("visible binaries = %+v, want healthy only", view.BinarySpecs)
	}
	if len(view.SessionEnvSpecs) != 1 || view.SessionEnvSpecs[0].PluginID != a.ID {
		t.Fatalf("visible env = %+v, want healthy only", view.SessionEnvSpecs)
	}
	if !slices.ContainsFunc(view.SkillSpecs, func(spec pkgplugins.PluginSkillSpec) bool {
		return spec.PluginID == b.ID && spec.Name == "shared-skill"
	}) {
		t.Fatalf("failed package Skill declaration was not retained for masking: %+v", view.SkillSpecs)
	}
	if !slices.ContainsFunc(context.SelectedPluginBinarySpecs(), func(spec pkgplugins.PluginBinarySpec) bool {
		return spec.PluginID == b.ID && spec.Name == "new"
	}) {
		t.Fatalf("failed package binary declaration was not retained for conflict checks: %+v", context.SelectedPluginBinarySpecs())
	}
	status := view.PackageResults.Status(b.ID)
	if status.Ready || status.Reason != "selected package configuration is incompatible" {
		t.Fatalf("failed package status = %+v, want config incompatibility", status)
	}
	preparation := sandbox.PrepareOAuthPackages(t.Context(), sandbox.Config{UserID: userID}, view.PackageRequirements)
	if got := preparation.Status(a.ID); !got.Ready {
		t.Fatalf("healthy package OAuth status = %+v, want ready", got)
	}
	if got := preparation.Status(b.ID); got.Ready || got.Reason != "selected package configuration is incompatible" {
		t.Fatalf("failed package OAuth status = %+v, want unchanged config incompatibility", got)
	}
}

func TestAgentGuideVisibilityIsIndependentFromNativeAdmission(t *testing.T) {
	db := dbtest.New(t)
	definitions, err := plugin.BuiltinDefinitions()
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
	view, err := sessionPluginView(snapshot)
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
	viewOff, err := sessionPluginView(snapshot)
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
	viewDenied, err := sessionPluginView(snapshot)
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
	disabledView, err := sessionPluginView(disabledSnapshot)
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

func TestWebAgentRetainsSkillAndBinariesWhenBunAgentIsDisabled(t *testing.T) {
	db := dbtest.New(t)
	definitions, err := plugin.BuiltinDefinitions()
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
	if _, err := db.Exec(t.Context(), `UPDATE plugin_config SET enabled = FALSE, revision = revision + 1 WHERE plugin_id = 'bun'`); err != nil {
		t.Fatal(err)
	}
	authority, err := authz.NewUserAuthority("10000000-0000-0000-0000-000000000094", true)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.ResolveSnapshot(t.Context(), authority, "")
	if err != nil {
		t.Fatalf("ResolveSnapshot: %v", err)
	}
	view, err := sessionPluginView(snapshot)
	if err != nil {
		t.Fatalf("SessionPluginView: %v", err)
	}
	if slices.Contains(view.ExposedPluginIDs, "bun") || !slices.Contains(view.ExposedPluginIDs, "web") {
		t.Fatalf("bun/web exposure = %v, want bun hidden and web exposed", view.ExposedPluginIDs)
	}
	var names []string
	for _, spec := range view.BinarySpecs {
		if spec.PluginID == "web" {
			names = append(names, spec.Name)
		}
	}
	if !slices.Equal(names, []string{"bun", "lightpanda"}) {
		t.Fatalf("web binaries = %v, want bun and lightpanda", names)
	}
	assertBuiltinGuides(t, view, true)
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
	if _, err := db.Exec(context.Background(), `INSERT INTO plugin_definition (id, display_name, source, spec, default_enabled, revision) VALUES ($1, $2, $3, $4, $5, $6)`, definition.ID, definition.DisplayName, definition.Source, definition.Spec, definition.DefaultEnabled, definition.Revision); err != nil {
		t.Fatal(err)
	}
}

func insertConfig(t *testing.T, db *pgxpool.Pool, id string, definition plugin.Definition, scope, userID string, enabled bool, payload string) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `INSERT INTO plugin_config (id, plugin_id, scope, user_id, enabled, config, credential_refs, revision) VALUES ($1, $2, $3, $4, $5, $6, '{}'::jsonb, 1)`, id, definition.ID, scope, userID, enabled, payload); err != nil {
		t.Fatal(err)
	}
}

func TestPromptUsesFrozenCLIConfig(t *testing.T) {
	db := dbtest.New(t)
	ctx := t.Context()
	def := plugin.Definition{ID: "prompted", DisplayName: "Prompted", Source: plugin.SourceBuiltin, DefaultEnabled: true, Revision: 1, Spec: publishedRuntimeSpec(t, `{"prompt":"shipped guidance"}`)}
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
	view, err := sessionPluginView(snapshot)
	sections := view.PromptSections
	if err != nil || len(sections) != 1 || sections[0].Content != "shipped guidance" {
		t.Fatalf("frozen prompt = %+v, %v", sections, err)
	}
	next, err := svc.ResolveSnapshot(ctx, authority, "")
	if err != nil {
		t.Fatal(err)
	}
	view, err = sessionPluginView(next)
	sections = view.PromptSections
	if err != nil || len(sections) != 0 {
		t.Fatalf("disabled prompt = %+v, %v", sections, err)
	}
}

func sessionPluginView(snapshot plugin.Snapshot) (pkgplugins.SessionPluginView, error) {
	context, err := agentruntime.NewPluginContext(snapshot)
	return context.SessionPluginView(), err
}

func publishedRuntimeSpec(t *testing.T, raw string) json.RawMessage {
	t.Helper()
	spec, err := plugin.PublishDefinitionSpec(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	return spec
}
