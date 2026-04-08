package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/vaayne/anna/pkg/tools"
)

type Tool struct {
	manager *Manager
}

func New(manager *Manager) *Tool {
	if manager == nil {
		manager = DefaultManager()
	}
	return &Tool{manager: manager}
}

func (t *Tool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "mcp",
		Description: "Proxy MCP tools managed by the MCP plugin. Use action=list to inspect tools, action=get to fetch full schema, and always call get before exec.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"list", "get", "exec"},
					"description": "MCP action to perform.",
				},
				"id": map[string]any{
					"type":        "string",
					"description": "Canonical MCP tool ID, e.g. mcp__server__tool.",
				},
				"args": map[string]any{
					"type":        "object",
					"description": "Arguments for exec. Always call action=get first to inspect schema.",
				},
			},
			"required": []string{"action"},
		},
	}
}

func (t *Tool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action, _ := args["action"].(string)
	switch action {
	case "list":
		return marshalJSON(listResponse(t.manager.ValidTools()))
	case "get":
		id, _ := args["id"].(string)
		if id == "" {
			return "", fmt.Errorf("mcp: id is required for get")
		}
		tool, ok := t.manager.GetTool(id)
		if !ok {
			return "", fmt.Errorf("mcp: unknown tool id %q", id)
		}
		return marshalJSON(tool)
	case "exec":
		id, _ := args["id"].(string)
		if id == "" {
			return "", fmt.Errorf("mcp: id is required for exec")
		}
		execArgs, _ := args["args"].(map[string]any)
		result, err := t.manager.Exec(ctx, id, execArgs)
		if err != nil {
			payload, _ := marshalJSON(result)
			if payload != "" {
				return payload, err
			}
			return "", err
		}
		return marshalJSON(result)
	default:
		return "", fmt.Errorf("mcp: unsupported action %q", action)
	}
}

type listItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	ServerName  string `json:"server_name"`
}

func listResponse(tools []ToolInfo) []listItem {
	items := make([]listItem, 0, len(tools))
	for _, tool := range tools {
		items = append(items, listItem{
			ID:          tool.ID,
			Name:        tool.Name,
			Description: tool.Description,
			ServerName:  tool.ServerName,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].ServerName != items[j].ServerName {
			return items[i].ServerName < items[j].ServerName
		}
		if items[i].Name != items[j].Name {
			return items[i].Name < items[j].Name
		}
		return items[i].ID < items[j].ID
	})
	return items
}

func marshalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
