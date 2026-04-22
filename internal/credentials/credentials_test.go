package credentials_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"

	"github.com/vaayne/anna/internal/credentials"
	"github.com/vaayne/anna/internal/oauthcli"
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
	flowStore := oauthcli.NewFlowStore()
	return credentials.NewService(nil, cfg, flowStore, "http://localhost:8080")
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
	svc := newService(t, newStubPluginCfg())
	statuses := svc.GetProviderStatuses(context.Background(), 1)
	if len(statuses) == 0 {
		t.Error("expected at least one provider status")
	}
	for _, ps := range statuses {
		if ps.Available {
			t.Errorf("provider %s should be unavailable when plugin not configured", ps.Provider)
		}
		if ps.Unavailable == "" {
			t.Errorf("provider %s missing unavailable reason", ps.Provider)
		}
	}
}

func TestGetProviderStatusesDisabledPlugin(t *testing.T) {
	cfg := newStubPluginCfg()
	cfg.state["auth/github"] = pkgplugins.PluginState{
		Enabled: false,
		Config:  map[string]any{"client_id": "cid", "client_secret": "csecret"},
	}
	svc := newService(t, cfg)
	statuses := svc.GetProviderStatuses(context.Background(), 1)
	for _, ps := range statuses {
		if ps.Provider == "github" && ps.Available {
			t.Error("github should be unavailable when plugin is disabled")
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

// --- Tool tests ---

func ctxWithUser(userID int64) context.Context {
	return memory.WithUserID(context.Background(), userID)
}

func newTool(t *testing.T) *credentials.Tool {
	t.Helper()
	svc := newService(t, newStubPluginCfg())
	return credentials.NewTool(svc)
}

func TestToolNoUserContext(t *testing.T) {
	tool := newTool(t)
	_, err := tool.Execute(context.Background(), map[string]any{"action": "status"})
	if err == nil {
		t.Error("expected error when no user in context")
	}
}

func TestToolUnknownAction(t *testing.T) {
	tool := newTool(t)
	_, err := tool.Execute(ctxWithUser(1), map[string]any{"action": "invalid"})
	if err == nil {
		t.Error("expected error for unknown action")
	}
}

func TestToolStatus(t *testing.T) {
	tool := newTool(t)
	out, err := tool.Execute(ctxWithUser(1), map[string]any{"action": "status"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "OAuth providers:") {
		t.Errorf("status output missing providers section: %q", out)
	}
	if !strings.Contains(out, "Vault secrets:") {
		t.Errorf("status output missing vault section: %q", out)
	}
}

func TestToolListNoVault(t *testing.T) {
	tool := newTool(t)
	_, err := tool.Execute(ctxWithUser(1), map[string]any{"action": "list"})
	if err == nil {
		t.Error("expected error listing when vault is nil")
	}
}

func TestToolDeleteMissingName(t *testing.T) {
	tool := newTool(t)
	_, err := tool.Execute(ctxWithUser(1), map[string]any{"action": "delete"})
	if err == nil {
		t.Error("expected error when name is missing for delete")
	}
}

func TestToolOAuthStartMissingProvider(t *testing.T) {
	tool := newTool(t)
	_, err := tool.Execute(ctxWithUser(1), map[string]any{"action": "oauth_start"})
	if err == nil {
		t.Error("expected error when provider missing for oauth_start")
	}
}

func TestToolOAuthPollMissingFlowID(t *testing.T) {
	tool := newTool(t)
	_, err := tool.Execute(ctxWithUser(1), map[string]any{"action": "oauth_poll", "provider": "github"})
	if err == nil {
		t.Error("expected error when flow_id missing for oauth_poll")
	}
}

func TestToolAddSecretMissingName(t *testing.T) {
	tool := newTool(t)
	_, err := tool.Execute(ctxWithUser(1), map[string]any{"action": "add_secret"})
	if err == nil {
		t.Error("expected error when name missing for add_secret")
	}
}

func TestToolAddSecretInstruction(t *testing.T) {
	tool := newTool(t)
	out, err := tool.Execute(ctxWithUser(1), map[string]any{
		"action":  "add_secret",
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
	if strings.Contains(out, "<actual") || strings.Contains(out, "plaintext") {
		t.Errorf("add_secret output should not mention plaintext values: %q", out)
	}
}

func TestToolDefinition(t *testing.T) {
	tool := newTool(t)
	def := tool.Definition()
	if def.Name != "credentials" {
		t.Errorf("Definition.Name = %q; want credentials", def.Name)
	}
	if def.InputSchema == nil {
		t.Error("Definition.InputSchema is nil")
	}
}

func TestCredentialsDefinition(t *testing.T) {
	def := credentials.CredentialsDefinition()
	if def.Name != "credentials" {
		t.Errorf("CredentialsDefinition.Name = %q; want credentials", def.Name)
	}
}
