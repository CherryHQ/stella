package sandbox

import (
	"strings"

	"github.com/CherryHQ/stella/internal/vision"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

// NewTools returns the sandbox-backed tools. The model-facing image tool is
// always present; it routes to pixels, vision text, or an actionable error.
func NewTools(host pkgsandbox.Session, sessionSecretValues *SessionSecretValues, visionSvc *vision.Service) []pkgtools.Tool {
	if host == nil {
		return nil
	}
	projectRoot := host.WorkingDir()
	out := []pkgtools.Tool{
		newBashTool(host, projectRoot, sessionSecretValues),
		newViewImageTool(host, visionServiceAdapter{service: visionSvc}),
	}
	return out
}

// ReservedToolDefinitions returns every core name plugins may never claim.
// vllm remains reserved for compatibility with existing tool overrides.
func ReservedToolDefinitions() []pkgtools.Definition {
	return []pkgtools.Definition{
		bashDefinition(),
		viewImageDefinition(),
		vllmDefinition(),
	}
}

// ToolAvailability pairs API metadata with current runtime availability. The
// catalog still shows unavailable core tools, but it no longer calls them enabled.
type ToolAvailability struct {
	Definition pkgtools.Definition
	Available  bool
}

// ToolDefinitionsWithAvailability returns the model-facing core catalog.
// Keep this explicit rather than deriving it from the larger reservation set:
// vllm is reserved but no longer a visible tool.
func ToolDefinitionsWithAvailability() []ToolAvailability {
	return []ToolAvailability{
		{Definition: bashDefinition(), Available: true},
		{Definition: viewImageDefinition(), Available: true},
	}
}

// resolveToolExpression expands only the model-authored leading filesystem
// variable before project resolution. env and the FileAccess that consumes the
// result must come from the same selected sandbox.FileView.
func resolveToolExpression(env map[string]string, projectRoot, path string) (string, error) {
	expanded := path
	if strings.HasPrefix(path, "$") {
		var err error
		expanded, err = pkgsandbox.ExpandPathVariables(path, env)
		if err != nil {
			return "", err
		}
	}
	if projectRoot == "" {
		return expanded, nil
	}
	return pkgtools.ResolveProjectPath(projectRoot, expanded)
}

func toolIntArg(args map[string]any, key string, defaultVal int) int {
	v, ok := args[key]
	if !ok {
		return defaultVal
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int32:
		return int(n)
	case int64:
		return int(n)
	default:
		return defaultVal
	}
}
