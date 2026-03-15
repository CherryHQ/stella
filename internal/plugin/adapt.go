package plugin

import (
	"context"

	"github.com/vaayne/anna/internal/agent/tool"
	"github.com/vaayne/anna/internal/toolspec"
	pluginapi "github.com/vaayne/anna/pkg/plugin"
)

// adaptedTool wraps a pluginapi.Tool to satisfy the tool.Tool interface.
type adaptedTool struct {
	inner pluginapi.Tool
}

func (a *adaptedTool) Definition() toolspec.Definition {
	return toolspec.Definition{
		Name:        a.inner.Name,
		Description: a.inner.Description,
		InputSchema: a.inner.InputSchema,
	}
}

func (a *adaptedTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	return a.inner.Execute(ctx, args)
}

// AdaptTool converts a plugin API tool into an internal tool.Tool.
func AdaptTool(t pluginapi.Tool) tool.Tool {
	return &adaptedTool{inner: t}
}
