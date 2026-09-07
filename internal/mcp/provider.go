package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/platform/diagnostic"
	"github.com/CherryHQ/stella/internal/plugin"
	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/tools"
)

// defaultDiscoveryConcurrency caps cold tools/list discovery. A bad
// system-wide MCP fleet should degrade by skipping servers, not by serially
// stalling runner creation for N*timeout.
const defaultDiscoveryConcurrency = 4

// defaultDiscoveryTimeout bounds the whole cold-discovery pass at session
// start; a persisted catalog needs no connection and is never subject to it.
const defaultDiscoveryTimeout = 20 * time.Second

// catalogMaxAge bounds how stale a persisted tool catalog may be before the
// next session start re-probes the server in the background of building tools.
const catalogMaxAge = 24 * time.Hour

// ToolProvider surfaces the tools of every MCP server visible to a (user,
// agent) context into the agent tool registry, proxying tools/call back to the
// server. It builds proxies from the persisted tool catalog (populated by
// Probe) so session startup does not have to connect; a stale or empty catalog
// triggers a cold discovery whose result is written back through the service.
// A down or misbehaving server is logged and skipped so it can never break an
// agent session.
type ToolProvider struct {
	svc         *Service
	log         *slog.Logger
	concurrency int
}

// NewToolProvider builds a provider over the registration service.
func NewToolProvider(svc *Service) *ToolProvider {
	return &ToolProvider{
		svc:         svc,
		log:         slog.With("component", "mcp"),
		concurrency: defaultDiscoveryConcurrency,
	}
}

// ToolsForSnapshot builds MCP tools from an already resolved plugin snapshot.
// The snapshot is the single source of truth for definition/config entries;
// this method never re-queries plugin configuration or falls back to another
// package. It reads observations internally for this snapshot's
// trusted authority, so callers never supply an arbitrary owner or cache.
func (p *ToolProvider) ToolsForSnapshot(ctx context.Context, snapshot plugin.Snapshot) ([]tools.Tool, error) {
	result, err := p.ToolsForSnapshotWithDirectory(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	return result.Tools, nil
}

// ToolsForSnapshotWithDirectory projects the MCP directory and its successful
// tool set in one observation read. The runner stores both parts in its
// immutable PluginContext, so cache identity and model-facing tools cannot be
// assembled from different probe generations.
func (p *ToolProvider) ToolsForSnapshotWithDirectory(ctx context.Context, snapshot plugin.Snapshot) (pkgplugins.MCPToolSnapshot, error) {
	authority := snapshot.Authority()
	if !authority.Valid() {
		return pkgplugins.MCPToolSnapshot{}, authz.ErrForbidden
	}
	if p == nil || p.svc == nil {
		return pkgplugins.MCPToolSnapshot{}, nil
	}
	registrations, err := p.svc.RegistrationsForSnapshot(ctx, snapshot)
	if err != nil {
		return pkgplugins.MCPToolSnapshot{}, err
	}
	tools, outcomes := p.toolsForRegistrationsDetailed(ctx, registrations, true, string(authority.UserID()))
	return pkgplugins.MCPToolSnapshot{
		Tools:               tools,
		Directory:           mcpDirectory(registrations, outcomes),
		SuccessfulPluginIDs: successfulPluginIDs(registrations, outcomes),
	}, nil
}

// observationsForSnapshot reads only config IDs visible to this snapshot and
// selects the exact shared or trusted per-user owner. It deliberately leaves
// legacy per-user cache rows dormant because their provenance is unknown.
func (s *Service) observationsForSnapshot(ctx context.Context, snapshot plugin.Snapshot, authority authz.Authority) (map[string]PluginMCPObservation, error) {
	if s == nil || s.pool == nil {
		return nil, errPluginCredentialsUnavailable
	}
	ids := make([]string, 0)
	modes := make(map[string]string)
	seen := make(map[string]struct{})
	for _, def := range snapshot.Definitions() {
		resolved, ok := snapshot.Get(def.ID)
		if !ok || !resolved.Effective.IsEffectivelyEnabled || resolved.Effective.ConfigID == "" || !payloadHasMCP(resolved.Effective.Payload) || resolved.Config == nil {
			continue
		}
		cfg := *resolved.Config
		for _, child := range cfg.MCPServers {
			payload, err := decodeMCPPluginPayloadForKey(resolved.Effective.Payload, child.ServerKey)
			if err != nil {
				slog.Warn("invalid MCP child skipped during observation lookup", "child_id", child.ID)
				continue
			}
			if _, exists := seen[child.ID]; !exists {
				seen[child.ID] = struct{}{}
				ids = append(ids, child.ID)
			}
			modes[child.ID] = payload.CredentialMode
		}
	}
	var userID *string
	if authority.Kind() == authz.ActorUser || authority.Kind() == authz.ActorAgent {
		value := string(authority.UserID())
		if value != "" {
			userID = &value
		}
	}
	states, err := appdb.ListMCPConnectionStatesForConfigs(ctx, s.pool, ids, userID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]PluginMCPObservation, len(states))
	for _, state := range states {
		mode := modes[state.ChildID]
		if mode == CredentialModePerUser {
			if userID == nil || state.CredentialUserID == nil || *state.CredentialUserID != *userID {
				continue
			}
		} else if state.CredentialUserID != nil {
			continue
		}
		var tools []CatalogTool
		if len(state.Tools) != 0 {
			if err := json.Unmarshal(state.Tools, &tools); err != nil {
				slog.Warn("invalid MCP observation skipped", "child_id", state.ChildID)
				continue
			}
		}
		observation := PluginMCPObservation{
			Status: state.Status, StatusError: state.StatusError,
			ConfigRevision: state.ConfigRevision, Tools: tools,
		}
		if state.ProbedAt != nil {
			observation.ProbedAt = state.ProbedAt.UTC()
		}
		if state.CredentialUserID != nil {
			observation.CredentialUserID = *state.CredentialUserID
		}
		out[state.ChildID] = observation
	}
	return out, nil
}

