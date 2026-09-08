package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	oauth "github.com/CherryHQ/stella/internal/connections/oauth"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type packageOAuthStore struct {
	mu   sync.Mutex
	data map[string]string
}

func (s *packageOAuthStore) key(userID, name string) string { return userID + ":" + name }
func (s *packageOAuthStore) Set(_ context.Context, userID, name, plaintext string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[s.key(userID, name)] = plaintext
	return nil
}

func (s *packageOAuthStore) Delete(_ context.Context, userID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, s.key(userID, name))
	return nil
}

func (s *packageOAuthStore) Lookup(_ context.Context, userID, name string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[s.key(userID, name)]
	return value, ok, nil
}

// packageAdmissionFixture drives the real Runtime admission and runner cache
// with a fresh package projection on every turn. The runner itself is fake so
// this stays deterministic and exercises no model or network I/O.
type packageAdmissionFixture struct {
	mu        sync.RWMutex
	authored  PluginContext
	store     *packageOAuthStore
	tokens    *oauth.TokenManager
	cliReadyB bool
	last      PluginContext
	builds    int
}

func (f *packageAdmissionFixture) context() PluginContext {
	f.mu.RLock()
	authored, tokens := f.authored, f.tokens
	f.mu.RUnlock()
	result := sandbox.PrepareOAuthPackages(context.Background(), sandbox.Config{
		UserID: "package-user", TokenManager: tokens,
	}, authored.view.PackageRequirements)
	return authored.WithOAuthPreparationResult(result)
}

func (f *packageAdmissionFixture) setToken(bundle oauth.OAuthBundle) {
	f.mu.Lock()
	if err := oauth.SaveOAuthBundle(context.Background(), f.store, "package-user", "DEMO_OAUTH", bundle); err != nil {
		f.mu.Unlock()
		panic(err)
	}
	f.mu.Unlock()
}

func (f *packageAdmissionFixture) setCLIReady(ready bool) {
	f.mu.Lock()
	f.cliReadyB = ready
	f.mu.Unlock()
}

