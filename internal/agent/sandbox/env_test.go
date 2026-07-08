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

func requireSessionSecretValues(t *testing.T, values []string, present []string, absent []string) {
	t.Helper()
	got := make(map[string]struct{}, len(values))
	for _, value := range values {
		got[value] = struct{}{}
	}
	for _, value := range present {
		if _, ok := got[value]; !ok {
			t.Fatalf("session secret values = %#v, missing %q", values, value)
		}
	}
	for _, value := range absent {
		if _, ok := got[value]; ok {
			t.Fatalf("session secret values = %#v, unexpectedly contains %q", values, value)
		}
	}
	if len(values) != len(present) {
		t.Fatalf("session secret values = %#v, want exactly %#v", values, present)
	}
}

func TestBuildSandboxEnvDropsVaultStellaToken(t *testing.T) {
	env, err := buildSandboxEnv(context.Background(), Config{
		UserID:         "user-1",
		AgentID:        "agent-1",
		SessionID:      "session-1",
		VaultEnvLoader: staticVaultEnv{env: map[string]string{"STELLA_TOKEN": "stella_legacy", "OTHER": "ok"}},
	}, Paths{})
	if err != nil {
		t.Fatalf("buildSandboxEnv: %v", err)
	}
	if _, ok := env["STELLA_TOKEN"]; ok {
		t.Fatal("legacy vault token must not be injected")
	}
	if got := env["OTHER"]; got != "ok" {
		t.Fatalf("OTHER = %q, want vault value", got)
	}
}

func TestBuildSandboxEnvRecordsOnlyInjectedVaultSecretValues(t *testing.T) {
	secretValues := NewSessionSecretValues()
	env, err := buildSandboxEnv(context.Background(), Config{
		UserID:              "user-1",
		AgentID:             "agent-1",
		SessionID:           "session-1",
		VaultEnvLoader:      staticVaultEnv{env: map[string]string{"MY_SECRET": "vault-secret", "STELLA_HOME": "vault-home", "STELLA_TOKEN": "stella_legacy"}},
		SessionSecretValues: secretValues,
	}, Paths{StellaHome: "/runtime/stella"})
	if err != nil {
		t.Fatalf("buildSandboxEnv: %v", err)
	}
	if got := env["MY_SECRET"]; got != "vault-secret" {
		t.Fatalf("MY_SECRET = %q, want vault-secret", got)
	}
	values := secretValues.Values()
	requireSessionSecretValues(t, values,
		[]string{"vault-secret"},
		[]string{"vault-home", "stella_legacy", "/runtime/stella"},
	)
}

func TestBuildSandboxEnvDeletesVaultTokenWhenScopedUnavailable(t *testing.T) {
	env, err := buildSandboxEnv(context.Background(), Config{
		UserID:         "user-1",
		AgentID:        "agent-1",
		VaultEnvLoader: staticVaultEnv{env: map[string]string{"STELLA_TOKEN": "stella_legacy"}},
	}, Paths{})
	if err != nil {
		t.Fatalf("buildSandboxEnv: %v", err)
	}
	if _, ok := env["STELLA_TOKEN"]; ok {
		t.Fatal("legacy vault token must not be injected")
	}
}
