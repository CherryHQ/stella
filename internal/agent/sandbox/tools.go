package sandbox

import (
	"strings"

	"github.com/CherryHQ/stella/internal/vision"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

// NewTools returns the four standard sandbox-backed tools (bash, read, write, edit).
// visionSvc renders images as text for the read tool when the running model
// cannot see; nil is allowed and degrades to local Xberg extraction.
func NewTools(host pkgsandbox.Host, toolsBinDir, projectRoot string, sessionSecretValues *SessionSecretValues, visionSvc *vision.Service) []pkgtools.Tool {
	if host == nil {
		return nil
	}
	return []pkgtools.Tool{
		newBashTool(host, toolsBinDir, projectRoot, sessionSecretValues),
		newReadTool(host, projectRoot, visionSvc),
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
	resolved, err := resolveToolExpression(host, projectRoot, path)
	if err != nil {
		return "", err
	}
	return host.ResolvePath(resolved)
}

func resolveWritableToolPath(host pkgsandbox.Host, projectRoot, path string) (string, error) {
	resolved, err := resolveToolExpression(host, projectRoot, path)
	if err != nil {
		return "", err
	}
	return host.ResolveWritePath(resolved)
}

// resolveToolExpression expands only the model-authored leading filesystem
// variable before project resolution; session path resolvers stay literal.
func resolveToolExpression(host pkgsandbox.Host, projectRoot, path string) (string, error) {
	expanded := path
	if strings.HasPrefix(path, "$") {
		var err error
		expanded, err = pkgsandbox.ExpandPathVariables(path, host.Policy().Env)
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
