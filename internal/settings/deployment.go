package settings

import (
	"context"
	"fmt"
	"net/url"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/controlplane"
	"github.com/CherryHQ/stella/internal/mcp"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// DeploymentMutation is the narrow, admin-only control-plane port exposed to
// Settings. The adapter below re-enters controlplane.Begin for every operation.
// Secrets are deliberately absent from this interface.
type DeploymentMutation interface {
	ListProviders(context.Context, authz.Authority) ([]config.Provider, error)
	GetProvider(context.Context, authz.Authority, string) (config.Provider, error)
	CreateProvider(context.Context, authz.Authority, config.Provider) error
	UpdateProvider(context.Context, authz.Authority, string, config.Provider) (config.Provider, error)
	DeleteProvider(context.Context, authz.Authority, string) error
	GetEmbedding(context.Context, authz.Authority) (config.EmbeddingSettings, error)
	SetEmbedding(context.Context, authz.Authority, controlplane.EmbeddingUpdate) (config.EmbeddingSettings, error)
	GetVision(context.Context, authz.Authority) (config.VisionSettings, error)
	SetVision(context.Context, authz.Authority, config.VisionSettings) (config.VisionSettings, error)
	ListPlugins(context.Context, authz.Authority) ([]pkgplugins.RegisteredPlugin, error)
	TogglePlugin(context.Context, authz.Authority, string, string, bool) (config.Plugin, error)
}

// NewDeploymentMutator binds Settings to the existing control-plane PEP. It is
// intentionally boring: no model-facing caller can receive a controlplane.Access
// and every method is admin-gated by the domain's Begin method.
func NewDeploymentMutator(svc *controlplane.Service) DeploymentMutation {
	return &deploymentMutator{svc: svc}
}

// NewDeploymentMutatorRef supports composition roots where the pool manager
// and control-plane service are created after the tool is registered.
func NewDeploymentMutatorRef(ref **controlplane.Service) DeploymentMutation {
	return &deploymentMutator{ref: ref}
}

type deploymentMutator struct {
	svc *controlplane.Service
	ref **controlplane.Service
}

func (m *deploymentMutator) begin(ctx context.Context, authority authz.Authority) (*controlplane.Access, error) {
	if m == nil {
		return nil, ErrUnavailable
	}
	svc := m.svc
	if m.ref != nil {
		svc = *m.ref
	}
	if svc == nil {
		return nil, ErrUnavailable
	}
	return svc.Begin(ctx, authority)
}

func (m *deploymentMutator) ListProviders(ctx context.Context, a authz.Authority) ([]config.Provider, error) {
	acc, err := m.begin(ctx, a)
	if err != nil {
		return nil, err
	}
	return acc.ListProviders(ctx)
}

func (m *deploymentMutator) GetProvider(ctx context.Context, a authz.Authority, id string) (config.Provider, error) {
	acc, err := m.begin(ctx, a)
	if err != nil {
		return config.Provider{}, err
	}
	return acc.GetProvider(ctx, id)
}

func (m *deploymentMutator) CreateProvider(ctx context.Context, a authz.Authority, p config.Provider) error {
	acc, err := m.begin(ctx, a)
	if err != nil {
		return err
	}
	return acc.CreateProvider(ctx, p)
}

func (m *deploymentMutator) UpdateProvider(ctx context.Context, a authz.Authority, id string, p config.Provider) (config.Provider, error) {
	acc, err := m.begin(ctx, a)
	if err != nil {
		return config.Provider{}, err
	}
	return acc.UpdateProvider(ctx, id, p)
}

func (m *deploymentMutator) DeleteProvider(ctx context.Context, a authz.Authority, id string) error {
	acc, err := m.begin(ctx, a)
	if err != nil {
		return err
	}
	return acc.DeleteProvider(ctx, id)
}

func (m *deploymentMutator) GetEmbedding(ctx context.Context, a authz.Authority) (config.EmbeddingSettings, error) {
	acc, err := m.begin(ctx, a)
	if err != nil {
		return config.EmbeddingSettings{}, err
	}
	return acc.GetEmbeddingSettings(ctx)
}

func (m *deploymentMutator) SetEmbedding(ctx context.Context, a authz.Authority, u controlplane.EmbeddingUpdate) (config.EmbeddingSettings, error) {
	acc, err := m.begin(ctx, a)
	if err != nil {
		return config.EmbeddingSettings{}, err
	}
	return acc.SetEmbeddingSettings(ctx, u)
}

func (m *deploymentMutator) GetVision(ctx context.Context, a authz.Authority) (config.VisionSettings, error) {
	acc, err := m.begin(ctx, a)
	if err != nil {
		return config.VisionSettings{}, err
	}
	return acc.GetVisionSettings(ctx)
}

func (m *deploymentMutator) SetVision(ctx context.Context, a authz.Authority, s config.VisionSettings) (config.VisionSettings, error) {
	acc, err := m.begin(ctx, a)
	if err != nil {
		return config.VisionSettings{}, err
	}
	return acc.SetVisionSettings(ctx, s)
}

func (m *deploymentMutator) ListPlugins(ctx context.Context, a authz.Authority) ([]pkgplugins.RegisteredPlugin, error) {
	acc, err := m.begin(ctx, a)
	if err != nil {
		return nil, err
	}
	return acc.ListPlugins(ctx)
}

func (m *deploymentMutator) TogglePlugin(ctx context.Context, a authz.Authority, kind, name string, enabled bool) (config.Plugin, error) {
	acc, err := m.begin(ctx, a)
	if err != nil {
		return config.Plugin{}, err
	}
	return acc.TogglePlugin(ctx, kind, name, enabled)
}

// MCPMutation is implemented by mcp.Access. Keeping this port at the Settings
// boundary prevents raw Service calls from bypassing owner resolution.
type MCPMutation interface {
	List(context.Context, string, string) ([]mcp.Registration, error)
	Create(context.Context, mcp.CreateInput) (mcp.Registration, error)
	Update(context.Context, mcp.UpdateInput) (mcp.Registration, error)
	Delete(context.Context, string, string, string) error
}

func safeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// validateSettingsURL is narrower than the MCP runtime policy on purpose. The
// model-facing boundary rejects URL-carried secrets without changing HTTP or
// control-plane behavior used by existing clients.
func validateSettingsURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("URL must be an absolute URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("URL must not contain userinfo, query, or fragment")
	}
	return nil
}

