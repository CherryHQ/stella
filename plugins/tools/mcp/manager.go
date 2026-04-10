package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"sync"

	officialmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Manager is the shared process-wide runtime for MCP state.
type Manager struct {
	mu          sync.RWMutex
	config      Config
	tools       map[string]ToolInfo
	serverTools map[string][]ToolInfo
	ids         *CanonicalRegistry
	enabled     bool
	servers     map[string]*serverRuntime
	runCtx      context.Context
	runCancel   context.CancelFunc
	dial        DialFunc
	supervisor  SupervisorConfig
}

func NewManager() *Manager {
	return &Manager{
		tools:       map[string]ToolInfo{},
		serverTools: map[string][]ToolInfo{},
		ids:         NewCanonicalRegistry(),
		servers:     map[string]*serverRuntime{},
		dial:        defaultDial,
		supervisor: SupervisorConfig{
			FailureThreshold: defaultFailureThreshold,
			BackoffBase:      defaultBackoffBase,
			BackoffMax:       defaultBackoffMax,
		},
	}
}

func (m *Manager) SetDial(dial DialFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if dial == nil {
		m.dial = defaultDial
		return
	}
	m.dial = dial
}

func (m *Manager) SetSupervisorConfig(cfg SupervisorConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cfg.FailureThreshold > 0 {
		m.supervisor.FailureThreshold = cfg.FailureThreshold
	}
	if cfg.BackoffBase > 0 {
		m.supervisor.BackoffBase = cfg.BackoffBase
	}
	if cfg.BackoffMax > 0 {
		m.supervisor.BackoffMax = cfg.BackoffMax
	}
}

func (m *Manager) Configure(cfg Config, enabled bool) {
	m.Reconcile(context.Background(), cfg, enabled)
}

func (m *Manager) Reconcile(parent context.Context, cfg Config, enabled bool) {
	cfg.Servers = append([]ServerConfig(nil), cfg.Servers...)

	m.mu.Lock()
	m.config = cfg
	m.enabled = enabled
	if !enabled {
		for name := range m.servers {
			m.stopServerLocked(name)
		}
		if m.runCancel != nil {
			m.runCancel()
			m.runCancel = nil
			m.runCtx = nil
		}
		m.serverTools = map[string][]ToolInfo{}
		m.rebuildToolsLocked()
		m.mu.Unlock()
		return
	}
	if m.runCancel == nil {
		m.runCtx, m.runCancel = context.WithCancel(parent)
	}

	wanted := make(map[string]ServerConfig, len(cfg.Servers))
	for _, server := range cfg.EnabledServers() {
		wanted[server.Name] = server.WithDefaults()
	}
	for name, rt := range m.servers {
		newCfg, ok := wanted[name]
		if !ok || !sameServerConfig(rt.cfg, newCfg) {
			m.stopServerLocked(name)
		}
	}
	for name, server := range wanted {
		if _, ok := m.servers[name]; !ok {
			m.startServerLocked(m.runCtx, server)
		}
	}
	m.mu.Unlock()
}

func (m *Manager) Enabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.enabled
}

func (m *Manager) Config() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.config
}

func (m *Manager) AddTool(serverName, toolName, displayName, description string, inputSchema, outputSchema, annotations map[string]any) ToolInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	info := ToolInfo{
		ServerName:   serverName,
		ToolName:     toolName,
		Name:         displayName,
		Description:  description,
		InputSchema:  cloneMap(inputSchema),
		OutputSchema: cloneMap(outputSchema),
		Annotations:  cloneMap(annotations),
		Valid:        true,
	}
	m.serverTools[serverName] = append(m.serverTools[serverName], info)
	m.rebuildToolsLocked()
	for _, tool := range m.serverTools[serverName] {
		if tool.ToolName == toolName {
			return tool
		}
	}
	return ToolInfo{}
}

func (m *Manager) ListTools() []ToolInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]ToolInfo, 0, len(m.tools))
	for _, tool := range m.tools {
		result = append(result, tool)
	}
	return result
}

func (m *Manager) ValidTools() []ToolInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]ToolInfo, 0, len(m.tools))
	for _, tool := range m.tools {
		if tool.Valid {
			result = append(result, tool)
		}
	}
	return result
}

func (m *Manager) GetTool(id string) (ToolInfo, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tool, ok := m.tools[id]
	return tool, ok
}

func (m *Manager) Resolve(id string) (Target, bool) {
	return m.ids.Resolve(id)
}

