package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/core/mcpconfig"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/pkg/tools"
)

const managementToolSibling = "settings_mcp_server_list"

var managementToolDescriptions = map[string]string{
	"list":   "List file-backed MCP servers visible to this agent. Credentials are never returned.",
	"get":    "Read one file-backed MCP server. Credentials are never returned.",
	"create": "Create one standalone MCP declaration file. Credentials are configured separately.",
	"update": "Update one declaration or its enablement with the matching content or settings digest.",
	"delete": "Delete one standalone MCP declaration with its current content digest.",
	"probe":  "Probe one enabled file-backed MCP server and list its current remote tools.",
}

// ManagementTool adapts exact generated MCP actions to the authority-bound
// registration service. It never accepts a bearer, credential reference, or a
// caller-supplied user identity.
type ManagementTool struct {
	spec  SettingsMcpActionTool
	files func() *FileService
}

// NewFileManagementTool binds the settings_mcp actions to file-backed MCP
// resources.
func NewFileManagementTool(spec SettingsMcpActionTool, files func() *FileService) *ManagementTool {
	return &ManagementTool{spec: spec, files: files}
}

func (t *ManagementTool) Definition() tools.Definition {
	return t.spec.Definition(managementToolDescriptions[t.spec.Action])
}

func (t *ManagementTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.files == nil {
		return "", fmt.Errorf("MCP management is unavailable — try again later")
	}
	return t.executeFiles(ctx, args)
}

func (t *ManagementTool) executeFiles(ctx context.Context, args map[string]any) (string, error) {
	authority, err := managementAuthority(ctx)
	if err != nil {
		return "", authz.MapToolError(t.spec.Name, managementToolSibling, err)
	}
	service := t.files()
	if service == nil {
		return "", fmt.Errorf("MCP management is unavailable — try again later")
	}
	access, err := service.Begin(authority)
	if err != nil {
		return "", authz.MapToolError(t.spec.Name, managementToolSibling, err)
	}
	var result any
	switch t.spec.Action {
	case "list":
		var in SettingsMcpListInput
		if err := tools.DecodeInputStrict(args, &in, nil); err != nil {
			return "", err
		}
		items, err := access.List(ctx, nil, "")
		if err != nil {
			return "", authz.MapToolError(t.spec.Name, managementToolSibling, err)
		}
		sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
		limit := in.PageSize
		if limit == 0 {
			limit = 50
		}
		if limit < 1 || limit > 50 {
			return "", fmt.Errorf("page_size must be between 1 and 50")
		}
		offset, err := decodeFileToolPageToken(in.PageToken)
		if err != nil || offset > len(items) {
			return "", fmt.Errorf("invalid page_token")
		}
		end := min(offset+limit, len(items))
		truncated := end < len(items)
		page := items[offset:end]
		items = page
		var nextPageToken string
		if truncated {
			nextPageToken = encodeFileToolPageToken(end)
		}
		views := make([]map[string]any, 0, len(items))
		for _, item := range items {
			views = append(views, fileToolView(ctx, access, service.mcp, authority, item))
		}
		result = map[string]any{"servers": views, "next_page_token": nextPageToken, "truncated": truncated}
	case "get", "probe":
		var in SettingsMcpGetInput
		if err := tools.DecodeInputStrict(args, &in, []string{"id"}); err != nil {
			return "", err
		}
		item, err := access.Get(ctx, in.Id)
		if err != nil {
			return "", authz.MapToolError(t.spec.Name, managementToolSibling, err)
		}
		if t.spec.Action == "probe" {
			if item.Resource.Disabled || item.Resource.Forbidden {
				return "", fmt.Errorf("MCP server is disabled or forbidden")
			}
			reg, regErr := RegistrationFromFileResource(item.Resource, item.ServerKey, authority)
			if regErr != nil {
				return "", authz.MapToolError(t.spec.Name, managementToolSibling, regErr)
			}
			if service.mcp == nil {
				return "", fmt.Errorf("MCP management is unavailable — try again later")
			}
			item.Registration, err = service.mcp.ProbeFile(ctx, reg, authority)
			if err != nil {
				return "", authz.MapToolError(t.spec.Name, managementToolSibling, err)
			}
		}
		result = fileToolView(ctx, access, service.mcp, authority, item)
	case "create":
		var in SettingsMcpCreateInput
		if err := tools.DecodeInputStrict(args, &in, []string{"name", "scope", "url"}); err != nil {
			return "", err
		}
		transport := in.Transport
		if transport == "" {
			transport = TransportStreamableHTTP
		}
		scope := plugin.Scope(in.Scope)
		agentID := ""
		if scope == plugin.ScopeUserAgent || scope == plugin.ScopeSystemAgent {
			if authority.Kind() != authz.ActorAgent {
				return "", fmt.Errorf("agent scope requires an agent session")
			}
			agentID = string(authority.AgentID())
		}
		item, err := access.Create(ctx, scope, agentID, in.Name, mcpconfig.Declaration{URL: in.Url, Transport: transport, Authentication: mcpconfig.Authentication{Type: AuthTypeNone, Mode: CredentialModePerUser}})
		if err != nil {
			return "", authz.MapToolError(t.spec.Name, managementToolSibling, err)
		}
		result = fileToolView(ctx, access, service.mcp, authority, item)
	case "update":
		var in SettingsMcpUpdateInput
		if err := tools.DecodeInputStrict(args, &in, []string{"expected_digest", "id"}); err != nil {
			return "", err
		}
		item, err := access.Get(ctx, in.Id)
		if err != nil {
			return "", authz.MapToolError(t.spec.Name, managementToolSibling, err)
		}
		declarationMutation := in.Url != "" || in.Transport != ""
		_, settingsDigestProvided := args["expected_settings_digest"]
		enablementMutation := in.IsEnabled != nil || settingsDigestProvided
		if declarationMutation && enablementMutation {
			return "", fmt.Errorf("declaration and enablement updates cannot be combined")
		}
		if settingsDigestProvided && in.IsEnabled == nil {
			return "", fmt.Errorf("is_enabled is required for enablement updates")
		}
		switch {
		case in.IsEnabled != nil:
			if !settingsDigestProvided {
				return "", fmt.Errorf("expected_settings_digest is required for enablement updates")
			}
			item, err = access.SetEnabled(ctx, in.Id, in.ExpectedSettingsDigest, *in.IsEnabled)
		case in.Url != "" || in.Transport != "":
			declaration, ok := item.Resource.MCP[item.ServerKey]
			if !ok {
				return "", fmt.Errorf("MCP declaration is unavailable")
			}
			if in.Url != "" {
				declaration.URL = in.Url
			}
			if in.Transport != "" {
				declaration.Transport = in.Transport
			}
			item, err = access.UpdateDeclaration(ctx, in.Id, in.ExpectedDigest, declaration)
		default:
			return "", fmt.Errorf("one update field is required")
		}
		if err != nil {
			return "", authz.MapToolError(t.spec.Name, managementToolSibling, err)
		}
		result = fileToolView(ctx, access, service.mcp, authority, item)
	case "delete":
		var in SettingsMcpDeleteInput
		if err := tools.DecodeInputStrict(args, &in, []string{"expected_digest", "id"}); err != nil {
			return "", err
		}
		if err := access.Delete(ctx, in.Id, in.ExpectedDigest); err != nil {
			return "", authz.MapToolError(t.spec.Name, managementToolSibling, err)
		}
		result = map[string]string{"id": in.Id, "status": "deleted"}
	default:
		return "", fmt.Errorf("unsupported MCP action %q", t.spec.Action)
	}
	return tools.MarshalResult(result)
}

