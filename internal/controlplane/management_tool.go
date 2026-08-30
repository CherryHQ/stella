package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/CherryHQ/stella/internal/agent/settingspolicy"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/embedding"
	"github.com/CherryHQ/stella/pkg/tools"
)

const deploymentToolSibling = "provider_list"

var deploymentToolDescriptions = map[string]map[string]string{
	"provider": {
		"list":   "List up to 50 configured providers without exposing API keys.",
		"get":    "Read one provider's safe configuration and version. Use its version for provider_update or provider_delete.",
		"create": "Create a provider without an API key. Add credentials only in the Web UI.",
		"update": "Update safe provider metadata using the version from provider_get. Endpoint changes with an existing key require the Web UI.",
		"delete": "Delete a provider using the version from provider_get. This refuses a stale version.",
	},
	"default_model": {
		"get":    "Read deployment default model roles and their version.",
		"update": "Set deployment default model roles using the version from default_model_get.",
	},
	"embedding_setting": {
		"get":    "Read deployment embedding settings and their version.",
		"update": "Update deployment embedding settings using the version from embedding_setting_get.",
	},
	"plugin": {
		"list":    "List up to 50 registered plugins and whether each is enabled.",
		"enable":  "Enable one registered plugin by kind and name.",
		"disable": "Disable one registered plugin by kind and name.",
	},
}

// ManagementTool is one Stella-only deployment action. It obtains both direct
// human authority and the control-plane admin capability at execution time so a
// cached catalog cannot turn a role change into a write capability.
type ManagementTool struct {
	providerSpec         *ProviderActionTool
	defaultModelSpec     *DefaultModelActionTool
	embeddingSettingSpec *EmbeddingSettingActionTool
	pluginSpec           *PluginActionTool
	service              func() *Service
}

func NewProviderManagementTool(spec ProviderActionTool, service func() *Service) *ManagementTool {
	return &ManagementTool{providerSpec: &spec, service: service}
}

func NewDefaultModelManagementTool(spec DefaultModelActionTool, service func() *Service) *ManagementTool {
	return &ManagementTool{defaultModelSpec: &spec, service: service}
}

func NewEmbeddingSettingManagementTool(spec EmbeddingSettingActionTool, service func() *Service) *ManagementTool {
	return &ManagementTool{embeddingSettingSpec: &spec, service: service}
}

func NewPluginManagementTool(spec PluginActionTool, service func() *Service) *ManagementTool {
	return &ManagementTool{pluginSpec: &spec, service: service}
}

func (t *ManagementTool) Definition() tools.Definition {
	family, action, spec := t.spec()
	return spec.Definition(deploymentToolDescriptions[family][action])
}

func (t *ManagementTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.service == nil || t.service() == nil {
		return "", fmt.Errorf("deployment management is unavailable — try again later")
	}
	authority, err := settingspolicy.DirectAuthority(ctx, authz.UserIDFromContext(ctx))
	if err != nil {
		return "", authz.MapToolError(t.name(), deploymentToolSibling, err)
	}
	access, err := t.service().Begin(ctx, authority)
	if err != nil {
		return "", authz.MapToolError(t.name(), deploymentToolSibling, err)
	}
	var out any
	switch {
	case t.providerSpec != nil:
		out, err = ProviderDispatch(ctx, providerManagementHandler{access: access}, t.providerSpec.Action, args)
	case t.defaultModelSpec != nil:
		out, err = DefaultModelDispatch(ctx, defaultModelManagementHandler{access: access}, t.defaultModelSpec.Action, args)
	case t.embeddingSettingSpec != nil:
		out, err = EmbeddingSettingDispatch(ctx, embeddingManagementHandler{access: access}, t.embeddingSettingSpec.Action, args)
	case t.pluginSpec != nil:
		out, err = PluginDispatch(ctx, pluginManagementHandler{access: access}, t.pluginSpec.Action, args)
	default:
		err = fmt.Errorf("deployment management tool has no action")
	}
	if err != nil {
		return "", authz.MapToolError(t.name(), deploymentToolSibling, err)
	}
	return tools.MarshalResult(out)
}

func (t *ManagementTool) spec() (string, string, interface{ Definition(string) tools.Definition }) {
	if t.providerSpec != nil {
		return "provider", t.providerSpec.Action, t.providerSpec
	}
	if t.defaultModelSpec != nil {
		return "default_model", t.defaultModelSpec.Action, t.defaultModelSpec
	}
	if t.embeddingSettingSpec != nil {
		return "embedding_setting", t.embeddingSettingSpec.Action, t.embeddingSettingSpec
	}
	return "plugin", t.pluginSpec.Action, t.pluginSpec
}

func (t *ManagementTool) name() string {
	if t.providerSpec != nil {
		return t.providerSpec.Name
	}
	if t.defaultModelSpec != nil {
		return t.defaultModelSpec.Name
	}
	if t.embeddingSettingSpec != nil {
		return t.embeddingSettingSpec.Name
	}
	return t.pluginSpec.Name
}

