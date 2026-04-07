package mcp

import (
	"context"
	"fmt"
	"sync"
)

// ToolInfo is Anna's cached normalized view of one MCP tool.
type ToolInfo struct {
	ID           string         `json:"id"`
	ServerName   string         `json:"server_name"`
	ToolName     string         `json:"tool_name"`
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"input_schema,omitempty"`
	OutputSchema map[string]any `json:"output_schema,omitempty"`
	Annotations  map[string]any `json:"annotations,omitempty"`
	Valid        bool           `json:"valid"`
}

// ExecResult is the normalized result shape returned by the mcp tool.
type ExecResult struct {
	OK         bool           `json:"ok"`
	ID         string         `json:"id"`
	ServerName string         `json:"server_name"`
	ToolName   string         `json:"tool_name"`
	Content    any            `json:"content,omitempty"`
	Structured map[string]any `json:"structured,omitempty"`
	IsError    bool           `json:"is_error"`
	Meta       map[string]any `json:"meta,omitempty"`
}

// Manager is the shared process-wide runtime for MCP state.
type Manager struct {
	mu      sync.RWMutex
	config  Config
	tools   map[string]ToolInfo
	ids     *CanonicalRegistry
	enabled bool
}

func NewManager() *Manager {
	return &Manager{
		tools: map[string]ToolInfo{},
		ids:   NewCanonicalRegistry(),
	}
}

func (m *Manager) Configure(cfg Config, enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.config = cfg
	m.enabled = enabled
	m.tools = map[string]ToolInfo{}
	m.ids.Reset()
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

func (m *Manager) RegisterTool(serverName, toolName, displayName, description string, inputSchema, outputSchema, annotations map[string]any) ToolInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.ids.Add(serverName, toolName)
	info := ToolInfo{
		ID:           id,
		ServerName:   serverName,
		ToolName:     toolName,
		Name:         displayName,
		Description:  description,
		InputSchema:  cloneMap(inputSchema),
		OutputSchema: cloneMap(outputSchema),
		Annotations:  cloneMap(annotations),
		Valid:        true,
	}
	m.tools[id] = info
	return info
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

func (m *Manager) Exec(_ context.Context, id string, _ map[string]any) (ExecResult, error) {
	tool, ok := m.GetTool(id)
	if !ok {
		return ExecResult{}, fmt.Errorf("mcp: unknown tool id %q", id)
	}
	return ExecResult{
		OK:         false,
		ID:         tool.ID,
		ServerName: tool.ServerName,
		ToolName:   tool.ToolName,
		IsError:    true,
		Meta: map[string]any{
			"status": "not_implemented",
		},
	}, fmt.Errorf("mcp: exec not implemented")
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
	for k, v := range in {
		out[k] = v
	}
	return out
}
