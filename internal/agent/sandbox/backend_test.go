package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/config"
	oauth "github.com/CherryHQ/stella/internal/connections/oauth"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// stubVaultLoader is a test-only VaultEnvLoader that returns a fixed map.
type stubVaultLoader struct {
	noDeclarableSecrets
	env map[string]string
	err error
}

func (s *stubVaultLoader) LoadEnvForAgentProject(_ context.Context, _ string, _ string, _ string) (map[string]string, error) {
	return s.env, s.err
}

type stubOAuthVaultStore struct {
	noDeclarableSecrets
	data map[string]string
}

func newStubOAuthVaultStore() *stubOAuthVaultStore {
	return &stubOAuthVaultStore{data: make(map[string]string)}
}

func (s *stubOAuthVaultStore) key(userID string, name string) string {
	return userID + ":" + name
}

func (s *stubOAuthVaultStore) Set(_ context.Context, userID string, name string, plaintext string) error {
	s.data[s.key(userID, name)] = plaintext
	return nil
}

func (s *stubOAuthVaultStore) Delete(_ context.Context, userID string, name string) error {
	delete(s.data, s.key(userID, name))
	return nil
}

func (s *stubOAuthVaultStore) Lookup(_ context.Context, userID string, name string) (string, bool, error) {
	value, ok := s.data[s.key(userID, name)]
	return value, ok, nil
}

func (s *stubOAuthVaultStore) LoadEnvForAgentProject(_ context.Context, userID string, _ string, _ string) (map[string]string, error) {
	out := make(map[string]string)
	prefix := userID + ":"
	for k, v := range s.data {
		if len(k) <= len(prefix) || k[:len(prefix)] != prefix {
			continue
		}
		out[k[len(prefix):]] = v
	}
	return out, nil
}

// TestResolveSessionRequiresUserRoot tests that ResolveSession fails without a UserRoot.
func TestResolveSessionRequiresUserRoot(t *testing.T) {
	_, err := ResolveSession(context.Background(), Config{
		Paths: Paths{
			AgentRoot: "/workspace/agent",
			// UserRoot intentionally omitted
		},
	})
	if err == nil {
		t.Fatal("expected error when UserRoot is missing")
	}
}

func TestSandboxProcessEnvLeavesHomeUnsetForDocker(t *testing.T) {
	cfg := Config{
		Paths: Paths{
			StellaHome: "/stella",
			AgentRoot:  "/workspace/agent",
			UserRoot:   "/workspace/agent/users/1",
		},
	}

	paths, err := ResolvePaths(cfg)
	if err != nil {
		t.Fatalf("ResolvePaths: %v", err)
	}
	env := ProcessEnv(paths)
	if _, ok := env["HOME"]; ok {
		t.Fatalf("HOME should not be set for docker backend; got %q", env["HOME"])
	}
	if got := env["STELLA_HOME"]; got != cfg.Paths.StellaHome {
		t.Fatalf("STELLA_HOME = %q, want %q", got, cfg.Paths.StellaHome)
	}
}

func TestCopyLocalHostEnvAllowlist(t *testing.T) {
	t.Setenv("STELLA_TEST_SECRET", "must-not-leak")
	t.Setenv("LANG", "C.UTF-8")
	t.Setenv("HTTPS_PROXY", "http://proxy.example:8080")

	env := map[string]string{}
	pkgsandbox.HostEnvCopy(env)

	if _, ok := env["STELLA_TEST_SECRET"]; ok {
		t.Fatal("local sandbox env copied non-allowlisted host variable")
	}
	if got := env["LANG"]; got != "C.UTF-8" {
		t.Fatalf("LANG = %q, want allowlisted host value", got)
	}
	if got := env["HTTPS_PROXY"]; got != "http://proxy.example:8080" {
		t.Fatalf("HTTPS_PROXY = %q, want allowlisted proxy value", got)
	}
}

