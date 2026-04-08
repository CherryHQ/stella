package mcp

import "time"

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

// ServerStatus describes the current runtime state of one managed MCP server.
type ServerStatus struct {
	Name                string    `json:"name"`
	Transport           string    `json:"transport"`
	State               string    `json:"state"`
	Failures            int       `json:"failures"`
	Suppressed          bool      `json:"suppressed"`
	LastError           string    `json:"last_error,omitempty"`
	LastConnectedAt     time.Time `json:"last_connected_at,omitempty"`
	LastDiscoveredAt    time.Time `json:"last_discovered_at,omitempty"`
	DiscoveredToolCount int       `json:"discovered_tool_count"`
}