func (m *Manager) Exec(ctx context.Context, id string, args map[string]any) (ExecResult, error) {
	tool, ok := m.GetTool(id)
	if !ok {
		return ExecResult{}, fmt.Errorf("mcp: unknown tool id %q", id)
	}
	m.mu.RLock()
	rt := m.servers[tool.ServerName]
	m.mu.RUnlock()
	if rt == nil || rt.session == nil {
		return ExecResult{}, fmt.Errorf("mcp: server %q is not connected", tool.ServerName)
	}
	res, err := rt.session.CallTool(ctx, &officialmcp.CallToolParams{Name: tool.ToolName, Arguments: args})
	if err != nil {
		return ExecResult{}, err
	}
	return normalizeExecResult(tool, res)
}

func (m *Manager) replaceServerTools(serverName string, infos []ToolInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]ToolInfo, len(infos))
	copy(copied, infos)
	m.serverTools[serverName] = copied
	m.rebuildToolsLocked()
}

func (m *Manager) clearServerTools(serverName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clearServerToolsLocked(serverName)
}

func (m *Manager) clearServerToolsLocked(serverName string) {
	delete(m.serverTools, serverName)
	m.rebuildToolsLocked()
}

func (m *Manager) rebuildToolsLocked() {
	m.ids.Reset()
	m.tools = map[string]ToolInfo{}
	serverNames := make([]string, 0, len(m.serverTools))
	for serverName := range m.serverTools {
		serverNames = append(serverNames, serverName)
	}
	sort.Strings(serverNames)
	for _, serverName := range serverNames {
		infos := append([]ToolInfo(nil), m.serverTools[serverName]...)
		sort.Slice(infos, func(i, j int) bool {
			if infos[i].ToolName != infos[j].ToolName {
				return infos[i].ToolName < infos[j].ToolName
			}
			if infos[i].Name != infos[j].Name {
				return infos[i].Name < infos[j].Name
			}
			return infos[i].Description < infos[j].Description
		})
		for _, info := range infos {
			info.ID = m.ids.Add(info.ServerName, info.ToolName)
			m.tools[info.ID] = info
		}
	}
}

func normalizeExecResult(tool ToolInfo, res *officialmcp.CallToolResult) (ExecResult, error) {
	if res == nil {
		return ExecResult{}, fmt.Errorf("mcp: empty tool response")
	}
	return ExecResult{
		OK:         !res.IsError,
		ID:         tool.ID,
		ServerName: tool.ServerName,
		ToolName:   tool.ToolName,
		Content:    normalizeContent(res.Content),
		Structured: anyToMap(res.StructuredContent),
		IsError:    res.IsError,
		Meta:       metaToMap(res.Meta),
	}, nil
}

func normalizeContent(content []officialmcp.Content) any {
	items := make([]any, 0, len(content))
	for _, item := range content {
		if item == nil {
			continue
		}
		b, err := item.MarshalJSON()
		if err != nil {
			continue
		}
		var decoded any
		if err := json.Unmarshal(b, &decoded); err != nil {
			continue
		}
		items = append(items, decoded)
	}
	return items
}

func metaToMap(meta officialmcp.Meta) map[string]any {
	if len(meta) == 0 {
		return nil
	}
	out := make(map[string]any, len(meta))
	maps.Copy(out, meta)
	return out
}

func sameServerConfig(a, b ServerConfig) bool {
	if a.Name != b.Name || a.Enabled != b.Enabled || a.Transport != b.Transport || a.Command != b.Command || a.URL != b.URL || a.TimeoutSeconds != b.TimeoutSeconds {
		return false
	}
	if len(a.Args) != len(b.Args) {
		return false
	}
	for i := range a.Args {
		if a.Args[i] != b.Args[i] {
			return false
		}
	}
	if !sameStringMap(a.Env, b.Env) || !sameStringMap(a.Headers, b.Headers) {
		return false
	}
	return true
}

func sameStringMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

var (
	defaultManagerMu sync.RWMutex
	defaultManager   = NewManager()
)

func DefaultManager() *Manager {
	defaultManagerMu.RLock()
	defer defaultManagerMu.RUnlock()
	return defaultManager
}

func SetDefaultManager(manager *Manager) {
	if manager == nil {
		manager = NewManager()
	}
	defaultManagerMu.Lock()
	defaultManager = manager
	defaultManagerMu.Unlock()
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	maps.Copy(out, in)
	return out
}
