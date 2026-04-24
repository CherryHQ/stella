package credentials_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"

	"github.com/vaayne/anna/internal/credentials"
	oauth "github.com/vaayne/anna/internal/credentials/oauth"
	"github.com/vaayne/anna/internal/pluginhost"
	"github.com/vaayne/anna/pkg/memory"
)

// --- pluginhost.ConfigBackend stub ---

type stubPluginCfg struct {
	state  map[string]pkgplugins.PluginState
	getErr error
}

func newStubPluginCfg() *stubPluginCfg {
	return &stubPluginCfg{state: map[string]pkgplugins.PluginState{}}
}

func (s *stubPluginCfg) Get(_ context.Context, pluginID string) (pkgplugins.PluginState, error) {
	if s.getErr != nil {
		return pkgplugins.PluginState{}, s.getErr
	}
	ps, ok := s.state[pluginID]
	if !ok {
		return pkgplugins.PluginState{}, errors.New("plugin not found")
	}
	return ps, nil
}

func (s *stubPluginCfg) Set(_ context.Context, pluginID string, config map[string]any) error {
	ps := s.state[pluginID]
	ps.Config = config
	s.state[pluginID] = ps
	return nil
}

func (s *stubPluginCfg) SetEnabled(_ context.Context, pluginID string, enabled bool) error {
	ps := s.state[pluginID]
	ps.Enabled = enabled
	s.state[pluginID] = ps
	return nil
}

var _ pluginhost.ConfigBackend = (*stubPluginCfg)(nil)

// --- helpers ---

func newService(t *testing.T, cfg pluginhost.ConfigBackend) *credentials.Service {
	t.Helper()
	flowStore := oauth.NewFlowStore()
	return credentials.NewService(nil, cfg, flowStore, "http://localhost:8080")
}

func ctxWithUser(userID int64) context.Context {
	return memory.WithUserID(context.Background(), userID)
}

func testProviderConfig(id, vaultKey string) oauth.ProviderConfig {
	return oauth.ProviderConfig{
		ID:       id,
		VaultKey: vaultKey,
		Flows: []oauth.ProviderFlowConfig{{
			Type:          "device_code",
			DeviceAuthURL: "https://example.com/device",
			TokenURL:      "https://example.com/token",
		}},
	}
}

// --- Service tests ---

func TestAddSecretInstruction(t *testing.T) {
	svc := newService(t, newStubPluginCfg())
	inst := svc.AddSecretInstruction("OPENAI_API_KEY", "access the OpenAI API")
	if inst.Name != "OPENAI_API_KEY" {
		t.Errorf("Name = %q; want OPENAI_API_KEY", inst.Name)
	}
	if !strings.Contains(inst.Command, "/config OPENAI_API_KEY") {
		t.Errorf("Command %q does not contain expected prefix", inst.Command)
	}
	if inst.Purpose != "access the OpenAI API" {
		t.Errorf("Purpose = %q; want 'access the OpenAI API'", inst.Purpose)
	}
}

func TestListVaultNilVault(t *testing.T) {
	svc := newService(t, newStubPluginCfg())
	_, err := svc.ListVault(context.Background(), 1)
	if err == nil {
		t.Error("expected error when vault is nil")
	}
}

func TestDeleteVaultEntryNilVault(t *testing.T) {
	svc := newService(t, newStubPluginCfg())
	err := svc.DeleteVaultEntry(context.Background(), 1, "FOO")
	if err == nil {
		t.Error("expected error when vault is nil")
	}
}

func TestGetProviderStatusesPluginNotFound(t *testing.T) {
	cfg := newStubPluginCfg()
	svc := newService(t, cfg)

	registry := oauth.NewProviderRegistry()
	registry.Register(testProviderConfig("github", oauth.VaultKeyGitHub))
	registry.Register(testProviderConfig("lark", oauth.VaultKeyLark))
	svc.SetRegistry(registry)
	svc.SetProviderPluginIDs(map[string]string{"lark": "tool/lark-cli"})

	statuses := svc.GetProviderStatuses(context.Background(), 1)
	if len(statuses) == 0 {
		t.Error("expected at least one provider status")
	}
	for _, ps := range statuses {
		switch ps.Provider {
		case "github":
			if !ps.Available {
				t.Errorf("github should be available without admin plugin config: %+v", ps)
			}
		case "lark":
			if ps.Available {
				t.Errorf("lark should be unavailable when plugin not configured: %+v", ps)
			}
			if ps.Unavailable == "" {
				t.Error("lark missing unavailable reason")
			}
		}
	}
}