func providerView(p config.Provider) map[string]any {
	return map[string]any{
		"id": p.ID, "type": p.Type, "name": p.Name, "enabled": p.Enabled,
		"has_api_key": p.APIKey != "", "base_url": safeURL(p.BaseURL), "model_count": len(p.Models),
	}
}

func embeddingView(s config.EmbeddingSettings) map[string]any {
	return map[string]any{"enabled": s.Enabled, "model": s.Model, "dimension": s.Dim, "base_url": safeURL(s.BaseURL), "normalize": s.Normalize, "has_api_key": s.APIKey != ""}
}

func visionView(s config.VisionSettings) map[string]any { return map[string]any{"model": s.Model} }

func pluginView(p pkgplugins.RegisteredPlugin) map[string]any {
	return map[string]any{"id": p.Info.ID, "kind": p.Kind, "name": p.Name, "enabled": p.State.Enabled, "has_config": p.HasConfig, "has_status": p.HasStatus, "capabilities": p.Capabilities}
}

func mcpView(r mcp.Registration) map[string]any {
	return map[string]any{"id": r.ID, "scope": r.Scope, "agent_id": r.AgentID, "name": r.Name, "url": safeURL(r.URL), "transport": r.Transport, "auth_type": r.AuthType, "has_secret": r.CredentialRef != "", "enabled": r.Enabled}
}

func registrationDigest(r mcp.Registration) string {
	return digestValue(map[string]any{"id": r.ID, "scope": r.Scope, "user_id": r.UserID, "agent_id": r.AgentID, "name": r.Name, "url": safeURL(r.URL), "transport": r.Transport, "auth_type": r.AuthType, "enabled": r.Enabled})
}

func findRegistration(rows []mcp.Registration, id string) (mcp.Registration, bool) {
	for _, row := range rows {
		if row.ID == id {
			return row, true
		}
	}
	return mcp.Registration{}, false
}

func findPlugin(rows []pkgplugins.RegisteredPlugin, kind, name string) (pkgplugins.RegisteredPlugin, bool) {
	for _, row := range rows {
		if row.Kind == kind && row.Name == name || row.Info.Kind == kind && row.Info.Name == name {
			return row, true
		}
	}
	return pkgplugins.RegisteredPlugin{}, false
}
