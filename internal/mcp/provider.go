package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/CherryHQ/stella/pkg/endpoint"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

// defaultConnectTimeout bounds how long connecting to a single server (dial +
// initialize + tools/list) may take before it is skipped. A slow or dead server
// must never stall agent session startup.
const defaultConnectTimeout = 15 * time.Second

// defaultDiscoveryConcurrency caps cold tools/list discovery. A bad system-wide
// MCP fleet should degrade by skipping servers, not by serially stalling runner
// creation for N*timeout.
const defaultDiscoveryConcurrency = 4

// defaultToolCacheTTL bounds how stale a remote server's advertised tool list
// may be. Tool calls still open a fresh session when needed; this cache only
// avoids repeating tools/list during agent session startup.
const defaultToolCacheTTL = 5 * time.Minute

type mcpClient interface {
	ListTools(ctx context.Context) ([]*mcpsdk.Tool, error)
	CallTool(ctx context.Context, name string, args map[string]any) (string, error)
	Close() error
}

type connectFunc func(ctx context.Context, reg Registration, bearer string) (mcpClient, error)

type cachedTool struct {
	remoteName string
	def        pkgtools.Definition
}

type cachedToolList struct {
	updatedAt time.Time
	expiresAt time.Time
	tools     []cachedTool
}

// ToolProvider surfaces the tools of every MCP server visible to a (user, agent)
// context into the agent tool registry, proxying tools/call back to the server.
// A down or misbehaving server is logged and skipped so it can never break an
// agent session.
type ToolProvider struct {
	svc         *Service
	timeout     time.Duration
	ttl         time.Duration
	log         *slog.Logger
	now         func() time.Time
	connect     connectFunc
	concurrency int

	mu    sync.Mutex
	cache map[string]cachedToolList
}

// NewToolProvider builds a provider over the registration service.
func NewToolProvider(svc *Service) *ToolProvider {
	return &ToolProvider{
		svc:         svc,
		timeout:     defaultConnectTimeout,
		ttl:         defaultToolCacheTTL,
		log:         slog.With("component", "mcp"),
		now:         time.Now,
		connect:     connectMCP,
		concurrency: defaultDiscoveryConcurrency,
		cache:       map[string]cachedToolList{},
	}
}

func connectMCP(ctx context.Context, reg Registration, bearer string) (mcpClient, error) {
	return Connect(ctx, reg, bearer)
}

// ToolsForContext connects to each visible, enabled MCP server, lists its tools,
// and returns them namespaced and ready to register. The caller owns the tools'
// lifetime: closing the tool registry closes any client session lazily opened by
// a tool call.
func (p *ToolProvider) ToolsForContext(ctx context.Context, userID, agentID string) []pkgtools.Tool {
	if p == nil || p.svc == nil {
		return nil
	}
	regs, err := p.svc.ResolveForContext(ctx, userID, agentID)
	if err != nil {
		p.log.Warn("resolve mcp servers", "user_id", userID, "agent_id", agentID, "error", err)
		return nil
	}
	discoveryCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	type result struct {
		index int
		tools []pkgtools.Tool
	}
	limit := p.concurrency
	if limit <= 0 {
		limit = defaultDiscoveryConcurrency
	}
	sem := make(chan struct{}, limit)
	results := make(chan result, len(regs))
	var wg sync.WaitGroup
	for i, reg := range regs {
		wg.Add(1)
		go func(index int, reg Registration) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-discoveryCtx.Done():
				return
			}
			results <- result{index: index, tools: p.toolsForServer(discoveryCtx, reg)}
		}(i, reg)
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	byIndex := make([][]pkgtools.Tool, len(regs))
	for res := range results {
		byIndex[res.index] = res.tools
	}
	seen := map[string]struct{}{}
	var out []pkgtools.Tool
	for i, tools := range byIndex {
		kept := false
		for _, tool := range tools {
			name := tool.Definition().Name
			if _, ok := seen[name]; ok {
				p.log.Warn("mcp tool name collision; skipping duplicate", "tool", name)
				continue
			}
			seen[name] = struct{}{}
			out = append(out, tool)
			kept = true
		}
		// Every proxy from one server shares one idempotent client. If collision
		// filtering discarded the whole server, no registry-owned proxy remains to
		// close that client; close one discarded proxy now. Do not close when any
		// sibling survived, or it would invalidate the retained proxies.
		if !kept && len(tools) > 0 {
			if closer, ok := tools[0].(interface{ Close() error }); ok {
				if err := closer.Close(); err != nil {
					p.log.Warn("close fully shadowed mcp server", "server", regs[i].Name, "error", err)
				}
			}
		}
	}
	return out
}

func (p *ToolProvider) toolsForServer(ctx context.Context, reg Registration) []pkgtools.Tool {
	bearer, err := p.svc.BearerToken(ctx, reg)
	if err != nil {
		p.log.Warn("mcp bearer token unavailable; skipping server", "server", reg.Name, "scope", reg.Scope, "error", err)
		return nil
	}

	if tools, ok := p.cachedTools(reg); ok {
		return p.toolProxies(reg, bearer, tools, nil)
	}

	connCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	client, err := p.connect(connCtx, reg, bearer)
	if err != nil {
		p.log.Warn("mcp connect failed; skipping server", "server", reg.Name, "url", endpoint.ForDiagnostic(reg.URL), "error", err)
		return nil
	}
	remoteTools, err := client.ListTools(connCtx)
	if err != nil {
		p.log.Warn("mcp list tools failed; skipping server", "server", reg.Name, "error", err)
		_ = client.Close()
		return nil
	}

	tools := make([]cachedTool, 0, len(remoteTools))
	for _, rt := range remoteTools {
		tools = append(tools, cachedTool{
			remoteName: rt.Name,
			def: pkgtools.Definition{
				Name:        NamespacedToolName(reg.Name, rt.Name),
				Description: rt.Description,
				InputSchema: cloneSchema(toolInputSchema(rt.InputSchema)),
			},
		})
	}
	p.storeCachedTools(reg, tools)
	if len(tools) == 0 {
		_ = client.Close()
		return nil
	}
	return p.toolProxies(reg, bearer, tools, client)
}

func (p *ToolProvider) cachedTools(reg Registration) ([]cachedTool, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.cache[reg.ID]
	if !ok || !entry.updatedAt.Equal(reg.UpdatedAt) || !p.now().Before(entry.expiresAt) {
		return nil, false
	}
	return cloneCachedTools(entry.tools), true
}

func (p *ToolProvider) storeCachedTools(reg Registration, tools []cachedTool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cache[reg.ID] = cachedToolList{
		updatedAt: reg.UpdatedAt,
		expiresAt: p.now().Add(p.ttl),
		tools:     cloneCachedTools(tools),
	}
}

func (p *ToolProvider) toolProxies(reg Registration, bearer string, tools []cachedTool, client mcpClient) []pkgtools.Tool {
	out := make([]pkgtools.Tool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, &toolProxy{
			client:     client,
			connect:    p.connect,
			reg:        reg,
			bearer:     bearer,
			remoteName: tool.remoteName,
			def:        tool.def,
		})
	}
	return out
}

func cloneCachedTools(in []cachedTool) []cachedTool {
	out := make([]cachedTool, len(in))
	for i, tool := range in {
		out[i] = cachedTool{remoteName: tool.remoteName, def: tool.def}
		out[i].def.InputSchema = cloneSchema(tool.def.InputSchema)
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
