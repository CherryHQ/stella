package tool

import "fmt"

var builtinToolNames = []string{"read", "bash", "edit", "write", "webfetch"}

func BuiltinToolNames() []string {
	names := make([]string, len(builtinToolNames))
	copy(names, builtinToolNames)
	return names
}

// BuiltinToolRuntime returns the local Go runtime for a built-in tool.
// This is used by the subprocess entry-point (anna-plugin) to execute tools.
func BuiltinToolRuntime(name, workDir, userDataDir string) (Tool, error) {
	var sandbox string
	bashDir := workDir
	if userDataDir != "" {
		sandbox = userDataDir
		bashDir = userDataDir
	}

	switch name {
	case "read":
		return wrapWithSandbox(&ReadTool{}, sandbox, "file_path"), nil
	case "bash":
		return &BashTool{workDir: bashDir}, nil
	case "edit":
		return wrapWithSandbox(&EditTool{}, sandbox, "file_path"), nil
	case "write":
		return wrapWithSandbox(&WriteTool{}, sandbox, "file_path"), nil
	case "webfetch":
		return NewWebFetchTool(), nil
	default:
		return nil, fmt.Errorf("unknown builtin tool: %s", name)
	}
}