// managementAuthority accepts both direct user turns and delegated Agent turns.
// The authority itself remains the source of truth; the runtime user check
// prevents a stale or foreign turn from borrowing the model-facing tool.
func managementAuthority(ctx context.Context) (authz.Authority, error) {
	authority, ok := authz.AuthorityFromContext(ctx)
	if !ok || (authority.Kind() != authz.ActorUser && authority.Kind() != authz.ActorAgent) || string(authority.UserID()) != authz.UserIDFromContext(ctx) {
		return authz.Authority{}, authz.ErrUnauthenticated
	}
	return authority, nil
}

type fileToolPageToken struct {
	Offset int `json:"offset"`
}

func encodeFileToolPageToken(offset int) string {
	payload, _ := json.Marshal(fileToolPageToken{Offset: offset})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeFileToolPageToken(token string) (int, error) {
	if token == "" {
		return 0, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, err
	}
	var page fileToolPageToken
	if err := json.Unmarshal(payload, &page); err != nil || page.Offset < 0 {
		return 0, fmt.Errorf("invalid page token")
	}
	return page.Offset, nil
}

func fileToolView(ctx context.Context, access *FileAccess, service *Service, authority authz.Authority, item FileServer) map[string]any {
	isReadOnly := item.Resource.Key.Kind == plugin.ResourcePlugin
	if access != nil {
		isReadOnly = !access.CanWrite(ctx, item)
	}
	needsAuth := item.Registration.AuthType != AuthTypeNone
	if needsAuth && service != nil {
		ready, err := service.FileCredentialReady(ctx, item.Registration, authority)
		needsAuth = err != nil || !ready
	}
	result := map[string]any{
		"id": item.ID, "resource_id": item.Resource.Key.ID(), "server_key": item.ServerKey,
		"name": item.Registration.Name, "scope": item.Registration.Scope,
		"content_digest": item.Resource.Digest, "settings_digest": item.Resource.SettingsDigest,
		"is_enabled":    !item.Resource.Disabled && !item.Resource.Forbidden,
		"is_read_only":  isReadOnly,
		"is_standalone": item.Resource.Key.Kind == plugin.ResourceMCP,
		"is_overridden": item.Resource.Overridden,
		"needs_auth":    needsAuth,
		"status":        item.Registration.Status,
	}
	if item.Registration.StatusError != "" {
		result["status_error"] = item.Registration.StatusError
	}
	return result
}
