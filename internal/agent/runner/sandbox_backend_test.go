package runner

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vaayne/anna/internal/config"
	oauth "github.com/vaayne/anna/internal/credentials/oauth"
)

// stubVaultLoader is a test-only VaultEnvLoader that returns a fixed map.
type stubVaultLoader struct {
	env map[string]string
	err error
}

func (s *stubVaultLoader) LoadEnv(_ context.Context, _ int64) (map[string]string, error) {
	return s.env, s.err
}

type stubOAuthVaultStore struct {
	data map[string]string
}

func newStubOAuthVaultStore() *stubOAuthVaultStore {
	return &stubOAuthVaultStore{data: make(map[string]string)}
}

func (s *stubOAuthVaultStore) key(userID int64, name string) string {
	return fmt.Sprintf("%d:%s", userID, name)
}

func (s *stubOAuthVaultStore) Set(_ context.Context, userID int64, name string, plaintext string) error {
	s.data[s.key(userID, name)] = plaintext
	return nil
}

func (s *stubOAuthVaultStore) Delete(_ context.Context, userID int64, name string) error {
	delete(s.data, s.key(userID, name))
	return nil
}

func (s *stubOAuthVaultStore) LoadEnv(_ context.Context, userID int64) (map[string]string, error) {
	out := make(map[string]string)
	prefix := fmt.Sprintf("%d:", userID)
	for k, v := range s.data {
		if len(k) <= len(prefix) || k[:len(prefix)] != prefix {
			continue
		}
		out[k[len(prefix):]] = v
	}
	return out, nil
}

// TestResolveSessionRequiresUserRoot tests that resolveSession fails without a UserRoot.
func TestResolveSessionRequiresUserRoot(t *testing.T) {
	_, err := resolveSession(context.Background(), GoRunnerConfig{
		AgentRoot: "/workspace/agent",
		// UserRoot intentionally omitted
	})
	if err == nil {
		t.Fatal("expected error when UserRoot is missing")
	}
}

func TestResolveRunnerPathsDefaultsWorkDirToUserRoot(t *testing.T) {
	cfg := GoRunnerConfig{
		AgentRoot: "/workspace/agent",
		UserRoot:  "/workspace/agent/users/1",
	}

	paths := resolveRunnerPaths(cfg)
	if got := resolveSandboxWorkingDir(cfg, cfg.UserRoot); got != cfg.UserRoot {
		t.Fatalf("resolveSandboxWorkingDir() = %q, want %q", got, cfg.UserRoot)
	}
	if paths.UserRoot != cfg.UserRoot {
		t.Fatalf("UserRoot = %q, want %q", paths.UserRoot, cfg.UserRoot)
	}
}

func TestSandboxProcessEnvLeavesHomeUnsetForDocker(t *testing.T) {
	cfg := GoRunnerConfig{
		AnnaHome:  "/anna",
		AgentRoot: "/workspace/agent",
		UserRoot:  "/workspace/agent/users/1",
	}

	paths, err := resolveSandboxPaths(cfg)
	if err != nil {
		t.Fatalf("resolveSandboxPaths: %v", err)
	}
	env := sandboxProcessEnv(paths)
	if _, ok := env["HOME"]; ok {
		t.Fatalf("HOME should not be set for docker backend; got %q", env["HOME"])
	}
	if got := env["ANNA_HOME"]; got != cfg.AnnaHome {
		t.Fatalf("ANNA_HOME = %q, want %q", got, cfg.AnnaHome)
	}
}