func TestLocalSandboxPathAllowed(t *testing.T) {
	stellaBin := "/home/me/.stella/bin"
	for _, entry := range []string{
		stellaBin,
		"/usr/bin",
		"/usr/local/bin",
		"/bin",
		"/sbin",
		"/nix/store/abc/bin",
		"/run/current-system/sw/bin",
	} {
		if !pkgsandbox.HostEnvPathAllowed(entry, stellaBin) {
			t.Fatalf("expected %q to be allowed", entry)
		}
	}
	for _, entry := range []string{"", "/home/me/bin", "/tmp/bin", "/binary"} {
		if pkgsandbox.HostEnvPathAllowed(entry, stellaBin) {
			t.Fatalf("expected %q to be rejected", entry)
		}
	}
}

// TestSyncSessionNoop verifies that SyncSession is a no-op for nil and
// for sessions that don't implement Sync.
func TestSyncSessionNoop(t *testing.T) {
	if err := SyncSession(nil); err != nil {
		t.Errorf("SyncSession(nil): %v", err)
	}

	nop := pkgsandbox.NopSession()
	if err := SyncSession(nop); err != nil {
		t.Errorf("SyncSession(nop): %v", err)
	}
}

// TestResolveSessionDockerUnreachableDaemonReturnsError verifies that ResolveSession
// routes to createDockerSession and fails with a docker-related error when the daemon
// is unreachable.
func TestResolveSessionDockerUnreachableDaemonReturnsError(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///nonexistent/stella-test-docker.sock")
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")

	workspace := t.TempDir()
	userRoot := workspace + "/users/1"
	_, err := ResolveSession(context.Background(), Config{
		Paths: Paths{
			AgentRoot: workspace,
			UserRoot:  userRoot,
		},
		SandboxConfig:    config.SandboxConfig{},
		SandboxBackendFn: func(_ context.Context) string { return config.SandboxBackendDocker },
	})
	if err == nil {
		t.Fatal("expected error for docker backend with unreachable daemon")
	}
	if !strings.Contains(err.Error(), "docker") {
		t.Fatalf("expected error to mention 'docker', got: %v", err)
	}
}

// TestBuildSandboxEnv_vaultSecretsInjected verifies that vault secrets appear
// in the sandbox env and that runner-set vars (STELLA_HOME) take precedence over
// any same-named vault entry.
func TestRunnerFilesystemPolicyUsesUserScopedTmp(t *testing.T) {
	paths := Paths{UserRoot: "/workspace/users/42", WorkDir: "/workspace/users/42"}
	policy := runnerFilesystemPolicy(paths, Config{UserID: "42"})
	base := os.TempDir()
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	want := filepath.Join(base, "users", "42")
	if policy.TempDirHost != want {
		t.Fatalf("TempDirHost = %q, want %q", policy.TempDirHost, want)
	}
	for _, unsafeID := range []string{"../42", ".", "..", "a/b", `a\\b`} {
		if got := runnerFilesystemPolicy(paths, Config{UserID: unsafeID}).TempDirHost; got != "" {
			t.Fatalf("unsafe user id %q should not produce temp dir, got %q", unsafeID, got)
		}
	}
}

// A group session carries both a GroupID and a synthetic UserID equal to it. It
// must key off the group under the "group-" prefix in the users tree, so a real
// user whose ID equals the group ID can never share the group's writable
// mise/temp trees (#442).
func TestRunnerFilesystemPolicyGroupUsesGroupSubtree(t *testing.T) {
	userData := filepath.Join("/stella", "users", "group-g7", "data")
	paths := Paths{
		StellaHome:    "/stella",
		UserRoot:      "/stella/users/group-g7",
		UserDataDir:   userData,
		WorkspaceRoot: "/stella/users/group-g7/agents/a1",
		WorkDir:       "/stella/users/group-g7/agents/a1",
	}
	cfg := Config{GroupID: "g7", UserID: "g7", AgentID: "a1"}

	// The mise tree lives under the group's home in the STELLA_HOME frame (a
	// sibling of data, not inside it), so it shares the system tree's sandbox root
	// once STELLA_HOME is remapped. The group-keying still gates validity (a real
	// user with the same raw ID can't share it).
	if want := filepath.Join("/stella", "users", "group-g7", ".mise-tools"); miseUserDirHost(paths, cfg) != want {
		t.Fatalf("miseUserDirHost = %q, want %q", miseUserDirHost(paths, cfg), want)
	}
	base := os.TempDir()
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	if want := filepath.Join(base, "users", "group-g7"); runnerFilesystemPolicy(paths, cfg).TempDirHost != want {
		t.Fatalf("TempDirHost = %q, want %q", runnerFilesystemPolicy(paths, cfg).TempDirHost, want)
	}
}

