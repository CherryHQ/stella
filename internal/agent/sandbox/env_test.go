package sandbox

import (
	"context"
	"maps"
	"testing"
)

type staticVaultEnv map[string]string

func (v staticVaultEnv) LoadEnv(context.Context, string) (map[string]string, error) {
	out := make(map[string]string, len(v))
	maps.Copy(out, v)
	return out, nil
}

type staticScopedToken struct {
	token string
}

func (s staticScopedToken) CreateScopedToken(context.Context, string, string, string, string) (string, error) {
	return s.token, nil
}

func TestBuildSandboxEnvUsesScopedTokenOverVaultToken(t *testing.T) {
	env, err := buildSandboxEnv(context.Background(), Config{
		UserID:         "user-1",
		AgentID:        "agent-1",
		SessionID:      "session-1",
		VaultEnvLoader: staticVaultEnv{"STELLA_TOKEN": "stella_legacy", "OTHER": "ok"},
		TokenEnsurer:   staticScopedToken{token: "stella_scoped_session"},
	}, Paths{})
	if err != nil {
		t.Fatalf("buildSandboxEnv: %v", err)
	}
	if got := env["STELLA_TOKEN"]; got != "stella_scoped_session" {
		t.Fatalf("STELLA_TOKEN = %q, want scoped token", got)
	}
	if got := env["OTHER"]; got != "ok" {
		t.Fatalf("OTHER = %q, want vault value", got)
	}
}

func TestBuildSandboxEnvDeletesVaultTokenWhenScopedUnavailable(t *testing.T) {
	env, err := buildSandboxEnv(context.Background(), Config{
		UserID:         "user-1",
		AgentID:        "agent-1",
		VaultEnvLoader: staticVaultEnv{"STELLA_TOKEN": "stella_legacy"},
	}, Paths{})
	if err != nil {
		t.Fatalf("buildSandboxEnv: %v", err)
	}
	if _, ok := env["STELLA_TOKEN"]; ok {
		t.Fatal("legacy vault STELLA_TOKEN must not be injected without a scoped token")
	}
}
