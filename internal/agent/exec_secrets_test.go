package agent

import (
	"context"
	"strings"
	"testing"

	agentsandbox "github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/internal/vault"
)

type fakeExecSecretVault struct {
	audited []string
}

func (v *fakeExecSecretVault) LoadEnvForAgentProject(context.Context, string, string, string) (map[string]string, error) {
	return nil, nil
}

func (v *fakeExecSecretVault) ListDeclarableForAgentProject(context.Context, string, string, string) ([]vault.DeclarableSecret, error) {
	return nil, nil
}

func (v *fakeExecSecretVault) ResolveDeclarableEnv(_ context.Context, _ string, _ string, _ string, names []string) (map[string]string, []string, error) {
	env := make(map[string]string, len(names))
	for _, name := range names {
		env[name] = "value-for-" + name
	}
	return env, []string{"A", "B"}, nil
}

func (v *fakeExecSecretVault) RecordExecSecretUse(_ context.Context, _ string, _ string, _ string, name string, command string) error {
	v.audited = append(v.audited, name+":"+command)
	return nil
}

func TestNewExecSecretResolverWithoutVaultLoaderOrSessionSecretsIsTrueNil(t *testing.T) {
	if r := newExecSecretResolver(agentsandbox.Config{}); r != nil {
		t.Fatalf("resolver = %#v, want nil interface", r)
	}
}

func TestNewExecSecretResolverWithSessionSecretsOnlyRedacts(t *testing.T) {
	secretValues := agentsandbox.NewSessionSecretValues()
	secretValues.Set([]string{"scoped-token"})
	resolver := newExecSecretResolver(agentsandbox.Config{SessionSecretValues: secretValues})
	if resolver == nil {
		t.Fatal("resolver is nil, want redaction-only resolver")
	}

	values, err := resolver.SessionSecretValues(context.Background())
	if err != nil {
		t.Fatalf("SessionSecretValues: %v", err)
	}
	if len(values) != 1 || values[0] != "scoped-token" {
		t.Fatalf("session secret values = %#v, want scoped token", values)
	}
	_, _, err = resolver.ResolveExecSecrets(context.Background(), []string{"A"}, "echo ok")
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("err = %v, want graceful unavailable error", err)
	}
}

func TestExecSecretResolverGroupSessionStillReturnsRedactionValues(t *testing.T) {
	secretValues := agentsandbox.NewSessionSecretValues()
	secretValues.Set([]string{"group-scoped-token"})
	resolver := newExecSecretResolver(agentsandbox.Config{UserID: "user-1", GroupID: "group-1", AgentID: "agent-1", VaultEnvLoader: &fakeExecSecretVault{}, SessionSecretValues: secretValues})

	values, err := resolver.SessionSecretValues(context.Background())
	if err != nil {
		t.Fatalf("SessionSecretValues: %v", err)
	}
	if len(values) != 1 || values[0] != "group-scoped-token" {
		t.Fatalf("session secret values = %#v, want group scoped token", values)
	}
	_, _, err = resolver.ResolveExecSecrets(context.Background(), []string{"A"}, "echo ok")
	if err == nil || !strings.Contains(err.Error(), "group sessions") {
		t.Fatalf("err = %v, want group session rejection", err)
	}
}

func TestExecSecretResolverAuditsEachDeclaredSecret(t *testing.T) {
	vault := &fakeExecSecretVault{}
	resolver := &execSecretResolver{cfg: agentsandbox.Config{UserID: "user-1", AgentID: "agent-1", SessionID: "session-1", VaultEnvLoader: vault}}

	env, _, err := resolver.ResolveExecSecrets(context.Background(), []string{"A", "B"}, "echo ok")
	if err != nil {
		t.Fatalf("ResolveExecSecrets: %v", err)
	}
	if env["A"] == "" || env["B"] == "" {
		t.Fatalf("env = %#v, want both secrets", env)
	}
	if len(vault.audited) != 2 {
		t.Fatalf("audit rows = %#v, want 2", vault.audited)
	}
}

func TestExecSecretResolverRejectsGroupSession(t *testing.T) {
	resolver := &execSecretResolver{cfg: agentsandbox.Config{UserID: "user-1", GroupID: "group-1", AgentID: "agent-1", VaultEnvLoader: &fakeExecSecretVault{}}}

	_, _, err := resolver.ResolveExecSecrets(context.Background(), []string{"A"}, "echo ok")
	if err == nil || !strings.Contains(err.Error(), "group sessions") {
		t.Fatalf("err = %v, want group session rejection", err)
	}
}
