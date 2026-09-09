package mcp

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"

	"github.com/CherryHQ/stella/internal/agentrun"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/plugin"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/tools"
)

// NewFileSession creates the runner-owned lifetime boundary for filesystem
// MCP connections. A session is never shared between runners.
func (p *ToolProvider) NewFileSession() *FileSession {
	if p == nil {
		return nil
	}
	return NewFileSession(p.svc)
}

// ToolsForFileSession projects the selected, trusted filesystem resources into
// one turn's MCP tool snapshot. File resources have no observation table: each
// server is discovered directly and a failed child is represented in the
// directory while independent children continue to load.
func (p *ToolProvider) ToolsForFileSession(ctx context.Context, session *FileSession, resources []plugin.FileResource, authority authz.Authority) (pkgplugins.MCPToolSnapshot, error) {
	if !authority.Valid() {
		return pkgplugins.MCPToolSnapshot{}, authz.ErrForbidden
	}
	if p == nil || p.svc == nil || session == nil {
		return pkgplugins.MCPToolSnapshot{}, nil
	}
	type plan struct {
		reg      Registration
		identity pkgplugins.PluginResourceIdentity
		server   string
		disabled map[string]struct{}
		status   string
		reason   string
		tools    []tools.Tool
		catalog  []CatalogTool
		ready    bool
	}
	plans := make([]plan, 0)
	regs := make([]Registration, 0)
	for _, resource := range resources {
		if resource.Key.Kind != plugin.ResourcePlugin && resource.Key.Kind != plugin.ResourceMCP {
			continue
		}
		identity := pkgplugins.PluginResourceIdentity{PluginID: resource.Key.ID(), Scope: string(resource.Key.Scope)}
		keys := make([]string, 0, len(resource.MCP))
		for key := range resource.MCP {
			keys = append(keys, key)
		}
		// A standalone .json resource that failed declaration parsing has no
		// normalized map entry, but its filename still identifies one server for
		// the directory's failed, non-executable projection.
		if len(keys) == 0 && resource.Key.Kind == plugin.ResourceMCP {
			keys = append(keys, resource.Key.Name)
		}
		slices.Sort(keys)
		if len(keys) == 0 {
			continue
		}
		disabled, policyErr := fileDisabledTools(resource.DisabledTools)
		for _, serverKey := range keys {
			entry := plan{identity: identity, server: serverKey, disabled: disabled[serverKey]}
			switch {
			case resource.Forbidden:
				entry.status, entry.reason = StatusError, "file resource is forbidden"
			case resource.Disabled:
				entry.status, entry.reason = StatusError, "file resource is disabled"
			case fileMCPResourceFatal(resource):
				entry.status, entry.reason = StatusError, "file resource is unavailable"
			case policyErr != nil:
				entry.status, entry.reason = StatusError, "file resource has invalid tool policy"
			default:
				reg, err := RegistrationFromFileResource(resource, serverKey, authority)
				if err != nil {
					entry.status, entry.reason = StatusError, fileMCPStatusReason(err)
				} else {
					entry.reg = reg
					regs = append(regs, reg)
				}
			}
			plans = append(plans, entry)
		}
	}
	if err := session.Prepare(ctx, regs, authority); err != nil {
		return pkgplugins.MCPToolSnapshot{}, err
	}
	discoveryCtx, cancel := context.WithTimeout(ctx, defaultDiscoveryTimeout)
	defer cancel()

	// Discover in declaration order. A session connection is shared by all
	// tools of one child, and a per-server error never aborts its siblings.
	for index := range plans {
		item := &plans[index]
		if item.reg.ID == "" {
			continue
		}
		if err := agentrun.Check(discoveryCtx); err != nil {
			item.status, item.reason = fileMCPStatus(err)
			continue
		}
		conn, err := session.borrow(discoveryCtx, item.reg, authority)
		if err != nil {
			item.status, item.reason = fileMCPStatus(err)
			continue
		}
		client, err := conn.get()
		if err != nil {
			item.status, item.reason = fileMCPStatus(err)
			continue
		}
		if err := agentrun.Check(discoveryCtx); err != nil {
			item.status, item.reason = fileMCPStatus(err)
			_ = conn.close()
			continue
		}
		remote, err := client.ListTools(discoveryCtx)
		if err != nil {
			item.status, item.reason = fileMCPStatus(err)
			continue
		}
		if err := agentrun.Check(discoveryCtx); err != nil {
			item.status, item.reason = fileMCPStatus(err)
			_ = conn.close()
			continue
		}
		catalog := make([]CatalogTool, 0, len(remote))
		for _, remoteTool := range remote {
			catalog = append(catalog, CatalogTool{Name: remoteTool.Name, Description: remoteTool.Description, InputSchema: cloneSchema(toolInputSchema(remoteTool.InputSchema)), Annotations: annotationsSchema(remoteTool.Annotations)})
		}
		if err := validateCatalogTools(item.reg, catalog); err != nil {
			item.status, item.reason = StatusError, "MCP server returned an invalid tool catalog"
			continue
		}
		filtered := catalog[:0]
		for _, catalogTool := range catalog {
			if _, blocked := item.disabled[catalogTool.Name]; blocked {
				continue
			}
			filtered = append(filtered, catalogTool)
		}
		catalog = filtered
		for _, catalogTool := range catalog {
			name := exportedToolName(item.reg, catalogTool.Name)
			if name == "" {
				continue
			}
			item.tools = append(item.tools, &toolProxy{svc: p.svc, reg: item.reg, fileConn: conn, remoteName: catalogTool.Name, def: tools.Definition{Name: name, Description: catalogTool.Description, InputSchema: cloneSchema(catalogTool.InputSchema)}})
		}
		item.catalog, item.ready = catalog, true
		item.status = StatusOK
		item.reason = ""
	}

	result := pkgplugins.MCPToolSnapshot{}
	result.Directory = make([]pkgplugins.MCPDirectoryEntry, 0, len(plans))
	allReady := make(map[string]bool)
	seenResource := make(map[string]bool)
	seenExported := make(map[string]struct{})
	for index := range plans {
		item := &plans[index]
		entry := pkgplugins.MCPDirectoryEntry{PluginResourceIdentity: item.identity, ServerKey: item.server, Ready: item.ready, Status: item.status, StatusError: item.reason}
		for _, tool := range item.tools {
			if tool == nil {
				continue
			}
			definition := tool.Definition()
			if _, exists := seenExported[definition.Name]; exists {
				item.ready = false
				entry.Ready = false
				entry.Status = StatusError
				entry.StatusError = "MCP tool name collision"
				continue
			}
			seenExported[definition.Name] = struct{}{}
			entry.Tools = append(entry.Tools, pkgplugins.MCPToolDescriptor{Name: definition.Name, Description: definition.Description, InputSchema: cloneAnyMap(definition.InputSchema), Annotations: catalogAnnotations(item.reg, item.catalog, definition.Name)})
			result.Tools = append(result.Tools, tool)
		}
		result.Directory = append(result.Directory, entry)
		pluginID := item.identity.PluginID
		if !seenResource[pluginID] {
			seenResource[pluginID] = true
			allReady[pluginID] = true
		}
		if !item.ready {
			allReady[pluginID] = false
		}
	}
	for pluginID, ok := range allReady {
		if ok {
			result.SuccessfulPluginIDs = append(result.SuccessfulPluginIDs, pluginID)
		}
	}
	slices.Sort(result.SuccessfulPluginIDs)
	slices.SortFunc(result.Directory, func(left, right pkgplugins.MCPDirectoryEntry) int {
		if left.PluginID != right.PluginID {
			return strings.Compare(left.PluginID, right.PluginID)
		}
		return strings.Compare(left.ServerKey, right.ServerKey)
	})
	return result, nil
}

