package sandbox

import (
	"fmt"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

// NewTools returns the four standard sandbox-backed tools (bash, read, write, edit).
func NewTools(host pkgsandbox.Host, toolsBinDir, projectRoot string, secretResolver ExecSecretResolver) []pkgtools.Tool {
	if host == nil {
		return nil
	}
	return []pkgtools.Tool{
		newBashTool(host, toolsBinDir, projectRoot, secretResolver),
		newReadTool(host, projectRoot),
		newWriteTool(host, projectRoot),
		newEditTool(host, projectRoot),
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

func resolveToolPath(host pkgsandbox.Host, projectRoot, path string) (string, error) {
	if projectRoot != "" {
		resolved, err := pkgtools.ResolveProjectPath(projectRoot, path)
		if err != nil {
			return "", err
		}
		return host.ResolvePath(resolved)
	}
	return host.ResolvePath(path)
}

func resolveWritableToolPath(host pkgsandbox.Host, projectRoot, path string) (string, error) {
	if projectRoot != "" {
		resolved, err := pkgtools.ResolveProjectPath(projectRoot, path)
		if err != nil {
			return "", err
		}
		return host.ResolveWritePath(resolved)
	}
	return host.ResolveWritePath(path)
}

func toolStringSliceArg(args map[string]any, key string) ([]string, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return nil, nil
	}
	items, ok := v.([]any)
	if !ok {
		if ss, ok := v.([]string); ok {
			return ss, nil
		}
		return nil, nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok || s == "" {
			return nil, fmt.Errorf("%s must contain non-empty strings", key)
		}
		out = append(out, s)
	}
	return out, nil
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