func mcpRegistrationsFromSnapshot(snapshot plugin.Snapshot, observations map[string]PluginMCPObservation, authority authz.Authority) ([]Registration, error) {
	defs := snapshot.Definitions()
	slices.SortFunc(defs, func(a, b plugin.Definition) int { return strings.Compare(a.ID, b.ID) })

	registrations := make([]Registration, 0, len(defs))
	exportedNames := make(map[string]string)
	for _, def := range defs {
		effective, err := snapshot.Resolve(def.ID)
		if errors.Is(err, plugin.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("resolve MCP package %q: %w", def.ID, err)
		}
		resolved, ok := snapshot.Get(def.ID)
		if !ok {
			return nil, fmt.Errorf("resolve MCP package %q returned missing definition", def.ID)
		}
		if resolved.Definition.ID != def.ID || resolved.Effective.PluginID != def.ID || resolved.Effective.ConfigID != effective.ConfigID {
			return nil, fmt.Errorf("resolve MCP package %q returned inconsistent entry", def.ID)
		}
		if !effective.IsEffectivelyEnabled || !payloadHasMCP(effective.Payload) || resolved.Config == nil {
			continue
		}
		observation := observations[resolved.Config.ID]
		converted, err := registrationsFromResolvedConfig(resolved.Definition, *resolved.Config, resolved.Effective, observation, observations, authority)
		if err != nil {
			return nil, fmt.Errorf("convert MCP config %q: %w", resolved.Config.ID, err)
		}
		for _, registration := range converted {
			for _, catalogTool := range registration.Tools {
				name, err := agentpackage.ExportedToolName(registration.PluginID, registration.ServerKey, catalogTool.Name)
				if err != nil {
					return nil, fmt.Errorf("convert MCP config %q tool %q: %w", resolved.Config.ID, catalogTool.Name, err)
				}
				if prior, duplicate := exportedNames[name]; duplicate {
					return nil, fmt.Errorf("MCP configs %q and %q collide on exported tool name %q", prior, resolved.Config.ID, name)
				}
				exportedNames[name] = resolved.Config.ID
			}
		}
		registrations = append(registrations, converted...)
	}
	return registrations, nil
}

// registrationsFromResolvedConfig expands one package config into one runtime
// registration per authored MCP child. Nested resources need persisted child
// identities and only consume observations keyed by those identities.
func registrationsFromResolvedConfig(def plugin.Definition, cfg plugin.Config, effective plugin.Effective, parentObservation PluginMCPObservation, observations map[string]PluginMCPObservation, authority authz.Authority) ([]Registration, error) {
	if len(cfg.MCPServers) == 0 {
		if payloadHasMCP(effective.Payload) {
			return nil, nil
		}
		registration, err := RegistrationFromPluginConfig(def, cfg, effective, parentObservation, authority)
		if err != nil {
			return nil, err
		}
		return []Registration{registration}, nil
	}
	registrations := make([]Registration, 0, len(cfg.MCPServers))
	for _, child := range cfg.MCPServers {
		observation := PluginMCPObservation{ConfigRevision: cfg.Revision}
		if childObservation, ok := observations[child.ID]; ok {
			observation = childObservation
		}
		registration, err := RegistrationFromPluginChild(def, cfg, effective, child, observation, authority)
		if err != nil {
			// A malformed resource cannot hide independent siblings. The
			// adapter validates identity and credentials before returning it.
			slog.Warn("invalid MCP child skipped", "child_id", child.ID)
			continue
		}
		registrations = append(registrations, registration)
	}
	return registrations, nil
}

