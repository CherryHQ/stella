package tool

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/vaayne/anna/internal/pluginapi"
	"github.com/vaayne/anna/internal/pluginhost"
	"github.com/vaayne/anna/internal/toolspec"
)

type pluginTool struct {
	def        pluginhost.Definition
	supervisor *pluginhost.Supervisor
}

func newPluginTool(def pluginhost.Definition) Tool {
	return &pluginTool{
		def:        def,
		supervisor: pluginhost.NewSupervisor(def, pluginhost.SupervisorOptions{Logger: slog.Default()}),
	}
}

func (t *pluginTool) Definition() toolspec.Definition {
	if t.def.Manifest.Tool != nil {
		return toolspec.Definition{
			Name:        t.def.Manifest.Tool.Name,
			Description: t.def.Manifest.Tool.Description,
			InputSchema: t.def.Manifest.Tool.InputSchema,
		}
	}
	return toolspec.Definition{
		Name:        t.def.Manifest.Name,
		Description: t.def.Manifest.Description,
	}
}

func (t *pluginTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	client, err := t.supervisor.EnsureHealthy(ctx)
	if err != nil {
		return "", fmt.Errorf("plugin tool %s: %w", t.def.Manifest.Name, err)
	}

	var resp pluginapi.ToolCallResponse
	if err := client.Request(ctx, "call_tool", pluginapi.ToolCallRequest{
		Name:      t.def.Manifest.Name,
		Arguments: args,
	}, &resp); err != nil {
		return "", fmt.Errorf("plugin tool %s: %w", t.def.Manifest.Name, err)
	}
	if resp.Error != "" {
		if resp.Output != "" {
			return resp.Output, fmt.Errorf("plugin tool %s: %s", t.def.Manifest.Name, resp.Error)
		}
		return "", fmt.Errorf("plugin tool %s: %s", t.def.Manifest.Name, resp.Error)
	}
	return resp.Output, nil
}

func (t *pluginTool) Close() error {
	return t.supervisor.Close()
}
