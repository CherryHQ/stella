package plugin

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/CherryHQ/stella/internal/core/mcpconfig"
	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
)

// ResourceStore is the file-backed resource capability. It receives an
// opener, rather than a physical Home path, so every operation is fenced by
// Home's typed owner locks and containment checks.
type ResourceStore struct {
	opener home.RootOpener
}

func NewResourceStore(opener home.RootOpener) *ResourceStore {
	return &ResourceStore{opener: opener}
}

func (s *ResourceStore) ListScope(ctx context.Context, scope Scope, userID, agentID string, kind ResourceKind) ([]FileResource, error) {
	key := ResourceKey{Scope: scope, UserID: userID, AgentID: agentID, Kind: kind}
	if err := validateStoreKey(key, false); err != nil {
		return nil, err
	}
	root, err := s.open(ctx, key, home.RootReadOnly)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	settings, settingsDigest, err := readSettingsState(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("plugin: read %s resource settings: %w", scope, err)
	}
	if err := validateSettingsScope(scope, settings); err != nil {
		return nil, err
	}

	directory := resourceDirectory(kind)
	entries, err := root.List(ctx, directory, home.ListOptions{Limit: ResourceMaxEntries})
	if errors.Is(err, fs.ErrNotExist) {
		entries = nil
	} else if err != nil {
		return nil, err
	}
	resources := make([]FileResource, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if kind == ResourceMCP {
			var ok bool
			name, ok = strings.CutSuffix(name, ".json")
			if !ok {
				continue
			}
		} else if !entry.IsDir() {
			continue
		}
		if !validResourceName(kind, name) {
			continue
		}
		key.Name = name
		resource := s.readCandidate(ctx, root, key)
		resource.SettingsDigest = settingsDigest
		applyResourceSettings(&resource, settings)
		resources = append(resources, resource)
		seen[resourceName(kind, name)] = struct{}{}
	}
	// A settings entry is itself a deliberate negative candidate. Returning it
	// makes the caller able to display or edit a disabled resource whose bytes
	// have not been authored in this scope yet.
	for _, name := range append(slices.Clone(settings.Disabled), settings.Forbidden...) {
		if kindName, logicalName, ok := strings.Cut(name, ":"); !ok || ResourceKind(kindName) != kind || !validResourceName(kind, logicalName) {
			continue
		} else if _, ok := seen[name]; !ok {
			resource := FileResource{Key: ResourceKey{Scope: scope, UserID: userID, AgentID: agentID, Kind: kind, Name: logicalName}}
			resource.SettingsDigest = settingsDigest
			applyResourceSettings(&resource, settings)
			resources = append(resources, resource)
		}
	}
	slices.SortFunc(resources, func(a, b FileResource) int { return strings.Compare(a.Key.Name, b.Key.Name) })
	return resources, nil
}

func (s *ResourceStore) Get(ctx context.Context, key ResourceKey) (FileResource, error) {
	if err := validateStoreKey(key, true); err != nil {
		return FileResource{}, err
	}
	root, err := s.open(ctx, key, home.RootReadOnly)
	if err != nil {
		return FileResource{}, err
	}
	defer func() { _ = root.Close() }()
	return s.readWithRoot(ctx, root, key)
}

func (s *ResourceStore) readWithRoot(ctx context.Context, root home.RootOperations, key ResourceKey) (FileResource, error) {
	resource := s.readCandidate(ctx, root, key)
	settings, settingsDigest, err := readSettingsState(ctx, root)
	if err != nil {
		return FileResource{}, err
	}
	if err := validateSettingsScope(key.Scope, settings); err != nil {
		return FileResource{}, err
	}
	applyResourceSettings(&resource, settings)
	resource.SettingsDigest = settingsDigest
	if resource.Content == nil && resource.Digest == "" && resource.MCP == nil && len(resource.Diagnostics) == 0 && !resource.Disabled && !resource.Forbidden {
		return FileResource{}, ErrNotFound
	}
	return resource, nil
}

