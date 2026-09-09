package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/core/mcpconfig"
	"github.com/CherryHQ/stella/internal/plugin"
)

// FileService is the MCP-facing facade over the file resource store. The
// store owns bytes and CAS; FileAccess is the authority boundary for every
// operation. It deliberately keeps no owner tuple supplied by a caller.
type FileService struct {
	files *plugin.FileService
	store *plugin.ResourceStore
	mcp   *Service
}

func NewFileService(files *plugin.FileService, store *plugin.ResourceStore, mcp *Service) *FileService {
	return &FileService{files: files, store: store, mcp: mcp}
}

// Capture returns only valid, enabled MCP declarations from the effective
// file-backed resource snapshot. The plugin FileService applies the Agent
// read policy used by this profile endpoint, and resource precedence is
// resolved before projection so callers never see shadowed package copies.
func (s *FileService) Capture(ctx context.Context, authority authz.Authority, agentID string) ([]FileServer, error) {
	if s == nil || s.files == nil || s.store == nil || !authority.Valid() {
		return nil, authz.ErrForbidden
	}
	resources, err := s.files.ReadSnapshot(ctx, authority, agentID)
	if err != nil {
		return nil, err
	}
	result := make([]FileServer, 0, len(resources))
	for _, resource := range resources {
		if resource.Disabled || resource.Forbidden {
			continue
		}
		keys := make([]string, 0, len(resource.MCP))
		for key := range resource.MCP {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		if resource.Key.Kind == plugin.ResourceMCP && len(keys) == 0 {
			keys = append(keys, resource.Key.Name)
		}
		for _, serverKey := range keys {
			reg, regErr := RegistrationFromFileResource(resource, serverKey, authority)
			if regErr != nil {
				continue
			}
			result = append(result, FileServer{ID: mcpFileServerID(resource.Key, serverKey), Registration: reg, Resource: resource, ServerKey: serverKey})
		}
	}
	return result, nil
}

func (s *FileService) Begin(authority authz.Authority) (*FileAccess, error) {
	if s == nil || s.files == nil || s.store == nil || !authority.Valid() {
		return nil, authz.ErrForbidden
	}
	access, err := s.files.Begin(authority)
	if err != nil {
		return nil, err
	}
	return &FileAccess{service: s, files: access, authority: authority}, nil
}

// FileAccess is one request-scoped MCP file capability.
type FileAccess struct {
	service   *FileService
	files     *plugin.FileAccess
	authority authz.Authority
}

type FileServer struct {
	ID           string
	Registration Registration
	Resource     plugin.FileResource
	ServerKey    string
}

// FileCatalogEntry is the model-facing projection of one discovered file MCP
// tool. It contains identity and display metadata only, never endpoints or
// credentials.
type FileCatalogEntry struct {
	Name          string
	Description   string
	InputSchema   map[string]any
	PluginID      string
	ServerKey     string
	LocalToolName string
	Family        string
}

// Catalog discovers the authority-bound file MCP resources for management
// surfaces. Tool deny lists are cleared only on an observation clone so a
// disabled tool remains re-enableable while execution keeps filtering it.
func (s *FileService) Catalog(ctx context.Context, authority authz.Authority, agentID string) ([]FileCatalogEntry, error) {
	if s == nil || s.files == nil || s.mcp == nil {
		return nil, authz.ErrForbidden
	}
	resources, err := s.files.ReadSnapshot(ctx, authority, agentID)
	if err != nil {
		return nil, err
	}
	for i := range resources {
		resources[i].DisabledTools = nil
	}
	session := NewFileSession(s.mcp)
	defer func() { _ = session.Close() }()
	snapshot, err := NewToolProvider(s.mcp).ToolsForFileSession(ctx, session, resources, authority)
	if err != nil {
		return nil, err
	}
	family := make(map[string]string, len(snapshot.Directory))
	for _, entry := range snapshot.Directory {
		family[entry.PluginID+"\x00"+entry.ServerKey] = "mcp:" + entry.ServerKey
	}
	entries := make([]FileCatalogEntry, 0, len(snapshot.Tools))
	for _, tool := range snapshot.Tools {
		identityProvider, ok := tool.(interface {
			PluginToolIdentity() (string, string, string, bool)
		})
		if !ok {
			continue
		}
		pluginID, serverKey, local, ok := identityProvider.PluginToolIdentity()
		if !ok {
			continue
		}
		definition := tool.Definition()
		entries = append(entries, FileCatalogEntry{Name: definition.Name, Description: definition.Description, InputSchema: cloneSchema(definition.InputSchema), PluginID: pluginID, ServerKey: serverKey, LocalToolName: local, Family: family[pluginID+"\x00"+serverKey]})
	}
	return entries, nil
}

type fileServerAddress struct {
	ResourceID string `json:"resource_id"`
	ServerKey  string `json:"server_key"`
}

func mcpFileServerID(key plugin.ResourceKey, serverKey string) string {
	payload, err := json.Marshal(fileServerAddress{ResourceID: key.ID(), ServerKey: serverKey})
	if err != nil {
		return ""
	}
	return "mcp-file:" + base64.RawURLEncoding.EncodeToString(payload)
}

func parseMCPFileServerID(id string) (plugin.ResourceKey, string, error) {
	encoded, ok := strings.CutPrefix(id, "mcp-file:")
	if !ok || encoded == "" {
		return plugin.ResourceKey{}, "", plugin.ErrInvalidResourceID
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return plugin.ResourceKey{}, "", plugin.ErrInvalidResourceID
	}
	var address fileServerAddress
	if err := json.Unmarshal(raw, &address); err != nil || address.ResourceID == "" || address.ServerKey == "" {
		return plugin.ResourceKey{}, "", plugin.ErrInvalidResourceID
	}
	key, err := plugin.ParseResourceID(address.ResourceID)
	if err != nil || mcpFileServerID(key, address.ServerKey) != id {
		return plugin.ResourceKey{}, "", plugin.ErrInvalidResourceID
	}
	return key, address.ServerKey, nil
}

func (a *FileAccess) ensure() error {
	if a == nil || a.service == nil || a.files == nil || a.service.store == nil || !a.authority.Valid() {
		return authz.ErrForbidden
	}
	return nil
}

// List returns standalone declarations and MCP servers embedded in plugin
// packages. Scope and agentID are filters; the FileAccess PEP still validates
// every resulting root before ResourceStore opens it.
func (a *FileAccess) List(ctx context.Context, scope *plugin.Scope, agentID string) ([]FileServer, error) {
	if err := a.ensure(); err != nil {
		return nil, err
	}
	targets := []struct {
		scope plugin.Scope
		agent string
	}{}
	if scope != nil {
		if *scope == plugin.ScopeSystem || *scope == plugin.ScopeUser {
			agentID = ""
		}
		targets = append(targets, struct {
			scope plugin.Scope
			agent string
		}{*scope, agentID})
	} else {
		if a.authority.Kind() == authz.ActorAgent && agentID == "" {
			agentID = string(a.authority.AgentID())
		}
		targets = append(targets,
			struct {
				scope plugin.Scope
				agent string
			}{plugin.ScopeSystem, ""},
			struct {
				scope plugin.Scope
				agent string
			}{plugin.ScopeUser, ""})
		if agentID != "" {
			targets = append(targets,
				struct {
					scope plugin.Scope
					agent string
				}{plugin.ScopeSystemAgent, agentID},
				struct {
					scope plugin.Scope
					agent string
				}{plugin.ScopeUserAgent, agentID})
		}
	}
	var result []FileServer
	for _, target := range targets {
		key, err := a.files.Destination(ctx, target.scope, target.agent, plugin.ResourceMCP, "placeholder")
		if err != nil {
			return nil, err
		}
		items, err := a.service.store.ListScope(ctx, target.scope, key.UserID, key.AgentID, plugin.ResourceMCP)
		if err != nil {
			return nil, err
		}
		// A package root is listed separately by ResourceStore as a plugin
		// resource. Read both kinds so the MCP endpoint reflects all files.
		plugins, err := a.service.store.ListScope(ctx, target.scope, key.UserID, key.AgentID, plugin.ResourcePlugin)
		if err != nil {
			return nil, err
		}
		items = append(items, plugins...)
		for _, resource := range items {
			keys := make([]string, 0, len(resource.MCP))
			for serverKey := range resource.MCP {
				keys = append(keys, serverKey)
			}
			if len(keys) == 0 && resource.Key.Kind == plugin.ResourceMCP {
				keys = append(keys, resource.Key.Name)
			}
			for _, serverKey := range keys {
				result = append(result, a.projectVisible(resource, serverKey))
			}
		}
	}
	return result, nil
}

func (a *FileAccess) projectVisible(resource plugin.FileResource, serverKey string) FileServer {
	reg, err := RegistrationFromFileResource(resource, serverKey, a.authority)
	if err == nil {
		return FileServer{ID: mcpFileServerID(resource.Key, serverKey), Registration: reg, Resource: resource, ServerKey: serverKey}
	}
	// A malformed or disabled file is still an administrative row. Derive
	// identity from the trusted source key and expose diagnostics; execution
	// paths re-derive a valid registration and therefore cannot run it.
	clean := resource
	clean.Disabled, clean.Forbidden = false, false
	if reg, deriveErr := RegistrationFromFileResource(clean, serverKey, a.authority); deriveErr == nil {
		return FileServer{ID: mcpFileServerID(resource.Key, serverKey), Registration: reg, Resource: resource, ServerKey: serverKey}
	}
	return FileServer{ID: mcpFileServerID(resource.Key, serverKey), Registration: Registration{IdentityKind: RegistrationIdentityFile, FileKey: resource.Key, ID: resource.Key.ID(), PluginID: resource.Key.ID(), ServerKey: serverKey, Scope: string(resource.Key.Scope), UserID: resource.Key.UserID, AgentID: resource.Key.AgentID, Name: resource.Key.Name, Enabled: !resource.Disabled && !resource.Forbidden}, Resource: resource, ServerKey: serverKey}
}

func (a *FileAccess) Get(ctx context.Context, id string) (FileServer, error) {
	if err := a.ensure(); err != nil {
		return FileServer{}, err
	}
	key, serverKey, err := parseMCPFileServerID(id)
	if err != nil {
		return FileServer{}, authz.ErrNotFound
	}
	if err := a.files.AuthorizeKey(ctx, key, false); err != nil {
		return FileServer{}, err
	}
	resource, err := a.service.store.Get(ctx, key)
	if err != nil {
		return FileServer{}, err
	}
	if _, ok := resource.MCP[serverKey]; !ok && (key.Kind != plugin.ResourceMCP || key.Name != serverKey) {
		return FileServer{}, authz.ErrNotFound
	}
	return a.projectVisible(resource, serverKey), nil
}

// CanWrite reports whether the bound authority may mutate the declaration's
// owning resource. Package children remain read-only even when their parent
// resource is visible to an administrator.
func (a *FileAccess) CanWrite(ctx context.Context, item FileServer) bool {
	if err := a.ensure(); err != nil {
		return false
	}
	return item.Resource.Key.Kind != plugin.ResourcePlugin && a.files.CanWrite(ctx, item.Resource.Key)
}

func (a *FileAccess) Create(ctx context.Context, scope plugin.Scope, agentID, name string, declaration mcpconfig.Declaration) (FileServer, error) {
	if err := a.ensure(); err != nil {
		return FileServer{}, err
	}
	declaration, err := mcpconfig.Normalize(declaration)
	if err != nil {
		return FileServer{}, err
	}
	key, err := a.files.Destination(ctx, scope, agentID, plugin.ResourceMCP, name)
	if err != nil {
		return FileServer{}, err
	}
	if err := a.files.AuthorizeKey(ctx, key, true); err != nil {
		return FileServer{}, err
	}
	data, err := json.Marshal(declaration)
	if err != nil {
		return FileServer{}, err
	}
	resource, err := a.service.store.WriteMCP(ctx, key, "", data)
	if err != nil {
		return FileServer{}, err
	}
	return a.project(resource, name)
}

func (a *FileAccess) UpdateDeclaration(ctx context.Context, id, expectedDigest string, declaration mcpconfig.Declaration) (FileServer, error) {
	server, err := a.Get(ctx, id)
	if err != nil {
		return FileServer{}, err
	}
	if server.Resource.Key.Kind != plugin.ResourceMCP {
		return FileServer{}, fmt.Errorf("mcp: packaged declaration is read-only")
	}
	declaration, err = mcpconfig.Normalize(declaration)
	if err != nil {
		return FileServer{}, err
	}
	data, err := json.Marshal(declaration)
	if err != nil {
		return FileServer{}, err
	}
	resource, err := a.service.store.WriteMCP(ctx, server.Resource.Key, expectedDigest, data)
	if err != nil {
		return FileServer{}, err
	}
	return a.project(resource, server.ServerKey)
}

func (a *FileAccess) SetEnabled(ctx context.Context, id, expectedDigest string, enabled bool) (FileServer, error) {
	server, err := a.Get(ctx, id)
	if err != nil {
		return FileServer{}, err
	}
	if err := a.files.AuthorizeKey(ctx, server.Resource.Key, true); err != nil {
		return FileServer{}, err
	}
	if _, _, err := a.service.store.SetEnabled(ctx, server.Resource.Key, expectedDigest, enabled); err != nil {
		return FileServer{}, err
	}
	resource, err := a.service.store.Get(ctx, server.Resource.Key)
	if err != nil && !errors.Is(err, plugin.ErrNotFound) {
		return FileServer{}, err
	}
	if errors.Is(err, plugin.ErrNotFound) {
		resource = server.Resource
		resource.Disabled = !enabled
	}
	return a.project(resource, server.ServerKey)
}

func (a *FileAccess) Delete(ctx context.Context, id, expectedDigest string) error {
	server, err := a.Get(ctx, id)
	if err != nil {
		return err
	}
	if server.Resource.Key.Kind != plugin.ResourceMCP {
		return plugin.ErrForbidden
	}
	if err := a.files.AuthorizeKey(ctx, server.Resource.Key, true); err != nil {
		return err
	}
	return a.service.store.Delete(ctx, server.Resource.Key, expectedDigest)
}

func (a *FileAccess) ReadDeclaration(ctx context.Context, id string) ([]byte, string, error) {
	server, err := a.Get(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if server.Resource.Key.Kind != plugin.ResourceMCP {
		return nil, "", plugin.ErrForbidden
	}
	if err := a.files.AuthorizeKey(ctx, server.Resource.Key, false); err != nil {
		return nil, "", err
	}
	return a.service.store.ReadMCP(ctx, server.Resource.Key)
}

func (a *FileAccess) WriteDeclaration(ctx context.Context, id, expectedDigest string, data []byte) (FileServer, error) {
	server, err := a.Get(ctx, id)
	if err != nil {
		return FileServer{}, err
	}
	if server.Resource.Key.Kind != plugin.ResourceMCP {
		return FileServer{}, plugin.ErrForbidden
	}
	if err := a.files.AuthorizeKey(ctx, server.Resource.Key, true); err != nil {
		return FileServer{}, err
	}
	resource, err := a.service.store.WriteMCP(ctx, server.Resource.Key, expectedDigest, data)
	if err != nil {
		return FileServer{}, err
	}
	return a.project(resource, server.ServerKey)
}

func (a *FileAccess) SetCredentials(ctx context.Context, id, expectedDigest, bearerToken, clientSecret string) (FileServer, error) {
	server, err := a.Get(ctx, id)
	if err != nil {
		return FileServer{}, err
	}
	if server.Resource.Digest != expectedDigest {
		return FileServer{}, plugin.ErrConflict
	}
	if a.service.mcp == nil {
		return FileServer{}, errPluginCredentialsUnavailable
	}
	switch {
	case bearerToken != "":
		if err := a.service.mcp.SetFileBearerCredential(ctx, server.Registration, a.authority, bearerToken); err != nil {
			return FileServer{}, err
		}
	case clientSecret != "":
		if err := a.service.mcp.SetFileOAuthClientSecret(ctx, server.Registration, a.authority, clientSecret); err != nil {
			return FileServer{}, err
		}
	default:
		return FileServer{}, errors.New("mcp: one credential is required")
	}
	return server, nil
}

func (a *FileAccess) project(resource plugin.FileResource, serverKey string) (FileServer, error) {
	reg, err := RegistrationFromFileResource(resource, serverKey, a.authority)
	if err != nil {
		return FileServer{}, err
	}
	return FileServer{ID: mcpFileServerID(resource.Key, serverKey), Registration: reg, Resource: resource, ServerKey: serverKey}, nil
}

// SetFileOAuthClientSecret binds a manually supplied client secret to the
// declaration identity. The public declaration stays file-owned and is never
// rewritten to contain the secret.
func (s *Service) SetFileOAuthClientSecret(ctx context.Context, reg Registration, authority authz.Authority, secret string) error {
	if s == nil || !reg.IsFile() || reg.AuthType != AuthTypeOAuth || reg.OAuthClientSecretRef == "" || strings.TrimSpace(secret) == "" {
		return errors.New("mcp: invalid file OAuth client secret")
	}
	if _, err := FileCredentialOwner(reg, authority); err != nil {
		return err
	}
	if reg.CredentialMode == CredentialModeShared && !authority.IsAdmin() {
		return authz.ErrForbidden
	}
	return s.withFileVault(ctx, reg, fileClientOwner(reg), func(vault Vault) error {
		return fileVaultSet(ctx, vault, fileClientOwner(reg), reg.OAuthClientSecretRef, secret)
	})
}
