package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"slices"
	"strings"

	"github.com/CherryHQ/stella/internal/core/mcpconfig"
	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
)

type ResourceKind string

const (
	ResourcePlugin ResourceKind = "plugin"
	ResourceSkill  ResourceKind = "skill"
	ResourceMCP    ResourceKind = "mcp"
)

// ResourceKey carries trusted ownership; authored files never supply it.
type ResourceKey struct {
	Scope   Scope        `json:"scope"`
	UserID  string       `json:"user_id,omitempty"`
	AgentID string       `json:"agent_id,omitempty"`
	Kind    ResourceKind `json:"kind"`
	Name    string       `json:"name"`
}

// resourceIDPrefix separates file-backed identities from legacy database
// plugin IDs. The payload is a canonical JSON tuple encoded without padding,
// so owner fields and names cannot collide through delimiter escaping.
const resourceIDPrefix = "file:"

var ErrInvalidResourceID = errors.New("plugin: invalid file resource ID")

// ID returns the stable, scoped identity of this resource. Resource IDs are
// opaque outside this package; ParseResourceID is the only decoder.
func (k ResourceKey) ID() string {
	if !validResourceKey(k) {
		return ""
	}
	data, err := json.Marshal(k)
	if err != nil {
		return ""
	}
	return resourceIDPrefix + base64.RawURLEncoding.EncodeToString(data)
}

// ParseResourceID decodes a file-backed resource identity and validates its
// complete ownership tuple before returning it to a caller.
func ParseResourceID(id string) (ResourceKey, error) {
	encoded, ok := strings.CutPrefix(id, resourceIDPrefix)
	if !ok || encoded == "" {
		return ResourceKey{}, ErrInvalidResourceID
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return ResourceKey{}, fmt.Errorf("%w: encoding: %w", ErrInvalidResourceID, err)
	}
	var key ResourceKey
	if err := json.Unmarshal(data, &key); err != nil {
		return ResourceKey{}, fmt.Errorf("%w: JSON: %w", ErrInvalidResourceID, err)
	}
	if key.ID() != id {
		return ResourceKey{}, ErrInvalidResourceID
	}
	return key, nil
}

func validResourceKey(key ResourceKey) bool {
	return validScope(key.Scope) && ownerMatches(key.Scope, key.UserID, key.AgentID) &&
		(key.Kind == ResourcePlugin || key.Kind == ResourceSkill || key.Kind == ResourceMCP) &&
		validResourceName(key.Kind, key.Name)
}

// ResourceRoot is minted after access checks, not decoded from client input.
// Roots open one at a time because Home capabilities hold owner deletion locks.
type ResourceRoot struct {
	Scope           Scope
	UserID, AgentID string
	Open            func(context.Context) (home.RootOperations, error)
}

type ResourceSettings struct {
	Disabled      []string            `json:"disabled,omitempty"`
	Forbidden     []string            `json:"forbidden,omitempty"`
	DisabledTools map[string][]string `json:"disabled_tools,omitempty"`
}

type FileResource struct {
	Key            ResourceKey
	Content        *ResourceContent
	Digest         string
	SettingsDigest string
	Package        *agentpackage.Package
	Skills         []agentpackage.Skill
	MCP            map[string]mcpconfig.Declaration
	Disabled       bool
	Forbidden      bool
	Overridden     bool
	DisabledTools  []string
	Diagnostics    agentpackage.Diagnostics
}

func resourceName(kind ResourceKind, name string) string { return string(kind) + ":" + name }

