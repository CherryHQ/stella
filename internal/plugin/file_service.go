package plugin

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/CherryHQ/stella/internal/authz"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	"github.com/CherryHQ/stella/internal/platform/home"
)

// FileService owns the file-backed plugin capability. ResourceStore stays a
// deliberately authority-free primitive; callers must enter through Begin.
type FileService struct {
	store  *ResourceStore
	agents *agentaccess.Service
}

func NewFileService(store *ResourceStore, agents *agentaccess.Service) *FileService {
	return &FileService{store: store, agents: agents}
}

// Begin binds one trusted authority to the file capability. The authority is
// retained only as identity; every operation re-checks the Agent PEP before it
// opens a Home root.
func (s *FileService) Begin(authority authz.Authority) (*FileAccess, error) {
	if s == nil || s.store == nil || s.store.opener == nil || !authority.Valid() {
		return nil, ErrForbidden
	}
	return &FileAccess{service: s, authority: authority}, nil
}

// Capture resolves the effective file-backed resources for one trusted
// authority. It is the runtime snapshot boundary, so it applies execute
// policy before opening any personal or Agent root and then lets
// DiscoverResources enforce scope precedence and declaration conflicts.
func (s *FileService) Capture(ctx context.Context, authority authz.Authority, agentID string) ([]FileResource, error) {
	return s.capture(ctx, authority, agentID, authz.ActionExecute)
}

// ReadSnapshot resolves effective resources for read-only profile views. It
// keeps the HTTP projection's authorization contract aligned with the Agent
// endpoint, which grants read access without implying execution permission.
func (s *FileService) ReadSnapshot(ctx context.Context, authority authz.Authority, agentID string) ([]FileResource, error) {
	return s.capture(ctx, authority, agentID, authz.ActionRead)
}

func (s *FileService) capture(ctx context.Context, authority authz.Authority, agentID string, action authz.Action) ([]FileResource, error) {
	if s == nil || s.store == nil || s.store.opener == nil || !authority.Valid() {
		return nil, ErrForbidden
	}
	if authority.Kind() == authz.ActorGuest {
		if agentID == "" {
			return []FileResource{}, nil
		}
		return nil, ErrForbidden
	}
	if authority.Kind() == authz.ActorSystem && agentID != "" {
		return nil, ErrForbidden
	}
	if authority.Kind() == authz.ActorAgent && agentID != "" && agentID != string(authority.AgentID()) {
		return nil, ErrForbidden
	}
	if authority.Kind() == authz.ActorGroupAgent && agentID != "" && agentID != string(authority.AgentID()) {
		return nil, ErrForbidden
	}
	if agentID == "" && (authority.Kind() == authz.ActorAgent || authority.Kind() == authz.ActorGroupAgent) {
		agentID = string(authority.AgentID())
	}
	if agentID != "" && (authority.Kind() == authz.ActorUser || authority.Kind() == authz.ActorAgent || authority.Kind() == authz.ActorGroupAgent) {
		if s.agents == nil {
			return nil, ErrForbidden
		}
		if err := s.agents.Authorize(ctx, authority, agentID, action); err != nil {
			return nil, err
		}
	}
	userID := string(authority.UserID())
	roots := make([]ResourceRoot, 0, 4)
	addRoot := func(scope Scope, rootScope home.RootScope, ownerUser, ownerAgent string) {
		roots = append(roots, ResourceRoot{Scope: scope, UserID: ownerUser, AgentID: ownerAgent, Open: func(openCtx context.Context) (home.RootOperations, error) {
			return s.store.opener.OpenRoot(openCtx, home.WorkspaceRequest{UserID: ownerUser, AgentID: ownerAgent}, rootScope, home.RootReadOnly)
		}})
	}
	addRoot(ScopeSystem, home.RootSystemResources, "", "")
	switch authority.Kind() {
	case authz.ActorSystem:
		// System maintenance is deliberately confined to deployment resources.
	case authz.ActorGroupAgent:
		addRoot(ScopeSystemAgent, home.RootSystemAgentResources, "", agentID)
	case authz.ActorUser, authz.ActorAgent:
		addRoot(ScopeUser, home.RootUserResources, userID, "")
		if agentID != "" {
			addRoot(ScopeSystemAgent, home.RootSystemAgentResources, "", agentID)
			addRoot(ScopeUserAgent, home.RootUserAgentResources, userID, agentID)
		}
	}
	return DiscoverResources(ctx, roots)
}

