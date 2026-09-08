package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	agentruntime "github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/internal/authz"
	oauth "github.com/CherryHQ/stella/internal/connections/oauth"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/skill"
	"github.com/CherryHQ/stella/pkg/providers"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

type runnerProjectionOAuthStore struct{ values map[string]string }

func (s *runnerProjectionOAuthStore) Set(_ context.Context, userID, name, value string) error {
	s.values[userID+":"+name] = value
	return nil
}

func (s *runnerProjectionOAuthStore) Delete(_ context.Context, userID, name string) error {
	delete(s.values, userID+":"+name)
	return nil
}

func (s *runnerProjectionOAuthStore) Lookup(_ context.Context, userID, name string) (string, bool, error) {
	value, ok := s.values[userID+":"+name]
	return value, ok, nil
}

func TestNewRunnerFuncPublishesOneFailedPackageProjection(t *testing.T) {
	userID := uuid.NewString()
	const agentID, pluginID = "runner-projection-agent", "runner-projection-package"
	db := dbtest.NewAtMigration(t, runnerImportMigration44)
	seedRunnerIdentity(t, db, userID, agentID)
	digest := "sha256:" + strings.Repeat("a", 64)
	spec := map[string]any{
		"origin":         "package",
		"content_digest": digest,
		"content":        map[string]string{"digest": digest},
		"prompt":         "blocked prompt",
		"skills":         []map[string]string{{"name": "blocked-skill", "path": "skills/blocked-skill/SKILL.md"}},
		"session_env":    []map[string]any{{"env_var": "B_TOKEN", "source": "oauth.access_token", "required": true}},
		"oauth":          []map[string]any{{"provider": "demo", "scopes": []string{"scope-2"}, "bindings": []map[string]string{{"credential": "access_token", "env_var": "B_TOKEN"}}}},
	}
	rawSpec, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	publishedSpec, err := plugin.PublishDefinitionSpec(rawSpec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `
		INSERT INTO plugin_definition(id, display_name, source, spec, default_enabled, revision, creator_user_id)
		VALUES ($1, $2, 'custom', $3::jsonb, false, 1, $4::uuid)`, pluginID, "Runner Projection", publishedSpec, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `
		INSERT INTO plugin_config(id, plugin_id, scope, user_id, agent_id, enabled, config, credential_refs, revision)
		VALUES ($1, $2, 'user', $3::uuid, NULL, true, '{}'::jsonb, '{}'::jsonb, 1)`,
		uuid.NewString(), pluginID, userID); err != nil {
		t.Fatal(err)
	}
	authority, err := authz.NewUserAuthority(authz.UserID(userID), false)
	if err != nil {
		t.Fatal(err)
	}
	plugins := plugin.NewService(db, nil, plugin.NewCatalog(), plugin.BackendPolicy{Transition: noopBackendTransition}, noOpMutationFence)
	snapshot, err := plugins.ResolveSnapshot(t.Context(), authority, "")
	if err != nil {
		t.Fatal(err)
	}
	pluginContext, err := agentruntime.NewPluginContext(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	store := &runnerProjectionOAuthStore{values: make(map[string]string)}
	registry := oauth.NewProviderRegistry()
	registry.Register(oauth.ProviderConfig{ID: "demo", VaultKey: "DEMO_OAUTH"})
	tokens := oauth.NewTokenManager(store)
	tokens.SetRegistry(registry)
	if err := oauth.SaveOAuthBundle(t.Context(), store, userID, "DEMO_OAUTH", oauth.OAuthBundle{
		AccessToken: "scope-1-token", GrantedScope: "scope-1", AccessExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	stellaHome := t.TempDir()
	t.Setenv("STELLA_HOME", stellaHome)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)
	preparedSession := &closeCountingSession{Session: pkgsandbox.NopSession()}
	var capturedPolicy pkgsandbox.Policy
	backends, err := sandbox.NewBackendRegistry(sandbox.BackendDefinition{
		Name: config.SandboxBackendNone,
		Create: func(_ context.Context, request sandbox.BackendRequest) (pkgsandbox.Session, error) {
			capturedPolicy = request.Policy
			return preparedSession, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	snap := &config.Snapshot{AgentID: agentID, Provider: "anthropic", Model: "test-model", APIKey: "test-key", Workspace: t.TempDir()}
	build := newRunnerFunc(withTestSkillDependencies(runnerBuilderConfig{
		Snap: snap, Home: testWorkspaceViewer{root: stellaHome}, SandboxBackends: backends,
		SystemRuntimePlan: fixtureRunnerSystemRuntimePlan(t, stellaHome),
		SandboxBackendFn:  func(context.Context) string { return config.SandboxBackendNone },
		TokenManager:      tokens,
		PluginContextBuilder: func(context.Context, authz.Authority, string) (PluginContext, error) {
			return pluginContext, nil
		},
		ProviderStreamBuilder: func(api, apiKey, baseURL string) (providers.StreamFunc, error) {
			return providers.AdapterStreamFunc(fakeStreamProvider{}), nil
		},
	}))
	builtRunner, err := build(t.Context(), RunnerParams{UserID: userID, AgentID: agentID, SessionID: "runner-projection-session"})
	if err != nil {
		t.Fatalf("build runner: %v", err)
	}
	t.Cleanup(func() { _ = builtRunner.Close() })
	view := builtRunner.PluginContext().SessionPluginView()
	if len(view.ExposedPluginIDs) != 0 || len(view.SessionEnvSpecs) != 0 || len(view.PromptSections) != 0 || len(view.MCPDirectory) != 0 {
		t.Fatalf("failed package leaked into runner view: %+v", view)
	}
	refs := pluginSkillRefs(builtRunner.PluginContext())
	if len(refs) != 1 || !refs[0].Masked || refs[0].PackageID != pluginID {
		t.Fatalf("skill refs = %+v, want one masked failed-package skill", refs)
	}
	skillView, err := skill.CaptureSkillTurnView(t.Context(), emptySkillRuntime{}, allowSkillReads{}, nil, refs, skill.ViewContext{UserID: userID, AgentID: agentID})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(skillView.MaskedSkillNames(), "blocked-skill") {
		t.Fatalf("skill turn view masks = %v, want blocked-skill", skillView.MaskedSkillNames())
	}
	if got := capturedPolicy.Env["B_TOKEN"]; got != "" {
		t.Fatalf("failed package env leaked into sandbox policy: %q", got)
	}
	if preparedSession.closes.Load() != 0 {
		t.Fatalf("session closed before runner cleanup: %d", preparedSession.closes.Load())
	}
}