func (p *ToolProvider) toolsForRegistrations(ctx context.Context, regs []Registration, allowDiscovery bool, userID string) []tools.Tool {
	result, _ := p.toolsForRegistrationsDetailed(ctx, regs, allowDiscovery, userID)
	return result
}

type registrationToolsResult struct {
	tools       []tools.Tool
	catalog     []CatalogTool
	ready       bool
	status      string
	statusError string
}

func (p *ToolProvider) toolsForRegistrationsDetailed(ctx context.Context, regs []Registration, allowDiscovery bool, userID string) ([]tools.Tool, []registrationToolsResult) {
	type result struct {
		index       int
		tools       []tools.Tool
		catalog     []CatalogTool
		ready       bool
		status      string
		statusError string
	}
	limit := p.concurrency
	if limit <= 0 {
		limit = defaultDiscoveryConcurrency
	}
	discoveryCtx, cancel := context.WithTimeout(ctx, defaultDiscoveryTimeout)
	defer cancel()
	sem := make(chan struct{}, limit)
	results := make(chan result, len(regs))
	var wg sync.WaitGroup
	for i, reg := range regs {
		if !reg.Enabled {
			continue
		}
		owner := p.svc.CredentialOwner(reg, userID)
		if reg.Status == StatusNeedsAuth || !p.svc.HasUserCredential(ctx, reg, userID) {
			// Skip without connecting: needs_auth means the last credential was
			// rejected; a per_user registration without this user's bundle has
			// nothing to authenticate with. Only a reconnect from the Web UI
			// fixes either.
			results <- result{index: i, status: reg.Status, statusError: reg.StatusError}
			continue
		}
		if catalog, ok := freshCatalog(reg); ok {
			if err := validateCatalogTools(reg, catalog); err != nil {
				p.log.Warn("mcp cached catalog is invalid; skipping server", "server", reg.Name, "error", err)
				results <- result{index: i, status: reg.Status, statusError: err.Error()}
				continue
			}
			results <- result{index: i, tools: p.catalogProxies(reg, catalog, owner), catalog: catalog, ready: true, status: reg.Status, statusError: reg.StatusError}
			continue
		}
		if !allowDiscovery {
			continue
		}
		index, registration, credentialOwner := i, reg, owner
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-discoveryCtx.Done():
				return
			}
			tools, catalog, ready, status, statusErr := p.discover(discoveryCtx, registration, credentialOwner)
			results <- result{index: index, tools: tools, catalog: catalog, ready: ready, status: status, statusError: statusErr}
		})
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect by registration index so name collisions resolve by scope
	// precedence (regs is ordered most-specific-first), not by arrival order.
	outcomes := make([]registrationToolsResult, len(regs))
	for res := range results {
		outcomes[res.index] = registrationToolsResult{tools: res.tools, catalog: res.catalog, ready: res.ready, status: res.status, statusError: res.statusError}
	}
	seen := map[string]struct{}{}
	var out []tools.Tool
	for i := range outcomes {
		for _, tool := range outcomes[i].tools {
			name := tool.Definition().Name
			if _, ok := seen[name]; ok {
				p.log.Warn("mcp tool name collision; skipping duplicate", "tool", name)
				continue
			}
			seen[name] = struct{}{}
			out = append(out, tool)
		}
	}
	return out, outcomes
}

func mcpDirectory(regs []Registration, outcomes []registrationToolsResult) []pkgplugins.MCPDirectoryEntry {
	entries := make([]pkgplugins.MCPDirectoryEntry, 0, len(regs))
	for i, reg := range regs {
		if !reg.Enabled || i >= len(outcomes) {
			continue
		}
		entry := pkgplugins.MCPDirectoryEntry{
			PluginResourceIdentity: pkgplugins.PluginResourceIdentity{
				PluginID: reg.PluginID,
				ConfigID: reg.ParentConfigID,
				Scope:    reg.Scope,
				Revision: reg.ConfigRevision,
			},
			ServerKey: reg.ServerKey,
			Ready:     outcomes[i].ready, Status: outcomes[i].status, StatusError: outcomes[i].statusError,
			Tools: make([]pkgplugins.MCPToolDescriptor, 0, len(outcomes[i].tools)),
		}
		for _, tool := range outcomes[i].tools {
			if tool != nil {
				definition := tool.Definition()
				entry.Tools = append(entry.Tools, pkgplugins.MCPToolDescriptor{Name: definition.Name, Description: definition.Description, InputSchema: cloneAnyMap(definition.InputSchema), Annotations: catalogAnnotations(reg, outcomes[i].catalog, definition.Name)})
			}
		}
		slices.SortFunc(entry.Tools, func(left, right pkgplugins.MCPToolDescriptor) int { return strings.Compare(left.Name, right.Name) })
		entries = append(entries, entry)
	}
	slices.SortFunc(entries, func(left, right pkgplugins.MCPDirectoryEntry) int {
		if left.PluginID != right.PluginID {
			return strings.Compare(left.PluginID, right.PluginID)
		}
		if left.ConfigID != right.ConfigID {
			return strings.Compare(left.ConfigID, right.ConfigID)
		}
		return strings.Compare(left.ServerKey, right.ServerKey)
	})
	return entries
}