// FileAccess is the shared authority boundary used by HTTP, control-plane
// tools, and MCP. It never accepts an owner tuple from a caller as authority.
type FileAccess struct {
	service   *FileService
	authority authz.Authority
}

func (a *FileAccess) ensure() error {
	if a == nil || a.service == nil || a.service.store == nil || a.service.store.opener == nil || !a.authority.Valid() {
		return ErrForbidden
	}
	return nil
}

// Destination derives a fully-owned resource key. Agent scopes always run the
// Agent PEP before returning a key, even for a later read-only operation.
func (a *FileAccess) Destination(ctx context.Context, scope Scope, agentID string, kind ResourceKind, name string) (ResourceKey, error) {
	if err := a.ensure(); err != nil {
		return ResourceKey{}, err
	}
	if !validScope(scope) || !validResourceName(kind, name) {
		return ResourceKey{}, fmt.Errorf("plugin: %w", ErrInvalidConfig)
	}
	key := ResourceKey{Scope: scope, Kind: kind, Name: name}
	switch scope {
	case ScopeSystem:
		if agentID != "" {
			return ResourceKey{}, ErrForbidden
		}
	case ScopeSystemAgent:
		if agentID == "" || (a.authority.Kind() != authz.ActorUser && a.authority.Kind() != authz.ActorAgent) {
			return ResourceKey{}, ErrForbidden
		}
		if a.authority.Kind() == authz.ActorAgent && string(a.authority.AgentID()) != agentID {
			return ResourceKey{}, ErrForbidden
		}
		key.AgentID = agentID
	case ScopeUser:
		if agentID != "" || (a.authority.Kind() != authz.ActorUser && a.authority.Kind() != authz.ActorAgent) {
			return ResourceKey{}, ErrForbidden
		}
		key.UserID = string(a.authority.UserID())
	case ScopeUserAgent:
		if agentID == "" {
			return ResourceKey{}, ErrForbidden
		}
		key.UserID = string(a.authority.UserID())
		key.AgentID = agentID
	default:
		return ResourceKey{}, ErrUnknownScope
	}
	if err := a.authorizeKey(ctx, key, false); err != nil {
		return ResourceKey{}, err
	}
	return key, nil
}

// AuthorizeKey checks a complete resource identity before opening its root.
// write distinguishes mutation policy from read visibility.
func (a *FileAccess) AuthorizeKey(ctx context.Context, key ResourceKey, write bool) error {
	if err := a.ensure(); err != nil {
		return err
	}
	if err := validateStoreKey(key, true); err != nil {
		return err
	}
	return a.authorizeKey(ctx, key, write)
}

func (a *FileAccess) CanWrite(ctx context.Context, key ResourceKey) bool {
	return a.AuthorizeKey(ctx, key, true) == nil
}