type providerToolView struct {
	ID                   string                          `json:"id"`
	Type                 string                          `json:"type"`
	Name                 string                          `json:"name"`
	Enabled              bool                            `json:"enabled"`
	BaseURL              string                          `json:"base_url"`
	EndpointRedacted     bool                            `json:"endpoint_redacted,omitempty"`
	Models               map[string]config.ProviderModel `json:"models,omitempty"`
	CredentialConfigured bool                            `json:"credential_configured"`
	Version              string                          `json:"version"`
}

func projectProvider(p config.Provider, version string) providerToolView {
	if len(p.Models) == 0 {
		p.Models = nil
	}
	baseURL, redacted := safeToolEndpoint(p.BaseURL)
	return providerToolView{ID: p.ID, Type: p.Type, Name: p.Name, Enabled: p.Enabled, BaseURL: baseURL, EndpointRedacted: redacted, Models: p.Models, CredentialConfigured: p.APIKey != "", Version: version}
}

// safeToolEndpoint prevents a legacy DB row containing query text, a fragment,
// or userinfo from leaking through a model-facing projection.
func safeToolEndpoint(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", true
	}
	return u.String(), false
}

func deploymentVersion(v ...any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func requireVersion(got, want string) error {
	if got != want {
		return &ConflictError{Msg: "resource changed; re-read it before retrying"}
	}
	return nil
}

func providerFromInput(id string, in ProviderCreateInput) (config.Provider, error) {
	var models map[string]config.ProviderModel
	if in.Models != nil {
		encoded, err := json.Marshal(in.Models)
		if err != nil {
			return config.Provider{}, invalid("invalid provider models")
		}
		if err := json.Unmarshal(encoded, &models); err != nil {
			return config.Provider{}, invalid("invalid provider models")
		}
	}
	p := config.Provider{ID: id, Type: strings.TrimSpace(in.Type), Name: strings.TrimSpace(in.Name), Enabled: in.Enabled, BaseURL: strings.TrimSpace(in.BaseUrl), Models: models}
	if p.Type == "" {
		p.Type = p.ID
	}
	if p.Name == "" {
		p.Name = p.ID
	}
	if p.ID == "" {
		return config.Provider{}, invalid("id is required")
	}
	if err := validateProviderEndpoint(p.BaseURL); err != nil {
		return config.Provider{}, invalid(err.Error())
	}
	return p, nil
}

func validateProviderEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("base_url must be an absolute HTTP URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("base_url must be an HTTP URL without userinfo, query, or fragment")
	}
	return nil
}

func endpointOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("missing host")
	}
	return strings.ToLower(u.Scheme) + "://" + net.JoinHostPort(strings.ToLower(host), u.Port()), nil
}

type providerManagementHandler struct{ access *Access }

func (h providerManagementHandler) List(ctx context.Context, _ ProviderListInput) (any, error) {
	rows, err := h.access.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	truncated := len(rows) > 50
	if truncated {
		rows = rows[:50]
	}
	out := make([]providerToolView, 0, len(rows))
	for _, p := range rows {
		version, err := h.access.ProviderVersion(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, projectProvider(p, version))
	}
	return map[string]any{"providers": out, "truncated": truncated}, nil
}