func TestBuildSandboxEnv_vaultSecretsInjected(t *testing.T) {
	cfg := Config{
		Paths: Paths{
			StellaHome: "/stella",
			AgentRoot:  "/workspace/agent",
			UserRoot:   "/workspace/users/1",
		},
		UserID:  "42",
		AgentID: "a1",
		VaultEnvLoader: &stubVaultLoader{
			env: map[string]string{
				"MY_SECRET":   "s3cr3t",
				"STELLA_HOME": "should-be-overridden", // runner var must win
			},
		},
	}

	paths, err := ResolvePaths(cfg)
	if err != nil {
		t.Fatalf("ResolvePaths: %v", err)
	}

	env, err := buildSandboxEnv(context.Background(), cfg, paths)
	if err != nil {
		t.Fatalf("buildSandboxEnv: %v", err)
	}

	// Vault secret must be present.
	if got := env["MY_SECRET"]; got != "s3cr3t" {
		t.Errorf("MY_SECRET = %q, want %q", got, "s3cr3t")
	}

	// Runner var (STELLA_HOME) must override any same-named vault entry.
	if got := env["STELLA_HOME"]; got != cfg.Paths.StellaHome {
		t.Errorf("STELLA_HOME = %q, want %q (runner var must take precedence)", got, cfg.Paths.StellaHome)
	}
}

// TestBuildSandboxEnv_noVaultLoader verifies that buildSandboxEnv behaves
// correctly (returns runner env vars) when no vault loader is configured.
func TestBuildSandboxEnv_noVaultLoader(t *testing.T) {
	cfg := Config{
		Paths: Paths{
			StellaHome: "/stella",
			AgentRoot:  "/workspace/agent",
			UserRoot:   "/workspace/users/1",
		},
	}

	paths, err := ResolvePaths(cfg)
	if err != nil {
		t.Fatalf("ResolvePaths: %v", err)
	}

	env, err := buildSandboxEnv(context.Background(), cfg, paths)
	if err != nil {
		t.Fatalf("buildSandboxEnv: %v", err)
	}

	if got := env["STELLA_HOME"]; got != cfg.Paths.StellaHome {
		t.Errorf("STELLA_HOME = %q, want %q", got, cfg.Paths.StellaHome)
	}
}

// TestBuildSandboxEnv_OAuthBundleKeysStripped verifies that vault entries for
// OAuth bundle keys are not forwarded into the sandbox environment, even when
// present in the vault.
func TestBuildSandboxEnv_OAuthBundleKeysStripped(t *testing.T) {
	cfg := Config{
		Paths: Paths{
			StellaHome: "/stella",
			AgentRoot:  "/workspace/agent",
			UserRoot:   "/workspace/users/1",
		},
		UserID:  "1",
		AgentID: "a1",
		VaultEnvLoader: &stubVaultLoader{
			env: map[string]string{
				"GH_OAUTH":         `{"version":1,"access_token":"ghp_secret"}`,
				"LARK_CLI_OAUTH":   `{"version":1,"access_token":"u-lark-secret"}`,
				"FEISHU_CLI_OAUTH": `{"version":1,"access_token":"u-feishu-secret"}`,
				"OTHER_SECRET":     "should-pass-through",
			},
		},
	}

	paths, err := ResolvePaths(cfg)
	if err != nil {
		t.Fatalf("ResolvePaths: %v", err)
	}

	env, err := buildSandboxEnv(context.Background(), cfg, paths)
	if err != nil {
		t.Fatalf("buildSandboxEnv: %v", err)
	}

	if _, ok := env["GH_OAUTH"]; ok {
		t.Error("GH_OAUTH must not appear in sandbox env")
	}
	if _, ok := env["LARK_CLI_OAUTH"]; ok {
		t.Error("LARK_CLI_OAUTH must not appear in sandbox env")
	}
	if _, ok := env["FEISHU_CLI_OAUTH"]; ok {
		t.Error("FEISHU_CLI_OAUTH must not appear in sandbox env")
	}

	// Unrelated vault entries must still pass through.
	if got := env["OTHER_SECRET"]; got != "should-pass-through" {
		t.Errorf("OTHER_SECRET = %q, want %q", got, "should-pass-through")
	}
}