func (a *FileAccess) authorizeKey(ctx context.Context, key ResourceKey, write bool) error {
	if write {
		switch key.Scope {
		case ScopeSystem, ScopeSystemAgent:
			if a.authority.Kind() != authz.ActorUser || !a.authority.IsAdmin() {
				return ErrForbidden
			}
		case ScopeUser, ScopeUserAgent:
			if a.authority.Kind() != authz.ActorUser && a.authority.Kind() != authz.ActorAgent {
				return ErrForbidden
			}
		}
	}

	switch key.Scope {
	case ScopeSystem:
		if key.UserID != "" || key.AgentID != "" || (a.authority.Kind() != authz.ActorUser && a.authority.Kind() != authz.ActorAgent) {
			return ErrForbidden
		}
	case ScopeSystemAgent:
		if key.UserID != "" || key.AgentID == "" || (a.authority.Kind() != authz.ActorUser && a.authority.Kind() != authz.ActorAgent) {
			return ErrForbidden
		}
		if a.authority.Kind() == authz.ActorAgent && string(a.authority.AgentID()) != key.AgentID {
			return ErrForbidden
		}
	case ScopeUser:
		if key.AgentID != "" || key.UserID == "" || string(a.authority.UserID()) != key.UserID || (a.authority.Kind() != authz.ActorUser && a.authority.Kind() != authz.ActorAgent) {
			return ErrForbidden
		}
	case ScopeUserAgent:
		if key.UserID == "" || key.AgentID == "" || string(a.authority.UserID()) != key.UserID {
			return ErrForbidden
		}
		if a.authority.Kind() == authz.ActorAgent && string(a.authority.AgentID()) != key.AgentID {
			return ErrForbidden
		}
		if a.authority.Kind() != authz.ActorUser && a.authority.Kind() != authz.ActorAgent {
			return ErrForbidden
		}
	}

	if key.Scope == ScopeSystemAgent || key.Scope == ScopeUserAgent {
		if a.service.agents == nil {
			return ErrForbidden
		}
		if err := a.service.agents.Authorize(ctx, a.authority, key.AgentID, authz.ActionRead); err != nil {
			return err
		}
	}
	return nil
}

// List returns raw declarations in the scopes visible to the authority. An
// omitted Agent ID lists system and personal layers; Agent actors default to
// their own agent layer so they cannot accidentally list another agent.
func (a *FileAccess) List(ctx context.Context, kind ResourceKind, scope *Scope, agentID string) ([]FileResource, error) {
	if err := a.ensure(); err != nil {
		return nil, err
	}
	if kind != ResourcePlugin {
		return nil, fmt.Errorf("plugin: %w: plugin resources required", ErrInvalidConfig)
	}
	if a.authority.Kind() == authz.ActorAgent {
		if scope != nil && (*scope == ScopeSystem || *scope == ScopeUser) {
			// The agent identity selects only agent-owned layers. A user/system
			// filter must not accidentally carry it into an ownerless scope.
			agentID = ""
		}
		if agentID != "" && agentID != string(a.authority.AgentID()) {
			return nil, ErrForbidden
		}
		if agentID == "" {
			agentID = string(a.authority.AgentID())
		}
	}
	type target struct {
		scope Scope
		agent string
	}
	var targets []target
	if scope != nil {
		targets = append(targets, target{scope: *scope, agent: agentID})
	} else {
		targets = append(targets, target{scope: ScopeSystem}, target{scope: ScopeUser})
		if agentID != "" {
			targets = append(targets, target{scope: ScopeSystemAgent, agent: agentID}, target{scope: ScopeUserAgent, agent: agentID})
		}
	}
	var result []FileResource
	for _, target := range targets {
		key, err := a.Destination(ctx, target.scope, target.agent, kind, "placeholder")
		if err != nil {
			// Destination validates names, so use a scope-only check here and
			// authorize the actual roots below.
			if target.scope == ScopeSystem && !a.canReadSystem() {
				return nil, err
			}
		} else {
			_ = key
		}
		ownerUser := ""
		if target.scope == ScopeUser || target.scope == ScopeUserAgent {
			ownerUser = string(a.authority.UserID())
		}
		if err := a.authorizeScope(ctx, target.scope, ownerUser, target.agent); err != nil {
			return nil, err
		}
		items, err := a.service.store.ListScope(ctx, target.scope, ownerUser, target.agent, kind)
		if err != nil {
			return nil, err
		}
		result = append(result, items...)
	}
	for i := range result {
		for j := range result {
			if i == j || result[i].Key.Kind != result[j].Key.Kind || result[i].Key.Name != result[j].Key.Name {
				continue
			}
			if scopeRank(result[j].Key.Scope) < scopeRank(result[i].Key.Scope) {
				result[i].Overridden = true
				break
			}
		}
	}
	return result, nil
}