func (s *ResourceStore) ReadFile(ctx context.Context, key ResourceKey, name string) (ResourceFile, string, error) {
	if err := validateStoreKey(key, true); err != nil {
		return ResourceFile{}, "", err
	}
	name, err := cleanResourcePath(name)
	if err != nil {
		return ResourceFile{}, "", err
	}
	root, err := s.open(ctx, key, home.RootReadOnly)
	if err != nil {
		return ResourceFile{}, "", err
	}
	defer func() { _ = root.Close() }()
	base := resourceBase(key)
	if base == "" {
		return ResourceFile{}, "", fmt.Errorf("plugin: %w: file reads require a package resource", ErrInvalidConfig)
	}
	content, err := CaptureResource(ctx, root, base)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ResourceFile{}, "", ErrNotFound
		}
		return ResourceFile{}, "", err
	}
	info, err := fs.Stat(content.FS(), name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ResourceFile{}, "", ErrNotFound
		}
		return ResourceFile{}, "", err
	}
	if !info.Mode().IsRegular() {
		return ResourceFile{}, "", fmt.Errorf("plugin: resource file %q is not regular", name)
	}
	data, err := fs.ReadFile(content.FS(), name)
	if err != nil {
		return ResourceFile{}, "", err
	}
	return ResourceFile{Data: data, Mode: info.Mode().Perm()}, content.Digest, nil
}

func (s *ResourceStore) CreatePlugin(ctx context.Context, key ResourceKey, files map[string]ResourceFile) (FileResource, error) {
	if key.Kind != ResourcePlugin {
		return FileResource{}, fmt.Errorf("plugin: %w: create supports plugins only", ErrInvalidConfig)
	}
	if err := validateStoreKey(key, true); err != nil {
		return FileResource{}, err
	}
	if err := validateFileTree(files); err != nil {
		return FileResource{}, err
	}
	root, err := s.open(ctx, key, home.RootReadWrite)
	if err != nil {
		return FileResource{}, err
	}
	defer func() { _ = root.Close() }()
	stage, err := s.stageTree(ctx, root, files)
	if err != nil {
		return FileResource{}, err
	}
	defer func() { _ = root.Remove(context.Background(), stage, home.RemoveOptions{Recursive: true}) }()
	content, err := CaptureResource(ctx, root, stage)
	if err != nil {
		return FileResource{}, err
	}
	if err := validatePluginContent(content, key.Name); err != nil {
		return FileResource{}, err
	}
	if err := root.Mkdir(ctx, "plugins", 0o755, home.MkdirOptions{Parents: true}); err != nil {
		return FileResource{}, err
	}
	if err := root.Rename(ctx, stage, path.Join("plugins", key.Name), home.RenameOptions{NoReplace: true, SyncParent: true}); err != nil {
		return FileResource{}, err
	}
	if err := root.Close(); err != nil {
		return FileResource{}, err
	}
	return s.Get(ctx, key)
}

func (s *ResourceStore) PatchPlugin(ctx context.Context, key ResourceKey, expectedDigest string, writes map[string]*ResourceFile) (FileResource, error) {
	if key.Kind != ResourcePlugin {
		return FileResource{}, fmt.Errorf("plugin: %w: patch supports plugins only", ErrInvalidConfig)
	}
	if err := validateStoreKey(key, true); err != nil {
		return FileResource{}, err
	}
	root, err := s.open(ctx, key, home.RootReadWrite)
	if err != nil {
		return FileResource{}, err
	}
	defer func() { _ = root.Close() }()
	current, err := s.readWithRoot(ctx, root, key)
	if err != nil {
		return FileResource{}, err
	}
	if current.Content == nil || current.Digest == "" {
		return FileResource{}, fmt.Errorf("plugin: malformed plugin cannot be patched")
	}
	if expectedDigest == "" || expectedDigest != current.Digest {
		return FileResource{}, ErrConflict
	}
	desired, err := resourceTree(current.Content)
	if err != nil {
		return FileResource{}, err
	}
	for name, file := range writes {
		clean, err := cleanResourcePath(name)
		if err != nil {
			return FileResource{}, err
		}
		if file == nil {
			delete(desired, clean)
			continue
		}
		copy := ResourceFile{Data: bytes.Clone(file.Data), Mode: file.Mode}
		desired[clean] = copy
	}
	if err := validateFileTree(desired); err != nil {
		return FileResource{}, err
	}
	stage, err := s.stageTree(ctx, root, desired)
	if err != nil {
		return FileResource{}, err
	}
	defer func() { _ = root.Remove(context.Background(), stage, home.RemoveOptions{Recursive: true}) }()
	prospective, err := CaptureResource(ctx, root, stage)
	if err != nil {
		return FileResource{}, err
	}
	if err := validatePluginContent(prospective, key.Name); err != nil {
		return FileResource{}, err
	}

	base := path.Join("plugins", key.Name)
	oldTree, err := resourceTree(current.Content)
	if err != nil {
		return FileResource{}, err
	}
	for name := range oldTree {
		if _, ok := desired[name]; ok {
			continue
		}
		if err := root.Remove(ctx, path.Join(base, name), home.RemoveOptions{}); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return FileResource{}, err
		}
	}
	names := slices.Collect(maps.Keys(desired))
	slices.Sort(names)
	for _, name := range names {
		file := desired[name]
		parent := path.Dir(name)
		if parent != "." {
			if err := root.Mkdir(ctx, path.Join(base, parent), 0o755, home.MkdirOptions{Parents: true}); err != nil {
				return FileResource{}, err
			}
		}
		mode := file.Mode.Perm()
		if mode == 0 {
			mode = 0o644
		}
		if err := root.Upload(ctx, path.Join(base, name), bytes.NewReader(file.Data), home.WriteOptions{Mode: mode, MaxBytes: int64(len(file.Data)) + 1, Sync: true}); err != nil {
			return FileResource{}, err
		}
	}
	updated, err := s.readWithRoot(ctx, root, key)
	if err != nil {
		return FileResource{}, fmt.Errorf("plugin: verify patched plugin: %w", errors.Join(home.ErrOutcomeUnknown, err))
	}
	if updated.Digest != prospective.Digest {
		return FileResource{}, home.ErrOutcomeUnknown
	}
	return updated, nil
}

