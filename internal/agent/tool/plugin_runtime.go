package tool

import (
	"fmt"

	"github.com/vaayne/anna/internal/pluginhost"
)

var builtinToolNames = []string{"read", "bash", "edit", "write", "webfetch"}

func BuiltinToolNames() []string {
	names := make([]string, len(builtinToolNames))
	copy(names, builtinToolNames)
	return names
}

// augmentDefinition injects runtime-specific args and metadata into a catalog
// definition loaded from disk. This adds --work-dir, --user-data-dir flags
// and corresponding metadata that the subprocess plugin host uses.
func augmentDefinition(def pluginhost.Definition, workDir, userDataDir string) pluginhost.Definition {
	def.Manifest.Args = append(def.Manifest.Args, "--work-dir", workDir)
	if def.Manifest.Metadata == nil {
		def.Manifest.Metadata = make(map[string]any)
	}
	def.Manifest.Metadata["work_dir"] = workDir
	def.Manifest.Metadata["user_data_dir"] = userDataDir
	if userDataDir != "" {
		def.Manifest.Args = append(def.Manifest.Args, "--user-data-dir", userDataDir)
	}
	return def
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
