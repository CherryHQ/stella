package sandbox

import (
	"context"
	"maps"
	"testing"

	"github.com/CherryHQ/stella/internal/vault"
)

// noDeclarableSecrets stubs the declarable-secret surface of VaultEnvLoader
// for tests that only exercise env assembly.
type noDeclarableSecrets struct{}

func (noDeclarableSecrets) ListDeclarableForAgentProject(context.Context, string, string, string) ([]vault.DeclarableSecret, error) {
	return nil, nil
}

func (noDeclarableSecrets) ResolveDeclarableEnv(context.Context, string, string, string, []string) (map[string]string, []string, error) {
	return nil, nil, nil
}

func (noDeclarableSecrets) RecordExecSecretUse(context.Context, string, string, string, string, string) error {
	return nil
}

type staticVaultEnv struct {
	noDeclarableSecrets
	env map[string]string
}

func (v staticVaultEnv) LoadEnvForAgentProject(context.Context, string, string, string) (map[string]string, error) {
	out := make(map[string]string, len(v.env))
	maps.Copy(out, v.env)
	return out, nil
}

func TestBuildSandboxEnvDropsVaultStellaToken(t *testing.T) {
	env, err := buildSandboxEnv(context.Background(), Config{
		UserID:         "user-1",
		AgentID:        "agent-1",
		SessionID:      "session-1",
		VaultEnvLoader: staticVaultEnv{env: map[string]string{stellaTokenEnvName(): "stella_legacy", "OTHER": "ok"}},
	}, Paths{})
	if err != nil {
		t.Fatalf("buildSandboxEnv: %v", err)
	}
	if _, ok := env[stellaTokenEnvName()]; ok {
		t.Fatal("legacy vault token must not be injected")
	}
	if got := env["OTHER"]; got != "ok" {
		t.Fatalf("OTHER = %q, want vault value", got)
	}
}
