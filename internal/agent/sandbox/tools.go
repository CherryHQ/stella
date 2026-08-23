package sandbox

import (
	"strings"

	"github.com/CherryHQ/stella/internal/vision"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

// NewTools returns the sandbox-backed tools. bash, read, write and edit are
// always present; vllm appears only when the deployment configured a vision
// model, because without one it could only ever answer "not configured".
func NewTools(host pkgsandbox.Session, sessionSecretValues *SessionSecretValues, visionSvc *vision.Service) []pkgtools.Tool {
	if host == nil {
		return nil
	}
	projectRoot := host.WorkingDir()
	out := []pkgtools.Tool{
		newBashTool(host, projectRoot, sessionSecretValues),
		newReadTool(host),
		newWriteTool(host),
		newEditTool(host),
	}
	if visionSvc.ModelConfigured() {
		out = append(out, newVLLMTool(host, visionSvc))
	}
	return out
}

// ToolDefinitions returns the canonical definitions for all core tools.
// No sandbox session is required — useful for API metadata endpoints.
//
// vllm is listed unconditionally: this is the set of names that are reserved as
// core and may not be overridden by a plugin, which does not depend on whether
// a given deployment has a vision model wired up today.
func ToolDefinitions() []pkgtools.Definition {
	return []pkgtools.Definition{
		bashDefinition(),
		readDefinition(),
		writeDefinition(),
		editDefinition(),
		vllmDefinition(),
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