func fileDisabledTools(raw []string) (map[string]map[string]struct{}, error) {
	result := make(map[string]map[string]struct{})
	for _, item := range raw {
		if item == "" {
			return nil, errors.New("empty disabled tool policy")
		}
		separator := strings.IndexByte(item, '/')
		if separator <= 0 || separator == len(item)-1 || strings.IndexByte(item[separator+1:], '/') >= 0 {
			return nil, errors.New("malformed disabled tool policy")
		}
		serverKey, err := url.PathUnescape(item[:separator])
		if err != nil || url.PathEscape(serverKey) != item[:separator] {
			return nil, errors.New("malformed disabled tool policy")
		}
		toolName, err := url.PathUnescape(item[separator+1:])
		if err != nil || url.PathEscape(toolName) != item[separator+1:] || serverKey == "" || toolName == "" {
			return nil, errors.New("malformed disabled tool policy")
		}
		if result[serverKey] == nil {
			result[serverKey] = make(map[string]struct{})
		}
		result[serverKey][toolName] = struct{}{}
	}
	return result, nil
}

func fileMCPStatusReason(err error) string {
	if errors.Is(err, authz.ErrForbidden) {
		return "authorization denied"
	}
	if FileMCPGrantRevoked(err) {
		return "authorization revoked"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "MCP server timed out"
	}
	return "MCP server unavailable"
}

func fileMCPStatus(err error) (string, string) {
	if errors.Is(err, authz.ErrForbidden) || FileMCPGrantRevoked(err) {
		return StatusNeedsAuth, fileMCPStatusReason(err)
	}
	return StatusError, fileMCPStatusReason(err)
}