func (s *ResourceStore) ReadMCP(ctx context.Context, key ResourceKey) ([]byte, string, error) {
	if key.Kind != ResourceMCP {
		return nil, "", fmt.Errorf("plugin: %w: MCP resource required", ErrInvalidConfig)
	}
	if err := validateStoreKey(key, true); err != nil {
		return nil, "", err
	}
	root, err := s.open(ctx, key, home.RootReadOnly)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = root.Close() }()
	var data bytes.Buffer
	if err := root.Read(ctx, mcpPath(key.Name), &data, home.ReadOptions{MaxBytes: 256 << 10}); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, "", ErrNotFound
		}
		return nil, "", err
	}
	return bytes.Clone(data.Bytes()), digestBytes(data.Bytes()), nil
}

func (s *ResourceStore) WriteMCP(ctx context.Context, key ResourceKey, expectedDigest string, data []byte) (FileResource, error) {
	if key.Kind != ResourceMCP {
		return FileResource{}, fmt.Errorf("plugin: %w: MCP resource required", ErrInvalidConfig)
	}
	if err := validateStoreKey(key, true); err != nil {
		return FileResource{}, err
	}
	if len(data) == 0 || len(data) > 256<<10 {
		return FileResource{}, ErrResourceLimit
	}
	if _, err := mcpconfig.Parse(data); err != nil {
		return FileResource{}, err
	}
	root, err := s.open(ctx, key, home.RootReadWrite)
	if err != nil {
		return FileResource{}, err
	}
	defer func() { _ = root.Close() }()
	var current bytes.Buffer
	readErr := root.Read(ctx, mcpPath(key.Name), &current, home.ReadOptions{MaxBytes: 256 << 10})
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		return FileResource{}, readErr
	}
	currentDigest := ""
	if readErr == nil {
		currentDigest = digestBytes(current.Bytes())
	}
	if currentDigest != expectedDigest {
		return FileResource{}, ErrConflict
	}
	if err := root.Mkdir(ctx, "mcp", 0o755, home.MkdirOptions{Parents: true}); err != nil {
		return FileResource{}, err
	}
	if err := root.Upload(ctx, mcpPath(key.Name), bytes.NewReader(data), home.WriteOptions{Mode: 0o644, MaxBytes: int64(len(data)) + 1, Sync: true}); err != nil {
		return FileResource{}, err
	}
	if err := root.Close(); err != nil {
		return FileResource{}, err
	}
	return s.Get(ctx, key)
}