func scopeRank(scope Scope) int {
	for rank, candidate := range scopePrecedence {
		if scope == candidate {
			return rank
		}
	}
	return len(scopePrecedence)
}

func (a *FileAccess) canReadSystem() bool {
	return a.authority.Kind() == authz.ActorUser || a.authority.Kind() == authz.ActorAgent
}

func (a *FileAccess) authorizeScope(ctx context.Context, scope Scope, userID, agentID string) error {
	key := ResourceKey{Scope: scope, UserID: userID, AgentID: agentID, Kind: ResourcePlugin, Name: "scope"}
	return a.authorizeKey(ctx, key, false)
}

func (a *FileAccess) Get(ctx context.Context, id string) (FileResource, error) {
	key, err := ParseResourceID(id)
	if err != nil {
		return FileResource{}, err
	}
	if err := a.AuthorizeKey(ctx, key, false); err != nil {
		return FileResource{}, err
	}
	return a.service.store.Get(ctx, key)
}

func (a *FileAccess) CreatePlugin(ctx context.Context, scope Scope, agentID, name string, files map[string]ResourceFile) (FileResource, error) {
	key, err := a.Destination(ctx, scope, agentID, ResourcePlugin, name)
	if err != nil {
		return FileResource{}, err
	}
	if err := a.AuthorizeKey(ctx, key, true); err != nil {
		return FileResource{}, err
	}
	return a.service.store.CreatePlugin(ctx, key, files)
}

func (a *FileAccess) ImportPlugin(ctx context.Context, sourcePath string, scope Scope, agentID, name string) (FileResource, error) {
	if err := a.ensure(); err != nil {
		return FileResource{}, err
	}
	if a.authority.Kind() != authz.ActorUser || !a.authority.IsAdmin() {
		return FileResource{}, ErrForbidden
	}
	files, sourceName, err := importResourceFiles(sourcePath)
	if err != nil {
		return FileResource{}, err
	}
	if name == "" {
		name = sourceName
	}
	if err := ValidateName(name); err != nil {
		return FileResource{}, err
	}
	if name != sourceName {
		if err := rewriteManifestName(files, name); err != nil {
			return FileResource{}, err
		}
	}
	return a.CreatePlugin(ctx, scope, agentID, name, files)
}

func (a *FileAccess) CopyPlugin(ctx context.Context, sourceID string, scope Scope, agentID, name string) (FileResource, error) {
	source, err := a.Get(ctx, sourceID)
	if err != nil {
		return FileResource{}, err
	}
	if source.Key.Kind != ResourcePlugin || source.Content == nil {
		return FileResource{}, ErrNotFound
	}
	files, err := resourceTree(source.Content)
	if err != nil {
		return FileResource{}, err
	}
	if name == "" {
		name = source.Key.Name
	}
	if err := ValidateName(name); err != nil {
		return FileResource{}, err
	}
	if name != source.Key.Name {
		if err := rewriteManifestName(files, name); err != nil {
			return FileResource{}, err
		}
	}
	return a.CreatePlugin(ctx, scope, agentID, name, files)
}

func (a *FileAccess) SetEnabled(ctx context.Context, id, expectedSettingsDigest string, enabled bool) (FileResource, error) {
	key, err := ParseResourceID(id)
	if err != nil {
		return FileResource{}, err
	}
	if err := a.AuthorizeKey(ctx, key, true); err != nil {
		return FileResource{}, err
	}
	_, digest, err := a.service.store.SetEnabled(ctx, key, expectedSettingsDigest, enabled)
	if err != nil {
		return FileResource{}, err
	}
	resource, getErr := a.service.store.Get(ctx, key)
	if getErr == nil {
		return resource, nil
	}
	if errors.Is(getErr, ErrNotFound) {
		return FileResource{Key: key, SettingsDigest: digest, Disabled: !enabled}, nil
	}
	return FileResource{}, getErr
}

