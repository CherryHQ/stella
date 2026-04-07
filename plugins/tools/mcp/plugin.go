package mcp

import (
	"context"
	"sort"
	"time"

	annamcp "github.com/vaayne/anna/internal/mcp"
	pkgmcp "github.com/vaayne/anna/pkg/mcp"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	"github.com/vaayne/anna/pkg/tools"
)

const (
	PluginID    = "tool/mcp"
	RuntimeName = "manager"
)

func init() {
	pkgplugins.Register(PluginID, pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.Registry().RegisterConfig(pkgplugins.ConfigRegistration{
			PluginID:      PluginID,
			DefaultConfig: func() map[string]any { return map[string]any{"servers": []any{}} },
			Schema:        configSchema(),
			Validate:      func(raw map[string]any) error { _, err := pkgmcp.DecodeConfig(raw); return err },
			Redact: func(raw map[string]any) map[string]any {
				cfg, err := pkgmcp.DecodeConfig(raw)
				if err != nil {
					return raw
				}
				out := map[string]any{"servers": make([]any, 0, len(cfg.Servers))}
				for _, server := range cfg.Servers {
					out["servers"] = append(out["servers"].([]any), map[string]any{
						"name": server.Name, "enabled": server.Enabled, "transport": server.Transport, "command": server.Command,
						"args": server.Args, "env": redactStringMap(server.Env), "url": server.URL, "headers": redactStringMap(server.Headers), "timeout_seconds": server.TimeoutSeconds,
					})
				}
				return out
			},
		})
		host.Registry().RegisterRuntime(pkgplugins.RuntimeRegistration{PluginID: PluginID, Name: RuntimeName, Factory: func(ctx pkgplugins.RuntimeContext) (pkgplugins.ManagedRuntime, error) {
			return &managedRuntime{manager: annamcp.NewManager()}, nil
		}})
		host.Registry().RegisterTool(pkgplugins.ToolRegistration{PluginID: PluginID, Name: "mcp", Description: "Proxy MCP tools managed by the MCP plugin.", Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
			rt, ok := LookupRuntime(ctx.Services)
			if !ok {
				return New(nil), nil
			}
			return New(rt.Manager()), nil
		}})
		host.Registry().RegisterStatus(pkgplugins.StatusRegistration{PluginID: PluginID, Get: func(ctx context.Context) (any, error) {
			rt, ok := LookupRuntime(host.Services())
			if !ok {
				return map[string]any{"servers": []annamcp.ServerStatus{}}, nil
			}
			return map[string]any{"servers": rt.Manager().Statuses()}, nil
		}})
		host.Registry().RegisterPromptInventory(pkgplugins.PromptInventoryRegistration{PluginID: PluginID, Name: "tools", GetTools: func(ctx context.Context) ([]pkgplugins.PromptToolInfo, error) {
			rt, ok := LookupRuntime(host.Services())
			if !ok {
				return nil, nil
			}
			items := make([]pkgplugins.PromptToolInfo, 0, len(rt.Manager().ValidTools()))
			for _, tool := range rt.Manager().ValidTools() {
				items = append(items, pkgplugins.PromptToolInfo{Name: tool.ID, Description: tool.Description, Metadata: map[string]any{"server_name": tool.ServerName}})
			}
			sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
			return items, nil
		}})
	}))
}

func configSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"servers": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name": map[string]any{
							"type":        "string",
							"description": "Unique server name.",
						},
						"enabled": map[string]any{
							"type":        "boolean",
							"description": "Whether the server should be connected.",
							"default":     true,
						},
						"transport": map[string]any{
							"type":        "string",
							"enum":        []any{pkgmcp.TransportStdio, pkgmcp.TransportSSE, pkgmcp.TransportStreamableHTTP, pkgmcp.TransportHTTP},
							"description": "How Anna connects to the MCP server.",
						},
						"command": map[string]any{
							"type":        "string",
							"description": "Program to launch for stdio transport.",
						},
						"args": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "string"},
							"description": "Command-line arguments for stdio transport.",
						},
						"env": map[string]any{
							"type":                 "object",
							"additionalProperties": map[string]any{"type": "string"},
							"description":          "Environment variables for stdio transport.",
						},
						"url": map[string]any{
							"type":        "string",
							"description": "Remote endpoint URL for HTTP, SSE, or streamable HTTP transport.",
						},
						"headers": map[string]any{
							"type":                 "object",
							"additionalProperties": map[string]any{"type": "string"},
							"description":          "HTTP headers for remote transports.",
						},
						"timeout_seconds": map[string]any{
							"type":        "integer",
							"minimum":     0,
							"default":     pkgmcp.DefaultTimeoutSeconds,
							"description": "Timeout for connection and discovery operations.",
						},
					},
					"required": []any{"name", "transport"},
				},
			},
		},
	}
}

type runtimeAccessor interface{ Manager() *annamcp.Manager }

type managedRuntime struct{ manager *annamcp.Manager }

func (r *managedRuntime) Manager() *annamcp.Manager { return r.manager }
func (r *managedRuntime) RuntimeAccessor() any      { return runtimeWrapper{manager: r.manager} }
func (r *managedRuntime) Apply(ctx context.Context, desired pkgplugins.PluginState) error {
	cfg, err := annamcp.DecodeConfig(desired.Config)
	if err != nil {
		return err
	}
	r.manager.Reconcile(ctx, cfg, desired.Enabled)
	return nil
}
func (r *managedRuntime) Stop(ctx context.Context) error {
	r.manager.Reconcile(ctx, annamcp.Config{}, false)
	return nil
}
func (r *managedRuntime) Snapshot(ctx context.Context) (pkgplugins.RuntimeSnapshot, error) {
	statuses := r.manager.Statuses()
	state := pkgplugins.RuntimeStateStopped
	for _, status := range statuses {
		if status.State == "running" {
			state = pkgplugins.RuntimeStateRunning
			break
		}
	}
	if r.manager.Enabled() && state == pkgplugins.RuntimeStateStopped {
		state = pkgplugins.RuntimeStateUnknown
	}
	return pkgplugins.RuntimeSnapshot{State: state, UpdatedAt: time.Now().UTC(), Metadata: map[string]any{"servers": statuses}}, nil
}

func LookupRuntime(host pkgplugins.ServiceHost) (runtimeAccessor, bool) {
	if host == nil {
		return nil, false
	}
	handle, ok := host.Runtime().Get(PluginID, RuntimeName)
	if !ok {
		return nil, false
	}
	accessor, ok := handle.(runtimeAccessorHandle)
	if !ok {
		return nil, false
	}
	runtime, ok := accessor.RuntimeAccessor().(runtimeAccessor)
	if !ok {
		return nil, false
	}
	return runtime, true
}

type runtimeAccessorHandle interface{ RuntimeAccessor() any }

func redactStringMap(src map[string]string) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for k := range src {
		out[k] = "***"
	}
	return out
}