func (s *ResourceStore) Delete(ctx context.Context, key ResourceKey, expectedDigest string) error {
	if key.Kind != ResourcePlugin && key.Kind != ResourceMCP {
		return fmt.Errorf("plugin: %w: only plugin and MCP sources can be deleted", ErrForbidden)
	}
	if err := validateStoreKey(key, true); err != nil {
		return err
	}
	root, err := s.open(ctx, key, home.RootReadWrite)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	current, err := s.readWithRoot(ctx, root, key)
	if err != nil {
		return err
	}
	if expectedDigest == "" || current.Digest != expectedDigest {
		return ErrConflict
	}
	name := resourceBase(key)
	if key.Kind == ResourceMCP {
		name = mcpPath(key.Name)
	}
	if err := root.Remove(ctx, name, home.RemoveOptions{Recursive: key.Kind == ResourcePlugin}); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

func (s *ResourceStore) ReadSettings(ctx context.Context, scope Scope, userID, agentID string) (ResourceSettings, string, error) {
	key := ResourceKey{Scope: scope, UserID: userID, AgentID: agentID, Kind: ResourcePlugin, Name: "settings"}
	if err := validateStoreKey(key, false); err != nil {
		return ResourceSettings{}, "", err
	}
	root, err := s.open(ctx, key, home.RootReadOnly)
	if err != nil {
		return ResourceSettings{}, "", err
	}
	defer func() { _ = root.Close() }()
	settings, digest, err := readSettingsState(ctx, root)
	if err != nil {
		return ResourceSettings{}, "", err
	}
	if err := validateSettingsScope(scope, settings); err != nil {
		return ResourceSettings{}, "", err
	}
	return settings, digest, nil
}

func (s *ResourceStore) SetDisabled(ctx context.Context, key ResourceKey, expectedSettingsDigest string, disabled bool) (ResourceSettings, string, error) {
	return s.updateSettings(ctx, key, expectedSettingsDigest, func(settings *ResourceSettings) error {
		return setSettingName(&settings.Disabled, key, disabled)
	})
}

// SetEnabled performs the public toggle as one settings CAS. System scopes
// use forbidden as the administrator-wide deny bit; personal scopes use only
// disabled. Enabling clears both bits on administrator scopes so a stale
// lower-level disabled entry cannot keep the resource masked.
func (s *ResourceStore) SetEnabled(ctx context.Context, key ResourceKey, expectedSettingsDigest string, enabled bool) (ResourceSettings, string, error) {
	if err := validateStoreKey(key, true); err != nil {
		return ResourceSettings{}, "", err
	}
	return s.updateSettings(ctx, key, expectedSettingsDigest, func(settings *ResourceSettings) error {
		name := resourceName(key.Kind, key.Name)
		if key.Scope == ScopeSystem || key.Scope == ScopeSystemAgent {
			if enabled {
				settings.Forbidden = slices.DeleteFunc(settings.Forbidden, func(value string) bool { return value == name })
				settings.Disabled = slices.DeleteFunc(settings.Disabled, func(value string) bool { return value == name })
				return nil
			}
			if !slices.Contains(settings.Forbidden, name) {
				settings.Forbidden = append(settings.Forbidden, name)
			}
			return nil
		}
		if enabled {
			settings.Disabled = slices.DeleteFunc(settings.Disabled, func(value string) bool { return value == name })
			return nil
		}
		if !slices.Contains(settings.Disabled, name) {
			settings.Disabled = append(settings.Disabled, name)
		}
		return nil
	})
}

func (s *ResourceStore) SetForbidden(ctx context.Context, key ResourceKey, expectedSettingsDigest string, forbidden bool) (ResourceSettings, string, error) {
	if key.Scope != ScopeSystem && key.Scope != ScopeSystemAgent {
		return ResourceSettings{}, "", ErrForbidden
	}
	return s.updateSettings(ctx, key, expectedSettingsDigest, func(settings *ResourceSettings) error {
		return setSettingName(&settings.Forbidden, key, forbidden)
	})
}

func (s *ResourceStore) SetDisabledTools(ctx context.Context, key ResourceKey, expectedSettingsDigest string, tools []string) (ResourceSettings, string, error) {
	return s.updateSettings(ctx, key, expectedSettingsDigest, func(settings *ResourceSettings) error {
		if len(tools) == 0 {
			delete(settings.DisabledTools, resourceName(key.Kind, key.Name))
			return nil
		}
		if settings.DisabledTools == nil {
			settings.DisabledTools = make(map[string][]string)
		}
		copy := slices.Clone(tools)
		slices.Sort(copy)
		settings.DisabledTools[resourceName(key.Kind, key.Name)] = slices.Compact(copy)
		return nil
	})
}

func (s *ResourceStore) updateSettings(ctx context.Context, key ResourceKey, expectedDigest string, update func(*ResourceSettings) error) (ResourceSettings, string, error) {
	if err := validateStoreKey(key, true); err != nil {
		return ResourceSettings{}, "", err
	}
	root, err := s.open(ctx, key, home.RootReadWrite)
	if err != nil {
		return ResourceSettings{}, "", err
	}
	defer func() { _ = root.Close() }()
	settings, currentDigest, err := readSettingsState(ctx, root)
	if err != nil {
		return ResourceSettings{}, "", err
	}
	if currentDigest != expectedDigest {
		return ResourceSettings{}, "", ErrConflict
	}
	if err := update(&settings); err != nil {
		return ResourceSettings{}, "", err
	}
	canonicalizeSettings(&settings)
	if err := validateSettingsScope(key.Scope, settings); err != nil {
		return ResourceSettings{}, "", err
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return ResourceSettings{}, "", err
	}
	if settingsEmpty(settings) {
		if currentDigest != "" {
			if err := root.Remove(ctx, "settings.json", home.RemoveOptions{}); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return ResourceSettings{}, "", err
			}
		}
		return settings, "", nil
	}
	if err := root.Upload(ctx, "settings.json", bytes.NewReader(encoded), home.WriteOptions{Mode: 0o600, MaxBytes: int64(len(encoded)) + 1, Sync: true}); err != nil {
		return ResourceSettings{}, "", err
	}
	return settings, digestBytes(encoded), nil
}