// TestRunnerSessionLifecycle tests the runnerSession lifecycle using a nil session
// (alwaysAlive=true path) to avoid requiring a live sandbox backend.
func TestRunnerSessionLifecycle(t *testing.T) {
	rs := &runnerSession{
		alwaysAlive: true,
	}

	// Test Alive
	if !rs.Alive() {
		t.Error("session should be alive")
	}

	// Test Done channel — nil session returns an already-closed channel.
	select {
	case <-rs.Done():
		// Expected: nil session Done() is immediately closed.
	default:
		t.Error("nil-session Done() should be closed")
	}

	// Test Close (no-op for nil inner session)
	if err := rs.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// TestResolveSessionDockerUnreachableDaemonReturnsError verifies that resolveSession
// routes to createDockerSession and fails with a docker-related error when the daemon
// is unreachable.
func TestResolveSessionDockerUnreachableDaemonReturnsError(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///nonexistent/anna-test-docker.sock")
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")

	workspace := t.TempDir()
	userRoot := workspace + "/users/1"
	_, err := resolveSession(context.Background(), GoRunnerConfig{
		AgentRoot: workspace,
		UserRoot:  userRoot,
		Sandbox:   config.SandboxConfig{},
	})
	if err == nil {
		t.Fatal("expected error for docker backend with unreachable daemon")
	}
	if !strings.Contains(err.Error(), "docker") {
		t.Fatalf("expected error to mention 'docker', got: %v", err)
	}
}

// TestRunnerSessionNilHandling tests nil session safety.
func TestRunnerSessionNilHandling(t *testing.T) {
	var rs *runnerSession

	// Should handle nil gracefully
	if rs.Alive() {
		t.Error("nil session should not be alive")
	}

	// Done should return closed channel for nil
	done := rs.Done()
	select {
	case <-done:
		// Expected
	default:
		t.Error("nil session Done() should be closed")
	}

	// Close should not panic
	if err := rs.Close(); err != nil {
		t.Errorf("Close on nil: %v", err)
	}
}

// TestBuildSandboxEnv_vaultSecretsInjected verifies that vault secrets appear
// in the sandbox env and that runner-set vars (ANNA_HOME) take precedence over
// any same-named vault entry.
func TestBuildSandboxEnv_vaultSecretsInjected(t *testing.T) {
	cfg := GoRunnerConfig{
		AnnaHome:  "/anna",
		AgentRoot: "/workspace/agent",
		UserRoot:  "/workspace/users/1",
		UserID:    42,
		VaultEnvLoader: &stubVaultLoader{
			env: map[string]string{
				"MY_SECRET": "s3cr3t",
				"ANNA_HOME": "should-be-overridden", // runner var must win
			},
		},
	}

	paths, err := resolveSandboxPaths(cfg)
	if err != nil {
		t.Fatalf("resolveSandboxPaths: %v", err)
	}

	env := buildSandboxEnv(context.Background(), cfg, paths)

	// Vault secret must be present.
	if got := env["MY_SECRET"]; got != "s3cr3t" {
		t.Errorf("MY_SECRET = %q, want %q", got, "s3cr3t")
	}

	// Runner var (ANNA_HOME) must override any same-named vault entry.
	if got := env["ANNA_HOME"]; got != cfg.AnnaHome {
		t.Errorf("ANNA_HOME = %q, want %q (runner var must take precedence)", got, cfg.AnnaHome)
	}
}

// TestBuildSandboxEnv_noVaultLoader verifies that buildSandboxEnv behaves
// correctly (returns runner env vars) when no vault loader is configured.
func TestBuildSandboxEnv_noVaultLoader(t *testing.T) {
	cfg := GoRunnerConfig{
		AnnaHome:  "/anna",
		AgentRoot: "/workspace/agent",
		UserRoot:  "/workspace/users/1",
	}

	paths, err := resolveSandboxPaths(cfg)
	if err != nil {
		t.Fatalf("resolveSandboxPaths: %v", err)
	}

	env := buildSandboxEnv(context.Background(), cfg, paths)

	if got := env["ANNA_HOME"]; got != cfg.AnnaHome {
		t.Errorf("ANNA_HOME = %q, want %q", got, cfg.AnnaHome)
	}
}

// TestBuildSandboxEnv_OAuthBundleKeysStripped verifies that vault entries for
// the OAuth bundle keys (GH_OAUTH and LARK_CLI_OAUTH) are not forwarded into
// the sandbox environment, even when present in the vault.
func TestBuildSandboxEnv_OAuthBundleKeysStripped(t *testing.T) {
	cfg := GoRunnerConfig{
		AnnaHome:  "/anna",
		AgentRoot: "/workspace/agent",
		UserRoot:  "/workspace/users/1",
		UserID:    1,
		VaultEnvLoader: &stubVaultLoader{
			env: map[string]string{
				"GH_OAUTH":       `{"version":1,"access_token":"ghp_secret"}`,
				"LARK_CLI_OAUTH": `{"version":1,"access_token":"u-lark-secret"}`,
				"OTHER_SECRET":   "should-pass-through",
			},
		},
	}

	paths, err := resolveSandboxPaths(cfg)
	if err != nil {
		t.Fatalf("resolveSandboxPaths: %v", err)
	}

	env := buildSandboxEnv(context.Background(), cfg, paths)

	if _, ok := env["GH_OAUTH"]; ok {
		t.Error("GH_OAUTH must not appear in sandbox env")
	}
	if _, ok := env["LARK_CLI_OAUTH"]; ok {
		t.Error("LARK_CLI_OAUTH must not appear in sandbox env")
	}

	// Unrelated vault entries must still pass through.
	if got := env["OTHER_SECRET"]; got != "should-pass-through" {
		t.Errorf("OTHER_SECRET = %q, want %q", got, "should-pass-through")
	}
}

func TestBuildSandboxEnv_RuntimeOAuthEnvInjected(t *testing.T) {
	ctx := context.Background()
	store := newStubOAuthVaultStore()
	userID := int64(7)
	now := time.Now().UTC().Truncate(time.Second)

	if err := oauth.SaveGHBundle(ctx, store, userID, oauth.GHOAuthBundle{
		Version:     1,
		AccessToken: "ghp_runtime_token",
		TokenType:   "bearer",
	}); err != nil {
		t.Fatalf("SaveGHBundle: %v", err)
	}
	if err := oauth.SaveLarkBundle(ctx, store, userID, oauth.LarkOAuthBundle{
		Version:          1,
		AppID:            "lark_app_id",
		AppSecret:        "lark_app_secret",
		Brand:            "feishu",
		AccessToken:      "lark_access_token",
		RefreshToken:     "lark_refresh_token",
		AccessExpiresAt:  now.Add(2 * time.Hour),
		RefreshExpiresAt: now.Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("SaveLarkBundle: %v", err)
	}
	if err := store.Set(ctx, userID, "OTHER_SECRET", "still-present"); err != nil {
		t.Fatalf("Set OTHER_SECRET: %v", err)
	}

	cfg := GoRunnerConfig{
		AnnaHome:       "/anna",
		AgentRoot:      "/workspace/agent",
		UserRoot:       "/workspace/users/1",
		UserID:         userID,
		VaultEnvLoader: store,
		TokenManager:   oauth.NewTokenManager(store),
	}

	paths, err := resolveSandboxPaths(cfg)
	if err != nil {
		t.Fatalf("resolveSandboxPaths: %v", err)
	}

	env := buildSandboxEnv(ctx, cfg, paths)
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