// DiscoverResources selects complete candidates before interpreting them. A
// broken or disabled winner must never reveal a broader candidate underneath.
func DiscoverResources(ctx context.Context, roots []ResourceRoot) ([]FileResource, error) {
	byScope := make(map[Scope]ResourceRoot)
	for _, root := range roots {
		if !validScope(root.Scope) || !ownerMatches(root.Scope, root.UserID, root.AgentID) || root.Open == nil {
			return nil, errors.New("plugin: invalid trusted resource root")
		}
		if _, exists := byScope[root.Scope]; exists {
			return nil, errors.New("plugin: duplicate resource scope")
		}
		byScope[root.Scope] = root
	}
	selected := map[string]FileResource{}
	totalBytes, totalFiles := 0, 0
	forbidden := map[string]bool{}
	toolLimits := map[string][]string{}
	for _, scope := range scopePrecedence {
		root, exists := byScope[scope]
		if !exists {
			continue
		}
		err := func() error {
			opened, err := root.Open(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = opened.Close() }()
			settings, err := readResourceSettings(ctx, opened)
			if err != nil {
				return fmt.Errorf("plugin: invalid %s resource settings", scope)
			}
			admin := scope == ScopeSystem || scope == ScopeSystemAgent
			if !admin && len(settings.Forbidden) != 0 {
				return errors.New("plugin: personal settings cannot declare administrator prohibitions")
			}
			for _, name := range settings.Forbidden {
				forbidden[name] = true
			}
			// Tool deny lists are compositional at every scope. A personal
			// settings file must be able to disable one inherited tool without
			// copying the entire package into the user-agent root.
			for name, tools := range settings.DisabledTools {
				toolLimits[name] = append(toolLimits[name], tools...)
			}
			candidates := map[string]FileResource{}
			for _, kind := range []ResourceKind{ResourcePlugin, ResourceSkill, ResourceMCP} {
				dir := string(kind) + "s"
				if kind == ResourceMCP {
					dir = "mcp"
				}
				entries, err := opened.List(ctx, dir, home.ListOptions{Limit: ResourceMaxEntries})
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				if err != nil {
					return fmt.Errorf("plugin: list %s resources: %w", scope, err)
				}
				for _, entry := range entries {
					name := entry.Name()
					if kind == ResourceMCP {
						var ok bool
						name, ok = strings.CutSuffix(name, ".json")
						if !ok {
							continue
						}
					}
					if !validResourceName(kind, name) {
						continue
					}
					key := resourceName(kind, name)
					if _, exists := selected[key]; exists {
						continue
					}
					resource := FileResource{Key: ResourceKey{scope, root.UserID, root.AgentID, kind, name}}
					var captureErr error
					capturedBytes := 0
					if kind == ResourceMCP {
						var data bytes.Buffer
						captureErr = opened.Read(ctx, dir+"/"+entry.Name(), &data, home.ReadOptions{MaxBytes: 256 << 10})
						capturedBytes = data.Len()
						digest := sha256.Sum256(data.Bytes())
						resource.Digest = "sha256:" + hex.EncodeToString(digest[:])
						if captureErr == nil {
							var declaration mcpconfig.Declaration
							declaration, captureErr = mcpconfig.Parse(data.Bytes())
							if captureErr == nil {
								resource.MCP = map[string]mcpconfig.Declaration{name: declaration}
							}
						}
					} else {
						resource.Content, captureErr = CaptureResource(ctx, opened, dir+"/"+entry.Name())
						if captureErr == nil && kind == ResourcePlugin {
							resource.Package, resource.Diagnostics = agentpackage.LoadFS(resource.Content.FS())
							if resource.Package != nil && resource.Package.Manifest.Name != name {
								resource.Package = nil
								captureErr = errors.New("manifest name differs from directory")
							}
						}
					}
					if resource.Content != nil {
						resource.Digest = resource.Content.Digest
						totalBytes += resource.Content.bytes
						totalFiles += resource.Content.files
					} else if kind == ResourceMCP {
						totalBytes += capturedBytes
						totalFiles++
					}
					if totalBytes > ResourceMaxBytes || totalFiles > ResourceMaxFiles {
						return ErrResourceLimit
					}
					if resource.Package != nil {
						resource.Skills = slices.Clone(resource.Package.Skills)
						resource.MCP = make(map[string]mcpconfig.Declaration)
						for _, server := range resource.Package.MCPServers {
							transport := strings.ReplaceAll(server.Type, "-", "_")
							declaration := mcpconfig.Declaration{URL: server.URL, Transport: transport, Headers: server.Headers}
							if extension := resource.Package.Extension; extension != nil {
								declaration.Authentication = extension.MCPAuth[server.Name]
								options := extension.MCPOptions[server.Name]
								declaration.Description = options.Description
								declaration.CallTimeoutSeconds = options.CallTimeoutSeconds
							}
							normalized, err := mcpconfig.Normalize(declaration)
							if err != nil {
								resource.Diagnostics = append(resource.Diagnostics, agentpackage.Diagnostic{Severity: agentpackage.SeverityError, Code: "resource.mcp", Message: "invalid MCP authentication or transport"})
								continue
							}
							resource.MCP[server.Name] = normalized
						}
					}
					if captureErr != nil {
						resource.Diagnostics = append(resource.Diagnostics, agentpackage.Diagnostic{Severity: agentpackage.SeverityError, Code: "resource.capture", Message: "resource content could not be captured"})
					}
					candidates[key] = resource
				}
			}
			for _, name := range append(slices.Clone(settings.Disabled), settings.Forbidden...) {
				kind, logicalName, _ := strings.Cut(name, ":")
				resource, exists := candidates[name]
				if !exists {
					resource.Key = ResourceKey{scope, root.UserID, root.AgentID, ResourceKind(kind), logicalName}
				}
				resource.Disabled = true
				candidates[name] = resource
			}
			for name, resource := range candidates {
				if _, exists := selected[name]; exists {
					continue
				}
				resource.DisabledTools = slices.Clone(settings.DisabledTools[name])
				selected[name] = resource
			}
			return nil
		}()
		if err != nil {
			return nil, err
		}
	}
	result := make([]FileResource, 0, len(selected))
	for _, name := range slices.Sorted(maps.Keys(selected)) {
		resource := selected[name]
		resource.Disabled = resource.Disabled || forbidden[name]
		resource.Forbidden = forbidden[name]
		resource.DisabledTools = append(resource.DisabledTools, toolLimits[name]...)
		slices.Sort(resource.DisabledTools)
		resource.DisabledTools = slices.Compact(resource.DisabledTools)
		result = append(result, resource)
	}
	resolveFileResourceConflicts(result)
	return result, nil
}

func readResourceSettings(ctx context.Context, root home.RootOperations) (ResourceSettings, error) {
	var data bytes.Buffer
	var settings ResourceSettings
	err := root.Read(ctx, "settings.json", &data, home.ReadOptions{MaxBytes: 256 << 10})
	if errors.Is(err, fs.ErrNotExist) {
		return settings, nil
	}
	if err != nil {
		return settings, err
	}
	if !bytes.HasPrefix(bytes.TrimSpace(data.Bytes()), []byte("{")) {
		return settings, errors.New("settings must be an object")
	}
	decoder := json.NewDecoder(&data)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return settings, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return settings, errors.New("trailing settings content")
	}
	keys := append(slices.Clone(settings.Disabled), settings.Forbidden...)
	keys = append(keys, slices.Collect(maps.Keys(settings.DisabledTools))...)
	for _, key := range keys {
		kind, name, ok := strings.Cut(key, ":")
		if !ok || (kind != "plugin" && kind != "skill" && kind != "mcp") || !validResourceName(ResourceKind(kind), name) {
			return settings, errors.New("invalid settings resource key")
		}
	}
	return settings, nil
}

func validResourceName(kind ResourceKind, name string) bool {
	if kind == ResourceSkill {
		return agentpackage.ValidSkillName(name)
	}
	return agentpackage.ValidName(name)
}