func TestGetProviderStatusesDisabledPlugin(t *testing.T) {
	cfg := newStubPluginCfg()
	cfg.state["tool/lark-cli"] = pkgplugins.PluginState{
		Enabled: false,
		Config:  map[string]any{"app_id": "cid", "app_secret": "csecret"},
	}
	svc := newService(t, cfg)

	registry := oauth.NewProviderRegistry()
	registry.Register(testProviderConfig("lark", oauth.VaultKeyLark))
	svc.SetRegistry(registry)
	svc.SetProviderPluginIDs(map[string]string{"lark": "tool/lark-cli"})

	statuses := svc.GetProviderStatuses(context.Background(), 1)
	for _, ps := range statuses {
		if ps.Provider == "lark" && ps.Available {
			t.Error("lark should be unavailable when plugin is disabled")
		}
	}
}

func TestStartFlowNilVault(t *testing.T) {
	svc := newService(t, newStubPluginCfg())
	_, err := svc.StartFlow(context.Background(), 1, "github")
	if err == nil {
		t.Error("expected error when vault is nil")
	}
}

func TestPollFlowUnknownFlow(t *testing.T) {
	svc := newService(t, newStubPluginCfg())
	_, _, err := svc.PollFlow(context.Background(), 1, "github", "nonexistent-flow-id")
	if err == nil {
		t.Error("expected error for unknown flow")
	}
}

func TestStartFlowUnsupportedProvider(t *testing.T) {
	svc := newService(t, newStubPluginCfg())
	_, err := svc.StartFlow(context.Background(), 1, "unsupported-provider")
	if err == nil {
		t.Error("expected error for unsupported provider")
	}
}

func TestDisconnectNilVault(t *testing.T) {
	svc := newService(t, newStubPluginCfg())
	err := svc.Disconnect(context.Background(), 1, "github")
	if err == nil {
		t.Error("expected error when vault is nil")
	}
}

func TestDisconnectUnsupportedProvider(t *testing.T) {
	svc := newService(t, newStubPluginCfg())
	err := svc.Disconnect(context.Background(), 1, "badprovider")
	if err == nil {
		t.Error("expected error for unsupported provider")
	}
}

func TestInvalidateUserNilInvalidator(t *testing.T) {
	svc := newService(t, newStubPluginCfg())
	if err := svc.InvalidateUser(42); err != nil {
		t.Errorf("InvalidateUser with nil invalidator should be a no-op, got %v", err)
	}
}

type stubInvalidator struct{ called int64 }

func (s *stubInvalidator) InvalidateUser(userID int64) error {
	s.called = userID
	return nil
}

func TestInvalidateUserCallsInvalidator(t *testing.T) {
	svc := newService(t, newStubPluginCfg())
	inv := &stubInvalidator{}
	svc.SetInvalidator(inv)
	if err := svc.InvalidateUser(99); err != nil {
		t.Fatal(err)
	}
	if inv.called != 99 {
		t.Errorf("InvalidateUser called with %d, want 99", inv.called)
	}
}

// --- OAuthTool tests ---

func newOAuthTool(t *testing.T) *credentials.OAuthTool {
	t.Helper()
	return credentials.NewOAuthTool(newService(t, newStubPluginCfg()))
}

func TestOAuthToolNoUserContext(t *testing.T) {
	tool := newOAuthTool(t)
	_, err := tool.Execute(context.Background(), map[string]any{"action": "status"})
	if err == nil {
		t.Error("expected error when no user in context")
	}
}

func TestOAuthToolUnknownAction(t *testing.T) {
	tool := newOAuthTool(t)
	_, err := tool.Execute(ctxWithUser(1), map[string]any{"action": "invalid"})
	if err == nil {
		t.Error("expected error for unknown action")
	}
}

