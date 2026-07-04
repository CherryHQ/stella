// Package tools provides the sandbox toolset — the agent-facing projections
// of a live sandbox session (bash, read, write, edit). Tools that project
// other capabilities live with those capabilities (internal/goal, internal/notify,
// internal/skills, ...); see buildToolRegistry in internal/agent for assembly.
package tools

import (
	"fmt"

	"github.com/CherryHQ/stella/pkg/sandbox"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

// New returns the four standard sandbox-backed tools (bash, read, write, edit).
func New(host sandbox.Host, toolsBinDir, projectRoot string) []pkgtools.Tool {
	return NewWithSecretResolver(host, toolsBinDir, projectRoot, nil)
}

func NewWithSecretResolver(host sandbox.Host, toolsBinDir, projectRoot string, secretResolver ExecSecretResolver) []pkgtools.Tool {
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

// Definitions returns the canonical definitions for all core tools.
// No sandbox session is required — useful for API metadata endpoints.
func Definitions() []pkgtools.Definition {
	return []pkgtools.Definition{
		bashDefinition(),
		readDefinition(),
		writeDefinition(),
		editDefinition(),
	}
}

func resolveToolPath(host sandbox.Host, projectRoot, path string) (string, error) {
	if projectRoot != "" {
		return pkgtools.ResolveProjectPath(projectRoot, path)
	}
	return host.ResolvePath(path)
}

func resolveWritableToolPath(host sandbox.Host, projectRoot, path string) (string, error) {
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
