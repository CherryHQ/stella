package sandbox

import (
	"strings"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

// NewTools returns the four standard sandbox-backed tools (bash, read, write, edit).
func NewTools(host pkgsandbox.Session, sessionSecretValues *SessionSecretValues) []pkgtools.Tool {
	if host == nil {
		return nil
	}
	projectRoot := host.WorkingDir()
	return []pkgtools.Tool{
		newBashTool(host, projectRoot, sessionSecretValues),
		newReadTool(host),
		newWriteTool(host),
		newEditTool(host),
	}
}

// ToolDefinitions returns the canonical definitions for all core tools.
// No sandbox session is required — useful for API metadata endpoints.
func ToolDefinitions() []pkgtools.Definition {
	return []pkgtools.Definition{
		bashDefinition(),
		readDefinition(),
		writeDefinition(),
		editDefinition(),
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
