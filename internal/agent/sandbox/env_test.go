package sandbox

import (
	"context"
	"maps"
	"testing"
	"time"

	oauth "github.com/CherryHQ/stella/internal/connections/oauth"
	"github.com/CherryHQ/stella/internal/vault"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type staticVaultEnv struct {
	env map[string]string
}

func (v staticVaultEnv) LoadEnvForAgent(context.Context, string, string) (map[string]string, error) {
	out := make(map[string]string, len(v.env))
	maps.Copy(out, v.env)
	return out, nil
}

func (v staticVaultEnv) ListAmbientSecretMetas(context.Context, string, string) ([]vault.AmbientSecretMeta, error) {
	return nil, nil
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

func TestBuildSandboxEnvSharesLarkCLIStateAcrossUserAgents(t *testing.T) {
	userData := "/stella/users/user-1/data"
	env, err := buildSandboxEnv(context.Background(), Config{UserID: "user-1"}, Paths{
		WorkspaceRoot: "/stella/users/user-1/agents/agent-1",
		UserDataDir:   userData,
	})
	if err != nil {
		t.Fatalf("buildSandboxEnv: %v", err)
	}
	configDir := userData + "/.lark-cli"
	if got := env[LarkCLIConfigDirEnv]; got != configDir {
		t.Fatalf("%s = %q, want %q", LarkCLIConfigDirEnv, got, configDir)
	}
	if got := env[LarkCLIDataDirEnv]; got != configDir+"/data" {
		t.Fatalf("%s = %q, want %q", LarkCLIDataDirEnv, got, configDir+"/data")
	}

	otherAgentEnv, err := buildSandboxEnv(context.Background(), Config{UserID: "user-1"}, Paths{
		WorkspaceRoot: "/stella/users/user-1/agents/agent-2",
		UserDataDir:   userData,
	})
	if err != nil {
		t.Fatalf("buildSandboxEnv for second Agent: %v", err)
	}
	for _, key := range []string{LarkCLIConfigDirEnv, LarkCLIDataDirEnv} {
		if otherAgentEnv[key] != env[key] {
			t.Fatalf("%s differs across the user's Agents: %q vs %q", key, env[key], otherAgentEnv[key])
		}
	}
}

func TestBuildSandboxEnvDoesNotExposePersonalLarkCLIStateToGroups(t *testing.T) {
	env, err := buildSandboxEnv(context.Background(), Config{GroupID: "group-1"}, Paths{
		WorkspaceRoot: "/stella/users/group-group-1/agents/agent-1",
		UserDataDir:   "/stella/users/group-group-1/data",
	})
	if err != nil {
		t.Fatalf("buildSandboxEnv: %v", err)
	}
	if _, ok := env[LarkCLIConfigDirEnv]; ok {
		t.Fatalf("%s must not be set for a group session", LarkCLIConfigDirEnv)
	}
	if _, ok := env[LarkCLIDataDirEnv]; ok {
		t.Fatalf("%s must not be set for a group session", LarkCLIDataDirEnv)
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

func TestBuildSandboxEnvVaultSecretOverridesOAuthSessionEnv(t *testing.T) {
	for _, tt := range []struct {
		name          string
		vaultSecret   bool
		wantToken     string
		wantOAuthBind bool
		wantRedacted  []string
		absentSecrets []string
	}{
		{
			name:          "vault secret wins",
			vaultSecret:   true,
			wantToken:     "vault_pat",
			wantRedacted:  []string{"vault_pat"},
			absentSecrets: []string{"oauth_access_token"},
		},
		{
			name:          "oauth injects without vault collision",
			wantToken:     "oauth_access_token",
			wantOAuthBind: true,
			wantRedacted:  []string{"oauth_access_token"},
			absentSecrets: []string{"vault_pat"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			userID := "user-1"
			store := newStubOAuthVaultStore()
			registry := oauth.NewProviderRegistry()
			registry.Register(oauth.ProviderConfig{ID: "github", VaultKey: oauth.VaultKeyGitHub})
			tm := oauth.NewTokenManager(store)
			tm.SetRegistry(registry)
			if err := oauth.SaveOAuthBundle(ctx, store, userID, oauth.VaultKeyGitHub, oauth.OAuthBundle{
				Version:         1,
				AccessToken:     "oauth_access_token",
				AccessExpiresAt: time.Now().Add(time.Hour),
			}); err != nil {
				t.Fatalf("SaveOAuthBundle: %v", err)
			}
			if tt.vaultSecret {
				if err := store.Set(ctx, userID, "GH_TOKEN", "vault_pat"); err != nil {
					t.Fatalf("Set GH_TOKEN: %v", err)
				}
			}
			secretValues := NewSessionSecretValues()
			oauthBindings := NewOAuthEnvBindings()
			env, err := buildSandboxEnv(ctx, Config{
				UserID:              userID,
				AgentID:             "agent-1",
				VaultEnvLoader:      store,
				SessionSecretValues: secretValues,
				TokenManager:        tm,
				OAuthEnvBindings:    oauthBindings,
				SessionEnvSpecs: []pkgplugins.SessionEnvSpec{
					{EnvVar: "GH_TOKEN", Source: pkgplugins.SessionEnvSource("oauth.access_token"), OAuthProviderID: "github"},
				},
			}, Paths{})
			if err != nil {
				t.Fatalf("buildSandboxEnv: %v", err)
			}
			if got := env["GH_TOKEN"]; got != tt.wantToken {
				t.Fatalf("GH_TOKEN = %q, want %q", got, tt.wantToken)
			}
			if _, ok := env[oauth.VaultKeyGitHub]; ok {
				t.Fatalf("%s must not appear in sandbox env", oauth.VaultKeyGitHub)
			}
			if got := oauthBindings.Has("GH_TOKEN"); got != tt.wantOAuthBind {
				t.Fatalf("OAuth binding recorded = %v, want %v", got, tt.wantOAuthBind)
			}
			requireSessionSecretValues(t, secretValues.Values(), tt.wantRedacted, tt.absentSecrets)
		})
	}
}