func (h providerManagementHandler) Get(ctx context.Context, in ProviderGetInput) (any, error) {
	p, err := h.access.GetProvider(ctx, in.Id)
	if err != nil {
		return nil, err
	}
	version, err := h.access.ProviderVersion(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	return projectProvider(p, version), nil
}

func (h providerManagementHandler) Create(ctx context.Context, in ProviderCreateInput) (any, error) {
	p, err := providerFromInput(in.Id, in)
	if err != nil {
		return nil, err
	}
	p.APIKey = ""
	if err := h.access.CreateProvider(ctx, p); err != nil {
		return nil, err
	}
	version, err := h.access.ProviderVersion(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	return projectProvider(p, version), nil
}

func (h providerManagementHandler) Update(ctx context.Context, in ProviderUpdateInput) (any, error) {
	current, err := h.access.GetProvider(ctx, in.Id)
	if err != nil {
		return nil, err
	}
	version, err := h.access.ProviderVersion(ctx, in.Id)
	if err != nil {
		return nil, err
	}
	if err := requireVersion(version, in.ExpectedVersion); err != nil {
		return nil, err
	}
	create := ProviderCreateInput{Id: in.Id, Type: in.Type, Name: in.Name, Enabled: in.Enabled, BaseUrl: in.BaseUrl, Models: in.Models}
	candidate, err := providerFromInput(in.Id, create)
	if err != nil {
		return nil, err
	}
	oldOrigin, oldErr := endpointOrigin(current.BaseURL)
	newOrigin, newErr := endpointOrigin(candidate.BaseURL)
	if oldErr != nil || newErr != nil {
		return nil, invalid("base_url must be an absolute HTTP URL")
	}
	if current.APIKey != "" && oldOrigin != newOrigin {
		return nil, &ConflictError{Msg: "provider endpoint with credentials must be changed in the Web UI"}
	}
	candidate.APIKey = current.APIKey
	if in.Models == nil {
		candidate.Models = current.Models
	}
	saved, err := h.access.UpdateProviderIfVersion(ctx, candidate, in.ExpectedVersion)
	if err != nil {
		return nil, err
	}
	updatedVersion, err := h.access.ProviderVersion(ctx, saved.ID)
	if err != nil {
		return nil, err
	}
	return projectProvider(saved, updatedVersion), nil
}

func (h providerManagementHandler) Delete(ctx context.Context, in ProviderDeleteInput) (any, error) {
	version, err := h.access.ProviderVersion(ctx, in.Id)
	if err != nil {
		return nil, err
	}
	if err := requireVersion(version, in.ExpectedVersion); err != nil {
		return nil, err
	}
	if err := h.access.DeleteProviderIfVersion(ctx, in.Id, in.ExpectedVersion); err != nil {
		return nil, err
	}
	return map[string]string{"id": in.Id, "status": "deleted"}, nil
}

type defaultModelToolView struct {
	config.DefaultModels
	Version string `json:"version"`
}

func projectDefaultModels(v config.DefaultModels) defaultModelToolView {
	return defaultModelToolView{DefaultModels: v, Version: deploymentVersion(v)}
}

type defaultModelManagementHandler struct{ access *Access }

func (h defaultModelManagementHandler) Get(ctx context.Context, _ DefaultModelGetInput) (any, error) {
	v, e := h.access.GetDefaultModels(ctx)
	return projectDefaultModels(v), e
}

func (h defaultModelManagementHandler) Update(ctx context.Context, in DefaultModelUpdateInput) (any, error) {
	next, e := h.access.SetDefaultModelsIfVersion(ctx, config.DefaultModels{Model: in.Model, ModelThinking: in.ModelThinking, ModelStrong: in.ModelStrong, ModelStrongThinking: in.ModelStrongThinking, ModelFast: in.ModelFast, ModelFastThinking: in.ModelFastThinking, ModelVision: in.ModelVision, ModelEmbedding: in.ModelEmbedding}, in.ExpectedVersion)
	if e != nil {
		return nil, e
	}
	return projectDefaultModels(next), nil
}

type embeddingToolView struct {
	Enabled   bool   `json:"enabled"`
	Dim       int    `json:"dim"`
	Normalize bool   `json:"normalize"`
	Active    bool   `json:"active"`
	Version   string `json:"version"`
}

func projectEmbedding(v EmbeddingState) embeddingToolView {
	return embeddingToolView{Enabled: v.Settings.Enabled, Dim: v.Settings.Dim, Normalize: v.Settings.Normalize, Active: v.Active, Version: deploymentVersion(v.Settings)}
}

type embeddingManagementHandler struct{ access *Access }

func (h embeddingManagementHandler) Get(ctx context.Context, _ EmbeddingSettingGetInput) (any, error) {
	v, e := h.access.GetEmbeddingSettings(ctx)
	return projectEmbedding(v), e
}

func (h embeddingManagementHandler) Update(ctx context.Context, in EmbeddingSettingUpdateInput) (any, error) {
	next, e := h.access.SetEmbeddingSettingsIfVersion(ctx, EmbeddingUpdate{Enabled: in.Enabled, Dim: in.Dim, Normalize: in.Normalize}, in.ExpectedVersion)
	if e != nil {
		return nil, e
	}
	return projectEmbedding(next), nil
}

type pluginToolView struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Version string `json:"version"`
}

func projectPlugin(kind, name string, enabled bool) pluginToolView {
	return pluginToolView{Kind: kind, Name: name, Enabled: enabled, Version: deploymentVersion(kind, name, enabled)}
}

type pluginManagementHandler struct{ access *Access }

func (h pluginManagementHandler) List(ctx context.Context, _ PluginListInput) (any, error) {
	rows, e := h.access.ListPlugins(ctx)
	if e != nil {
		return nil, e
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Kind+"/"+rows[i].Name < rows[j].Kind+"/"+rows[j].Name })
	truncated := len(rows) > 50
	if truncated {
		rows = rows[:50]
	}
	out := make([]pluginToolView, 0, len(rows))
	for _, p := range rows {
		out = append(out, projectPlugin(p.Kind, p.Name, p.State.Enabled))
	}
	return map[string]any{"plugins": out, "truncated": truncated}, nil
}

func (h pluginManagementHandler) Enable(ctx context.Context, in PluginEnableInput) (any, error) {
	p, e := h.access.TogglePlugin(ctx, in.Kind, in.Name, true)
	if e != nil {
		return nil, e
	}
	return projectPlugin(p.Kind, p.Name, p.Enabled), nil
}

func (h pluginManagementHandler) Disable(ctx context.Context, in PluginDisableInput) (any, error) {
	p, e := h.access.TogglePlugin(ctx, in.Kind, in.Name, false)
	if e != nil {
		return nil, e
	}
	return projectPlugin(p.Kind, p.Name, p.Enabled), nil
}

// validateEmbeddingDim is shared by HTTP and tool callers. Keeping it here
// stops the two transports from accepting deployment states the worker cannot use.
func validateEmbeddingDim(dim int) error {
	if dim < 0 || dim > embedding.StorageDim {
		return invalid(fmt.Sprintf("dim must be between 0 and %d", embedding.StorageDim))
	}
	return nil
}
