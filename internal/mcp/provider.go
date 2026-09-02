package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/CherryHQ/stella/internal/platform/diagnostic"
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

// ToolsForContext returns the tools of every visible, enabled MCP server,
// namespaced and ready to register. The caller owns the tools' lifetime:
// closing the tool registry closes any client session lazily opened by a tool
// call.
func (p *ToolProvider) ToolsForContext(ctx context.Context, userID, agentID string) []tools.Tool {
	if p == nil || p.svc == nil {
		return nil
	}
	regs, err := p.svc.ResolveForContext(ctx, userID, agentID)
	if err != nil {
		p.log.Warn("resolve mcp servers", "user_id", userID, "agent_id", agentID, "error", err)
		return nil
	}

	type result struct {
		index int
		tools []tools.Tool
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
		if reg.Status == StatusNeedsAuth {
			// Skip without connecting: the last credential was rejected and only
			// a reconnect from the Web UI can fix it.
			continue
		}
		if catalog, ok := freshCatalog(reg); ok {
			results <- result{index: i, tools: p.catalogProxies(reg, catalog)}
			continue
		}
		wg.Add(1)
		go func(index int, reg Registration) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-discoveryCtx.Done():
				return
			}
			results <- result{index: index, tools: p.discover(discoveryCtx, reg)}
		}(i, reg)
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect by registration index so name collisions resolve by scope
	// precedence (regs is ordered most-specific-first), not by arrival order.
	byIndex := make([][]tools.Tool, len(regs))
	for res := range results {
		byIndex[res.index] = res.tools
	}
	seen := map[string]struct{}{}
	var out []tools.Tool
	for _, serverTools := range byIndex {
		for _, tool := range serverTools {
			name := tool.Definition().Name
			if _, ok := seen[name]; ok {
				p.log.Warn("mcp tool name collision; skipping duplicate", "tool", name)
				continue
			}
			seen[name] = struct{}{}
			out = append(out, tool)
		}
	}
	return out
}

// freshCatalog reports whether the registration carries a persisted catalog
// good enough to build proxies without connecting.
func freshCatalog(reg Registration) ([]CatalogTool, bool) {
	if reg.Status != StatusOK || len(reg.Tools) == 0 {
		return nil, false
	}
	if reg.ProbedAt.IsZero() || time.Since(reg.ProbedAt) > catalogMaxAge {
		return nil, false
	}
	return reg.Tools, true
}

// discover cold-probes one server via the service, which persists both success
// and failure, then returns proxies from the refreshed catalog.
func (p *ToolProvider) discover(ctx context.Context, reg Registration) []tools.Tool {
	updated, err := p.svc.Probe(ctx, reg)
	if err != nil {
		p.log.Warn("mcp cold discovery failed; skipping server", "server", reg.Name, "url", diagnostic.Endpoint(reg.URL), "error", err)
		return nil
	}
	if updated.Status != StatusOK {
		p.log.Warn("mcp probe failed; skipping server", "server", reg.Name, "url", diagnostic.Endpoint(reg.URL), "status", updated.Status, "reason", updated.StatusError)
		return nil
	}
	catalog, ok := freshCatalog(updated)
	if !ok {
		// ok with an empty catalog: the server advertised no tools.
		return nil
	}
	return p.catalogProxies(updated, catalog)
}

func (p *ToolProvider) catalogProxies(reg Registration, catalog []CatalogTool) []tools.Tool {
	out := make([]tools.Tool, 0, len(catalog))
	conn := &serverConn{svc: p.svc, reg: reg}
	for _, ct := range catalog {
		out = append(out, &toolProxy{
			svc:        p.svc,
			reg:        reg,
			conn:       conn,
			remoteName: ct.Name,
			def: tools.Definition{
				Name:        NamespacedToolName(reg.Name, ct.Name),
				Description: ct.Description,
				InputSchema: cloneSchema(ct.InputSchema),
			},
		})
	}
	return out
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