func (s *ResourceStore) open(ctx context.Context, key ResourceKey, access home.RootAccess) (home.RootOperations, error) {
	if s == nil || s.opener == nil {
		return nil, errors.New("plugin: resource store opener is required")
	}
	scope, err := rootScope(key.Scope)
	if err != nil {
		return nil, err
	}
	return s.opener.OpenRoot(ctx, home.WorkspaceRequest{UserID: key.UserID, AgentID: key.AgentID}, scope, access)
}

func rootScope(scope Scope) (home.RootScope, error) {
	switch scope {
	case ScopeSystem:
		return home.RootSystemResources, nil
	case ScopeSystemAgent:
		return home.RootSystemAgentResources, nil
	case ScopeUser:
		return home.RootUserResources, nil
	case ScopeUserAgent:
		return home.RootUserAgentResources, nil
	default:
		return 0, ErrUnknownScope
	}
}

func validateStoreKey(key ResourceKey, named bool) error {
	if !validScope(key.Scope) || !ownerMatches(key.Scope, key.UserID, key.AgentID) || (key.Kind != ResourcePlugin && key.Kind != ResourceSkill && key.Kind != ResourceMCP) {
		return fmt.Errorf("plugin: %w", ErrInvalidConfig)
	}
	if named && !validResourceName(key.Kind, key.Name) {
		return fmt.Errorf("plugin: %w", ErrInvalidConfig)
	}
	return nil
}

func resourceDirectory(kind ResourceKind) string {
	if kind == ResourceMCP {
		return "mcp"
	}
	return string(kind) + "s"
}

func resourceBase(key ResourceKey) string {
	if key.Kind == ResourceMCP {
		return ""
	}
	return path.Join(resourceDirectory(key.Kind), key.Name)
}

func mcpPath(name string) string { return path.Join("mcp", name+".json") }