func catalogAnnotations(reg Registration, catalogTools []CatalogTool, exportedName string) map[string]any {
	if len(catalogTools) == 0 {
		catalogTools = reg.Tools
	}
	for _, catalog := range catalogTools {
		if exportedToolName(reg, catalog.Name) == exportedName {
			return cloneSchema(catalog.Annotations)
		}
	}
	return nil
}

func cloneAnyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func successfulPluginIDs(regs []Registration, outcomes []registrationToolsResult) []string {
	ready := make(map[string]bool)
	for i, reg := range regs {
		if reg.PluginID == "" || !reg.Enabled {
			continue
		}
		if _, ok := ready[reg.PluginID]; !ok {
			ready[reg.PluginID] = true
		}
		if i >= len(outcomes) || !outcomes[i].ready {
			ready[reg.PluginID] = false
		}
	}
	ids := make([]string, 0, len(ready))
	for id := range ready {
		if ready[id] {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

// freshCatalog reports whether the registration carries a persisted catalog
// good enough to build proxies without connecting.
func freshCatalog(reg Registration) ([]CatalogTool, bool) {
	if reg.Status != StatusOK {
		return nil, false
	}
	if reg.ProbedAt.IsZero() || time.Since(reg.ProbedAt) > catalogMaxAge {
		return nil, false
	}
	return reg.Tools, true
}

// discover cold-probes one server via the service, which persists both success
// and failure, then returns proxies from the refreshed catalog.
func (p *ToolProvider) discover(ctx context.Context, reg Registration, owner CredentialOwner) ([]tools.Tool, []CatalogTool, bool, string, string) {
	updated, err := p.svc.Probe(ctx, reg, owner)
	if err != nil {
		p.log.Warn("mcp cold discovery failed; skipping server", "server", reg.Name, "url", diagnostic.Endpoint(reg.URL), "error", err)
		return nil, nil, false, "error", err.Error()
	}
	if updated.Status != StatusOK {
		p.log.Warn("mcp probe failed; skipping server", "server", reg.Name, "url", diagnostic.Endpoint(reg.URL), "status", updated.Status, "reason", updated.StatusError)
		return nil, nil, false, updated.Status, updated.StatusError
	}
	catalog, ok := freshCatalog(updated)
	if !ok {
		// ok with an empty catalog: the server advertised no tools.
		return nil, catalog, true, updated.Status, updated.StatusError
	}
	return p.catalogProxies(updated, catalog, owner), catalog, true, updated.Status, updated.StatusError
}

func (p *ToolProvider) catalogProxies(reg Registration, catalog []CatalogTool, owner CredentialOwner) []tools.Tool {
	out := make([]tools.Tool, 0, len(catalog))
	conn := &serverConn{svc: p.svc, reg: reg, owner: owner}
	for _, ct := range catalog {
		name := exportedToolName(reg, ct.Name)
		if name == "" {
			// A registration without a trusted package identity is legacy state.
			// It cannot enter the model-facing registry under a guessed name.
			p.log.Warn("mcp catalog has no exported package identity; skipping tool", "server", reg.Name, "tool", ct.Name)
			continue
		}
		out = append(out, &toolProxy{
			svc:        p.svc,
			reg:        reg,
			conn:       conn,
			remoteName: ct.Name,
			def: tools.Definition{
				Name:        name,
				Description: ct.Description,
				InputSchema: cloneSchema(ct.InputSchema),
			},
		})
	}
	return out
}

func exportedToolName(reg Registration, remoteName string) string {
	name, _ := agentpackage.ExportedToolName(reg.PluginID, reg.ServerKey, remoteName)
	return name
}

func cloneSchema(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{"type": "object"}
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return map[string]any{"type": "object"}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return map[string]any{"type": "object"}
	}
	return out
}