func TestOAuthToolStatus(t *testing.T) {
	tool := newOAuthTool(t)
	out, err := tool.Execute(ctxWithUser(1), map[string]any{"action": "status"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "OAuth providers:") {
		t.Errorf("status output missing providers section: %q", out)
	}
}

func TestOAuthToolConnectMissingProvider(t *testing.T) {
	tool := newOAuthTool(t)
	_, err := tool.Execute(ctxWithUser(1), map[string]any{"action": "connect"})
	if err == nil {
		t.Error("expected error when provider missing for connect")
	}
}

func TestOAuthToolConnectPollMissingFlowID(t *testing.T) {
	// Providing an unknown flow_id should error, not silently start a new flow.
	svc := newService(t, newStubPluginCfg())
	tool := credentials.NewOAuthTool(svc)
	_, err := tool.Execute(ctxWithUser(1), map[string]any{"action": "connect", "provider": "github", "flow_id": "bad-id"})
	if err == nil {
		t.Error("expected error for unknown flow_id")
	}
}

func TestOAuthToolDisconnectMissingProvider(t *testing.T) {
	tool := newOAuthTool(t)
	_, err := tool.Execute(ctxWithUser(1), map[string]any{"action": "disconnect"})
	if err == nil {
		t.Error("expected error when provider missing for disconnect")
	}
}

func TestOAuthDefinition(t *testing.T) {
	def := credentials.OAuthDefinition()
	if def.Name != "oauth" {
		t.Errorf("OAuthDefinition.Name = %q; want oauth", def.Name)
	}
	if def.InputSchema == nil {
		t.Error("OAuthDefinition.InputSchema is nil")
	}
}

// --- VaultTool tests ---

func newVaultTool(t *testing.T) *credentials.VaultTool {
	t.Helper()
	return credentials.NewVaultTool(newService(t, newStubPluginCfg()))
}

func TestVaultToolNoUserContext(t *testing.T) {
	tool := newVaultTool(t)
	_, err := tool.Execute(context.Background(), map[string]any{"action": "list"})
	if err == nil {
		t.Error("expected error when no user in context")
	}
}

func TestVaultToolUnknownAction(t *testing.T) {
	tool := newVaultTool(t)
	_, err := tool.Execute(ctxWithUser(1), map[string]any{"action": "invalid"})
	if err == nil {
		t.Error("expected error for unknown action")
	}
}

func TestVaultToolListNoVault(t *testing.T) {
	tool := newVaultTool(t)
	_, err := tool.Execute(ctxWithUser(1), map[string]any{"action": "list"})
	if err == nil {
		t.Error("expected error listing when vault is nil")
	}
}

func TestVaultToolRemoveMissingName(t *testing.T) {
	tool := newVaultTool(t)
	_, err := tool.Execute(ctxWithUser(1), map[string]any{"action": "remove"})
	if err == nil {
		t.Error("expected error when name is missing for remove")
	}
}

func TestVaultToolAddMissingName(t *testing.T) {
	tool := newVaultTool(t)
	_, err := tool.Execute(ctxWithUser(1), map[string]any{"action": "add"})
	if err == nil {
		t.Error("expected error when name missing for add")
	}
}

func TestVaultToolAddInstruction(t *testing.T) {
	tool := newVaultTool(t)
	out, err := tool.Execute(ctxWithUser(1), map[string]any{
		"action":  "add",
		"name":    "STRIPE_KEY",
		"purpose": "process payments",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "/config STRIPE_KEY") {
		t.Errorf("add_secret output missing /config instruction: %q", out)
	}
	if !strings.Contains(out, "process payments") {
		t.Errorf("add_secret output missing purpose: %q", out)
	}
}

func TestVaultDefinition(t *testing.T) {
	def := credentials.VaultDefinition()
	if def.Name != "vault" {
		t.Errorf("VaultDefinition.Name = %q; want vault", def.Name)
	}
	if def.InputSchema == nil {
		t.Error("VaultDefinition.InputSchema is nil")
	}
}