func (f *packageAdmissionFixture) runtime(t *testing.T) *Runtime {
	t.Helper()
	rt, err := New(Config{
		Memory: fakeMemory{},
		NewRunner: func(_ context.Context, params RunnerParams) (Runner, error) {
			f.mu.Lock()
			f.builds++
			cliReadyB := f.cliReadyB
			oauthResult := params.PluginContext.OAuthPreparationResult()
			cli := pkgplugins.PluginPreparationResult{Packages: []pkgplugins.PluginPackageStatus{
				{PluginID: "package-a", Ready: true},
				{PluginID: "package-b", Ready: cliReadyB, Reason: "CLI preparation failed"},
			}}
			f.last = params.PluginContext.WithPreparationResult(oauthResult.Merge(cli))
			f.mu.Unlock()
			return &fakeRunner{alive: true, lastAct: time.Now(), pluginContext: f.last}, nil
		},
		PluginContextBuilder: func(context.Context, authz.Authority, string) (PluginContext, error) {
			return f.context(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

func packageProjectionContext() PluginContext {
	base := PluginContext{view: pkgplugins.SessionPluginView{
		RegisteredPluginIDs: []string{"package-a", "package-b"},
		ExposedPluginIDs:    []string{"package-a", "package-b"},
		SessionEnvSpecs: []pkgplugins.SessionEnvSpec{
			{PluginID: "package-a", EnvVar: "A_TOKEN"},
			{PluginID: "package-b", EnvVar: "B_TOKEN"},
		},
		PromptSections: []pkgplugins.SystemPromptSection{
			{PluginID: "package-a", Content: "a prompt"},
			{PluginID: "package-b", Content: "b prompt"},
		},
		MCPDirectory: []pkgplugins.MCPDirectoryEntry{
			{PluginResourceIdentity: pkgplugins.PluginResourceIdentity{PluginID: "package-a"}},
			{PluginResourceIdentity: pkgplugins.PluginResourceIdentity{PluginID: "package-b"}},
		},
		SkillSpecs: []pkgplugins.PluginSkillSpec{
			{PluginResourceIdentity: pkgplugins.PluginResourceIdentity{PluginID: "package-a"}, Name: "a"},
			{PluginResourceIdentity: pkgplugins.PluginResourceIdentity{PluginID: "package-b"}, Name: "b"},
		},
		PackageRequirements: []pkgplugins.PluginPackageRequirement{
			{PluginID: "package-a", OAuth: []pkgplugins.PluginOAuthRequirement{{Provider: "demo", Scopes: []string{"scope-1"}}}},
			{PluginID: "package-b", OAuth: []pkgplugins.PluginOAuthRequirement{{Provider: "demo", Scopes: []string{"scope-2"}}}},
		},
	}}
	return base
}

func preparePackageAdmission(t *testing.T, fixture *packageAdmissionFixture, rt *Runtime, info session.Info, authority authz.Authority) PluginContext {
	t.Helper()
	stream, err := rt.ChatAdmitted(context.Background(), info, "hello", WithTurnAuthority(authority))
	if err != nil {
		t.Fatal(err)
	}
	for event := range stream {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
	}
	fixture.mu.RLock()
	defer fixture.mu.RUnlock()
	return fixture.last
}

func TestPackageReadinessRunsThroughFreshAdmissionAndCache(t *testing.T) {
	authority, err := authz.NewUserAuthority(authz.UserID("package-user"), false)
	if err != nil {
		t.Fatal(err)
	}
	info := session.NewInfo("package-readiness", "package-agent", "package-user", "web", session.KindChat, "", time.Now().UTC())
	store := &packageOAuthStore{data: make(map[string]string)}
	registry := oauth.NewProviderRegistry()
	registry.Register(oauth.ProviderConfig{ID: "demo", VaultKey: "DEMO_OAUTH"})
	tokens := oauth.NewTokenManager(store)
	tokens.SetRegistry(registry)
	fixture := &packageAdmissionFixture{authored: packageProjectionContext(), store: store, tokens: tokens, cliReadyB: true}
	fixture.setToken(oauth.OAuthBundle{AccessToken: "token-a", GrantedScope: "scope-1", AccessExpiresAt: time.Now().UTC().Add(time.Hour)})
	rt := fixture.runtime(t)

	first := preparePackageAdmission(t, fixture, rt, info, authority)
	if got := first.SessionPluginView(); len(got.ExposedPluginIDs) != 1 || got.ExposedPluginIDs[0] != "package-a" || len(got.SessionEnvSpecs) != 1 || got.SessionEnvSpecs[0].PluginID != "package-a" || len(got.PromptSections) != 1 || got.PromptSections[0].PluginID != "package-a" || len(got.MCPDirectory) != 1 || got.MCPDirectory[0].PluginID != "package-a" {
		t.Fatalf("failed package leaked into admission view: %+v", got)
	}
	if fixture.builds != 1 {
		t.Fatalf("initial builds = %d, want 1", fixture.builds)
	}

	// Rechecking an unchanged OAuth failure reuses the runner. Rotating the
	// token value while retaining scope-1 produces the same readiness result
	// and therefore also keeps the runner.
	_ = preparePackageAdmission(t, fixture, rt, info, authority)
	fixture.setToken(oauth.OAuthBundle{AccessToken: "token-b", GrantedScope: "scope-1", AccessExpiresAt: time.Now().UTC().Add(time.Hour)})
	_ = preparePackageAdmission(t, fixture, rt, info, authority)
	if fixture.builds != 1 {
		t.Fatalf("unchanged readiness rebuilt runner %d times, want 1", fixture.builds)
	}

	fixture.setToken(oauth.OAuthBundle{AccessToken: "token-b", GrantedScope: "scope-1 scope-2", AccessExpiresAt: time.Now().UTC().Add(time.Hour)})
	recovered := preparePackageAdmission(t, fixture, rt, info, authority)
	if got := recovered.SessionPluginView(); len(got.ExposedPluginIDs) != 2 || fixture.builds != 2 {
		t.Fatalf("OAuth recovery view/builds = %+v/%d, want both packages and 2 builds", got, fixture.builds)
	}
}

func TestCLIReadinessFailureRetriesAtNextAdmission(t *testing.T) {
	authority, err := authz.NewUserAuthority(authz.UserID("cli-user"), false)
	if err != nil {
		t.Fatal(err)
	}
	info := session.NewInfo("cli-readiness", "cli-agent", "cli-user", "web", session.KindChat, "", time.Now().UTC())
	store := &packageOAuthStore{data: make(map[string]string)}
	registry := oauth.NewProviderRegistry()
	registry.Register(oauth.ProviderConfig{ID: "demo", VaultKey: "DEMO_OAUTH"})
	tokens := oauth.NewTokenManager(store)
	tokens.SetRegistry(registry)
	fixture := &packageAdmissionFixture{authored: packageProjectionContext(), store: store, tokens: tokens, cliReadyB: false}
	fixture.setToken(oauth.OAuthBundle{AccessToken: "token-a", GrantedScope: "scope-1 scope-2", AccessExpiresAt: time.Now().UTC().Add(time.Hour)})
	rt := fixture.runtime(t)

	_ = preparePackageAdmission(t, fixture, rt, info, authority)
	if fixture.builds != 1 {
		t.Fatalf("initial CLI failure builds = %d, want 1", fixture.builds)
	}
	// The failed package remains hidden for this turn, but the cache retires it
	// before the next ordinary admission so installation can be retried.
	_ = preparePackageAdmission(t, fixture, rt, info, authority)
	if fixture.builds != 2 {
		t.Fatalf("CLI retry builds = %d, want 2", fixture.builds)
	}

	fixture.setCLIReady(true)
	_ = preparePackageAdmission(t, fixture, rt, info, authority)
	if fixture.builds != 3 {
		t.Fatalf("CLI recovery builds = %d, want 3", fixture.builds)
	}
}

func TestStaticPackageFailureStaysMaskedThroughAdmission(t *testing.T) {
	authority, err := authz.NewUserAuthority(authz.UserID("package-user"), false)
	if err != nil {
		t.Fatal(err)
	}
	info := session.NewInfo("static-package-failure", "package-agent", "package-user", "web", session.KindChat, "", time.Now().UTC())
	store := &packageOAuthStore{data: make(map[string]string)}
	registry := oauth.NewProviderRegistry()
	registry.Register(oauth.ProviderConfig{ID: "demo", VaultKey: "DEMO_OAUTH"})
	tokens := oauth.NewTokenManager(store)
	tokens.SetRegistry(registry)
	authored := packageProjectionContext()
	authored.view.PackageResults = pkgplugins.PluginPreparationResult{Packages: []pkgplugins.PluginPackageStatus{
		{PluginID: "package-a", Ready: true},
		{PluginID: "package-b", Reason: "selected package configuration is incompatible"},
	}}
	authored.view.PackageRequirements[1].UnavailableReason = "selected package configuration is incompatible"
	fixture := &packageAdmissionFixture{authored: authored, store: store, tokens: tokens, cliReadyB: true}
	fixture.setToken(oauth.OAuthBundle{AccessToken: "token-a", GrantedScope: "scope-1", AccessExpiresAt: time.Now().UTC().Add(time.Hour)})
	rt := fixture.runtime(t)

	first := preparePackageAdmission(t, fixture, rt, info, authority)
	view := first.SessionPluginView()
	if len(view.ExposedPluginIDs) != 1 || view.ExposedPluginIDs[0] != "package-a" {
		t.Fatalf("admission exposed packages = %v, want healthy only", view.ExposedPluginIDs)
	}
	if status := view.PackageResults.Status("package-a"); !status.Ready {
		t.Fatalf("healthy package status = %+v, want ready", status)
	}
	if status := view.PackageResults.Status("package-b"); status.Ready || status.Reason != "selected package configuration is incompatible" {
		t.Fatalf("incompatible package status = %+v, want stable failure", status)
	}
	if fixture.builds != 1 {
		t.Fatalf("static failure builds = %d, want 1", fixture.builds)
	}
}