func TestBuildSandboxEnv_RuntimeOAuthEnvInjected(t *testing.T) {
	ctx := context.Background()
	store := newStubOAuthVaultStore()
	userID := "7"
	now := time.Now().UTC().Truncate(time.Second)

	registry := oauth.NewProviderRegistry()
	registry.Register(oauth.ProviderConfig{ID: "github", VaultKey: oauth.VaultKeyGitHub})
	registry.Register(oauth.ProviderConfig{ID: "lark", VaultKey: oauth.VaultKeyLark})
	tm := oauth.NewTokenManager(store)
	tm.SetRegistry(registry)

	if err := oauth.SaveOAuthBundle(ctx, store, userID, oauth.VaultKeyGitHub, oauth.OAuthBundle{
		Version:     1,
		AccessToken: "ghp_runtime_token",
	}); err != nil {
		t.Fatalf("SaveOAuthBundle: %v", err)
	}
	if err := oauth.SaveOAuthBundle(ctx, store, userID, oauth.VaultKeyLark, oauth.OAuthBundle{
		Version:          1,
		ClientID:         "lark_app_id",
		ClientSecret:     "lark_app_secret",
		Brand:            "feishu",
		AccessToken:      "lark_access_token",
		RefreshToken:     "lark_refresh_token",
		AccessExpiresAt:  now.Add(2 * time.Hour),
		RefreshExpiresAt: now.Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("SaveOAuthBundle: %v", err)
	}
	if err := store.Set(ctx, userID, "OTHER_SECRET", "still-present"); err != nil {
		t.Fatalf("Set OTHER_SECRET: %v", err)
	}

	cfg := Config{
		Paths: Paths{
			StellaHome: "/stella",
			AgentRoot:  "/workspace/agent",
			UserRoot:   "/workspace/users/1",
		},
		UserID:         userID,
		AgentID:        "a1",
		VaultEnvLoader: store,
		TokenManager:   tm,
		SessionEnvSpecs: []pkgplugins.SessionEnvSpec{
			{EnvVar: "GH_TOKEN", Source: pkgplugins.SessionEnvSource("oauth.access_token"), OAuthProviderID: "github"},
			{EnvVar: "LARKSUITE_CLI_USER_ACCESS_TOKEN", Source: pkgplugins.SessionEnvSource("oauth.access_token"), OAuthProviderID: "lark"},
			{EnvVar: "LARKSUITE_CLI_APP_ID", Source: pkgplugins.SessionEnvSource("oauth.client_id"), OAuthProviderID: "lark"},
			{EnvVar: "LARKSUITE_CLI_BRAND", Source: pkgplugins.SessionEnvSource("oauth.brand"), OAuthProviderID: "lark"},
		},
	}

	paths, err := ResolvePaths(cfg)
	if err != nil {
		t.Fatalf("ResolvePaths: %v", err)
	}

	env, err := buildSandboxEnv(ctx, cfg, paths)
	if err != nil {
		t.Fatalf("buildSandboxEnv: %v", err)
	}
	if _, ok := env[oauth.VaultKeyGitHub]; ok {
		t.Fatalf("%s must not appear in sandbox env", oauth.VaultKeyGitHub)
	}
	if _, ok := env[oauth.VaultKeyLark]; ok {
		t.Fatalf("%s must not appear in sandbox env", oauth.VaultKeyLark)
	}
	if got := env["GH_TOKEN"]; got != "ghp_runtime_token" {
		t.Fatalf("GH_TOKEN = %q, want %q", got, "ghp_runtime_token")
	}
	if got := env["LARKSUITE_CLI_USER_ACCESS_TOKEN"]; got != "lark_access_token" {
		t.Fatalf("LARKSUITE_CLI_USER_ACCESS_TOKEN = %q, want %q", got, "lark_access_token")
	}
	if got := env["LARKSUITE_CLI_APP_ID"]; got != "lark_app_id" {
		t.Fatalf("LARKSUITE_CLI_APP_ID = %q, want %q", got, "lark_app_id")
	}
	if got := env["LARKSUITE_CLI_BRAND"]; got != "feishu" {
		t.Fatalf("LARKSUITE_CLI_BRAND = %q, want %q", got, "feishu")
	}
	if got := env["OTHER_SECRET"]; got != "still-present" {
		t.Fatalf("OTHER_SECRET = %q, want %q", got, "still-present")
	}
}

func TestBuildSandboxEnv_TokenInjectionErrorsAreSkipped(t *testing.T) {
	ctx := context.Background()
	store := newStubOAuthVaultStore()
	userID := "9"

	registry := oauth.NewProviderRegistry()
	registry.Register(oauth.ProviderConfig{ID: "github", VaultKey: oauth.VaultKeyGitHub})
	registry.Register(oauth.ProviderConfig{ID: "lark", VaultKey: oauth.VaultKeyLark})
	tm := oauth.NewTokenManager(store)
	tm.SetRegistry(registry)

	if err := store.Set(ctx, userID, oauth.VaultKeyGitHub, `{"version":1,"client_id":"","client_secret":"","access_token":""}`); err != nil {
		t.Fatalf("Set GH_OAUTH: %v", err)
	}
	if err := store.Set(ctx, userID, oauth.VaultKeyLark, `{"version":1,"client_id":"app","client_secret":"secret","brand":"lark","access_token":"token","refresh_token":"refresh","access_expires_at":"2000-01-01T00:00:00Z","refresh_expires_at":"2000-01-01T00:00:00Z"}`); err != nil {
		t.Fatalf("Set LARK_CLI_OAUTH: %v", err)
	}

	cfg := Config{
		Paths: Paths{
			StellaHome: "/stella",
			AgentRoot:  "/workspace/agent",
			UserRoot:   "/workspace/users/1",
		},
		UserID:         userID,
		AgentID:        "a1",
		VaultEnvLoader: store,
		TokenManager:   tm,
		SessionEnvSpecs: []pkgplugins.SessionEnvSpec{
			{EnvVar: "GH_TOKEN", Source: pkgplugins.SessionEnvSource("oauth.access_token"), OAuthProviderID: "github"},
			{EnvVar: "LARKSUITE_CLI_USER_ACCESS_TOKEN", Source: pkgplugins.SessionEnvSource("oauth.access_token"), OAuthProviderID: "lark"},
		},
	}

	paths, err := ResolvePaths(cfg)
	if err != nil {
		t.Fatalf("ResolvePaths: %v", err)
	}

	env, err := buildSandboxEnv(ctx, cfg, paths)
	if err != nil {
		t.Fatalf("buildSandboxEnv: %v", err)
	}
	if _, ok := env["GH_TOKEN"]; ok {
		t.Fatal("GH_TOKEN should be skipped when access token is empty")
	}
	// Lark token is loaded by GetOAuthToken regardless of expiry (no refresh check).
	if got := env["LARKSUITE_CLI_USER_ACCESS_TOKEN"]; got != "token" {
		t.Fatalf("LARKSUITE_CLI_USER_ACCESS_TOKEN = %q, want %q", got, "token")
	}
	if _, ok := env[oauth.VaultKeyGitHub]; ok {
		t.Fatal("GH_OAUTH must still be stripped when token injection fails")
	}
	if _, ok := env[oauth.VaultKeyLark]; ok {
		t.Fatal("LARK_CLI_OAUTH must still be stripped when token injection fails")
	}
}