// SetDisabledTools updates the per-resource tool deny list in the owning
// settings file. The settings digest is the CAS version, so callers cannot
// silently overwrite an administrator or another user's concurrent policy.
func (a *FileAccess) SetDisabledTools(ctx context.Context, id, expectedSettingsDigest string, tools []string) (FileResource, error) {
	key, err := ParseResourceID(id)
	if err != nil {
		return FileResource{}, err
	}
	if err := a.AuthorizeKey(ctx, key, true); err != nil {
		return FileResource{}, err
	}
	_, digest, err := a.service.store.SetDisabledTools(ctx, key, expectedSettingsDigest, tools)
	if err != nil {
		return FileResource{}, err
	}
	resource, getErr := a.service.store.Get(ctx, key)
	if getErr == nil {
		return resource, nil
	}
	if errors.Is(getErr, ErrNotFound) {
		return FileResource{Key: key, SettingsDigest: digest, DisabledTools: slices.Clone(tools)}, nil
	}
	return FileResource{}, getErr
}

// ReadSettings returns the CAS state for one scope without requiring that the
// resource itself exists in that scope. This is used by policy adapters for a
// resource inherited from a broader layer.
func (a *FileAccess) ReadSettings(ctx context.Context, scope Scope, agentID string) (ResourceSettings, string, error) {
	if err := a.ensure(); err != nil {
		return ResourceSettings{}, "", err
	}
	ownerUser := ""
	if scope == ScopeUser || scope == ScopeUserAgent {
		ownerUser = string(a.authority.UserID())
	}
	if err := a.authorizeScope(ctx, scope, ownerUser, agentID); err != nil {
		return ResourceSettings{}, "", err
	}
	return a.service.store.ReadSettings(ctx, scope, ownerUser, agentID)
}

func (a *FileAccess) Delete(ctx context.Context, id, expectedDigest string) error {
	key, err := ParseResourceID(id)
	if err != nil {
		return err
	}
	if key.Kind != ResourcePlugin {
		return ErrForbidden
	}
	if err := a.AuthorizeKey(ctx, key, true); err != nil {
		return err
	}
	return a.service.store.Delete(ctx, key, expectedDigest)
}

func (a *FileAccess) ReadFile(ctx context.Context, id, filename string) (ResourceFile, string, error) {
	key, err := ParseResourceID(id)
	if err != nil {
		return ResourceFile{}, "", err
	}
	if err := a.AuthorizeKey(ctx, key, false); err != nil {
		return ResourceFile{}, "", err
	}
	return a.service.store.ReadFile(ctx, key, filename)
}

func (a *FileAccess) UpdateFile(ctx context.Context, id, filename string, file ResourceFile, expectedDigest string) (FileResource, error) {
	key, err := ParseResourceID(id)
	if err != nil {
		return FileResource{}, err
	}
	if err := a.AuthorizeKey(ctx, key, true); err != nil {
		return FileResource{}, err
	}
	return a.service.store.PatchPlugin(ctx, key, expectedDigest, map[string]*ResourceFile{filename: &file})
}

func (a *FileAccess) DeleteFile(ctx context.Context, id, filename, expectedDigest string) (FileResource, error) {
	key, err := ParseResourceID(id)
	if err != nil {
		return FileResource{}, err
	}
	if err := a.AuthorizeKey(ctx, key, true); err != nil {
		return FileResource{}, err
	}
	return a.service.store.PatchPlugin(ctx, key, expectedDigest, map[string]*ResourceFile{filename: nil})
}

// ReadMCP and WriteMCP are the narrow shared hooks for the MCP adapter. The
// adapter still supplies a complete key, but cannot open a root without this
// authority check.
func (a *FileAccess) ReadMCP(ctx context.Context, key ResourceKey) ([]byte, string, error) {
	if err := a.AuthorizeKey(ctx, key, false); err != nil {
		return nil, "", err
	}
	return a.service.store.ReadMCP(ctx, key)
}

func (a *FileAccess) WriteMCP(ctx context.Context, key ResourceKey, expectedDigest string, data []byte) (FileResource, error) {
	if err := a.AuthorizeKey(ctx, key, true); err != nil {
		return FileResource{}, err
	}
	return a.service.store.WriteMCP(ctx, key, expectedDigest, data)
}