func cleanResourcePath(name string) (string, error) {
	if name == "" || strings.Contains(name, `\`) || strings.ContainsRune(name, 0) || path.IsAbs(name) || path.Clean(name) != name || name == "." || name == ".." || strings.HasPrefix(name, "../") {
		return "", fmt.Errorf("plugin: invalid resource path %q", name)
	}
	for part := range strings.SplitSeq(name, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("plugin: invalid resource path %q", name)
		}
	}
	return name, nil
}

func validateFileTree(files map[string]ResourceFile) error {
	if len(files) == 0 || len(files) > ResourceMaxFiles {
		return ErrResourceLimit
	}
	total := 0
	paths := make([]string, 0, len(files))
	for name, file := range files {
		clean, err := cleanResourcePath(name)
		if err != nil || clean != name {
			return fmt.Errorf("plugin: invalid resource file %q", name)
		}
		if len(file.Data) > ResourceMaxFileBytes {
			return ErrResourceLimit
		}
		total += len(file.Data)
		if total > ResourceMaxBytes {
			return ErrResourceLimit
		}
		paths = append(paths, name)
	}
	slices.Sort(paths)
	for i := 1; i < len(paths); i++ {
		if strings.HasPrefix(paths[i], paths[i-1]+"/") {
			return fmt.Errorf("plugin: resource file path conflicts with directory %q", paths[i-1])
		}
	}
	return nil
}

func (s *ResourceStore) stageTree(ctx context.Context, root home.RootOperations, files map[string]ResourceFile) (string, error) {
	var random [10]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	stage := ".resource-stage-" + hex.EncodeToString(random[:])
	if err := root.Mkdir(ctx, stage, 0o700, home.MkdirOptions{}); err != nil {
		return "", err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		file := files[name]
		parent := path.Dir(name)
		if parent != "." {
			if err := root.Mkdir(ctx, path.Join(stage, parent), 0o755, home.MkdirOptions{Parents: true}); err != nil {
				return "", err
			}
		}
		mode := file.Mode.Perm()
		if mode == 0 {
			mode = 0o644
		}
		if err := root.Upload(ctx, path.Join(stage, name), bytes.NewReader(file.Data), home.WriteOptions{Mode: mode, MaxBytes: int64(len(file.Data)) + 1, Sync: true}); err != nil {
			return "", err
		}
	}
	return stage, nil
}

func (s *ResourceStore) readCandidate(ctx context.Context, root home.RootOperations, key ResourceKey) FileResource {
	resource := FileResource{Key: key}
	if key.Kind == ResourceMCP {
		var data bytes.Buffer
		err := root.Read(ctx, mcpPath(key.Name), &data, home.ReadOptions{MaxBytes: 256 << 10})
		if errors.Is(err, fs.ErrNotExist) {
			return resource
		}
		if err != nil {
			resource.Diagnostics = append(resource.Diagnostics, agentpackage.Diagnostic{Severity: agentpackage.SeverityError, Code: "resource.capture", Message: "resource content could not be captured"})
			return resource
		}
		resource.Digest = digestBytes(data.Bytes())
		declaration, err := mcpconfig.Parse(data.Bytes())
		if err != nil {
			resource.Diagnostics = append(resource.Diagnostics, agentpackage.Diagnostic{Severity: agentpackage.SeverityError, Code: "resource.mcp", Message: "invalid MCP declaration"})
			return resource
		}
		resource.MCP = map[string]mcpconfig.Declaration{key.Name: declaration}
		return resource
	}
	content, err := CaptureResource(ctx, root, resourceBase(key))
	if errors.Is(err, fs.ErrNotExist) {
		return resource
	}
	if err != nil {
		resource.Diagnostics = append(resource.Diagnostics, agentpackage.Diagnostic{Severity: agentpackage.SeverityError, Code: "resource.capture", Message: "resource content could not be captured"})
		return resource
	}
	resource.Content, resource.Digest = content, content.Digest
	if key.Kind == ResourcePlugin {
		resource.Package, resource.Diagnostics = agentpackage.LoadFS(content.FS())
		if resource.Package != nil && resource.Package.Manifest.Name != key.Name {
			resource.Package = nil
			resource.Diagnostics = append(resource.Diagnostics, agentpackage.Diagnostic{Severity: agentpackage.SeverityError, Code: "resource.manifest", Message: "manifest name differs from directory"})
		}
		if resource.Package != nil {
			resource.Skills = slices.Clone(resource.Package.Skills)
			resource.MCP = make(map[string]mcpconfig.Declaration)
			for _, server := range resource.Package.MCPServers {
				declaration := mcpconfig.Declaration{URL: server.URL, Transport: strings.ReplaceAll(server.Type, "-", "_"), Headers: server.Headers}
				if extension := resource.Package.Extension; extension != nil {
					declaration.Authentication = extension.MCPAuth[server.Name]
					options := extension.MCPOptions[server.Name]
					declaration.Description = options.Description
					declaration.CallTimeoutSeconds = options.CallTimeoutSeconds
				}
				normalized, normalizeErr := mcpconfig.Normalize(declaration)
				if normalizeErr != nil {
					resource.Diagnostics = append(resource.Diagnostics, agentpackage.Diagnostic{Severity: agentpackage.SeverityError, Code: "resource.mcp", Message: "invalid MCP authentication or transport"})
					continue
				}
				resource.MCP[server.Name] = normalized
			}
		}
	}
	return resource
}

func validatePluginContent(content *ResourceContent, name string) error {
	if content == nil {
		return errors.New("plugin: empty plugin content")
	}
	pkg, diagnostics := agentpackage.LoadFS(content.FS())
	if pkg == nil {
		return fmt.Errorf("plugin: invalid package: %v", diagnostics)
	}
	if pkg.Manifest.Name != name {
		return errors.New("plugin: manifest name differs from directory")
	}
	return nil
}

func resourceTree(content *ResourceContent) (map[string]ResourceFile, error) {
	files := make(map[string]ResourceFile, content.files)
	err := fs.WalkDir(content.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		data, err := fs.ReadFile(content.FS(), name)
		if err != nil {
			return err
		}
		files[name] = ResourceFile{Data: data, Mode: info.Mode().Perm()}
		return nil
	})
	return files, err
}

func readSettingsState(ctx context.Context, root home.RootOperations) (ResourceSettings, string, error) {
	var data bytes.Buffer
	err := root.Read(ctx, "settings.json", &data, home.ReadOptions{MaxBytes: 256 << 10})
	if errors.Is(err, fs.ErrNotExist) {
		return ResourceSettings{}, "", nil
	}
	if err != nil {
		return ResourceSettings{}, "", err
	}
	settings, err := decodeResourceSettings(data.Bytes())
	if err != nil {
		return ResourceSettings{}, "", err
	}
	return settings, digestBytes(data.Bytes()), nil
}

func decodeResourceSettings(data []byte) (ResourceSettings, error) {
	var settings ResourceSettings
	if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		return settings, errors.New("settings must be an object")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return settings, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return settings, errors.New("trailing settings content")
		}
		return settings, err
	}
	keys := append(slices.Clone(settings.Disabled), settings.Forbidden...)
	keys = append(keys, slices.Collect(maps.Keys(settings.DisabledTools))...)
	for _, key := range keys {
		kind, name, ok := strings.Cut(key, ":")
		if !ok || !validResourceName(ResourceKind(kind), name) {
			return settings, errors.New("invalid settings resource key")
		}
	}
	return settings, nil
}

func validateSettingsScope(scope Scope, settings ResourceSettings) error {
	if (scope == ScopeUser || scope == ScopeUserAgent) && len(settings.Forbidden) != 0 {
		return errors.New("plugin: personal settings cannot declare administrator prohibitions")
	}
	return nil
}

func applyResourceSettings(resource *FileResource, settings ResourceSettings) {
	name := resourceName(resource.Key.Kind, resource.Key.Name)
	resource.Disabled = slices.Contains(settings.Disabled, name) || slices.Contains(settings.Forbidden, name)
	resource.Forbidden = slices.Contains(settings.Forbidden, name)
	resource.DisabledTools = slices.Clone(settings.DisabledTools[name])
}

func setSettingName(values *[]string, key ResourceKey, enabled bool) error {
	name := resourceName(key.Kind, key.Name)
	current := slices.Clone(*values)
	if enabled {
		if !slices.Contains(current, name) {
			current = append(current, name)
		}
	} else {
		current = slices.DeleteFunc(current, func(value string) bool { return value == name })
	}
	slices.Sort(current)
	*values = slices.Compact(current)
	return nil
}

func canonicalizeSettings(settings *ResourceSettings) {
	slices.Sort(settings.Disabled)
	settings.Disabled = slices.Compact(settings.Disabled)
	slices.Sort(settings.Forbidden)
	settings.Forbidden = slices.Compact(settings.Forbidden)
	if len(settings.DisabledTools) == 0 {
		settings.DisabledTools = nil
		return
	}
	for key, tools := range settings.DisabledTools {
		slices.Sort(tools)
		settings.DisabledTools[key] = slices.Compact(tools)
	}
}

func settingsEmpty(settings ResourceSettings) bool {
	return len(settings.Disabled) == 0 && len(settings.Forbidden) == 0 && len(settings.DisabledTools) == 0
}

func digestBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:])
}
