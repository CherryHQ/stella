package tool

import (
	"fmt"
	"os"

	"github.com/vaayne/anna/internal/pluginapi"
	"github.com/vaayne/anna/internal/pluginhost"
	"github.com/vaayne/anna/internal/toolspec"
)

const builtinPluginVersion = "1.0.0"

// BuiltinToolPlugin builds both the subprocess manifest and the local runtime
// tool for a built-in tool name.
func BuiltinToolPlugin(name, workDir, userDataDir string) (pluginhost.Definition, Tool, error) {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}

	var runtime Tool
	var def toolspec.Definition
	var sandbox string
	bashDir := workDir
	if userDataDir != "" {
		sandbox = userDataDir
		bashDir = userDataDir
	}

	switch name {
	case "read":
		runtime = wrapWithSandbox(&ReadTool{}, sandbox, "file_path")
		def = runtime.Definition()
	case "bash":
		runtime = &BashTool{workDir: bashDir}
		def = runtime.Definition()
	case "edit":
		runtime = wrapWithSandbox(&EditTool{}, sandbox, "file_path")
		def = runtime.Definition()
	case "write":
		runtime = wrapWithSandbox(&WriteTool{}, sandbox, "file_path")
		def = runtime.Definition()
	case "webfetch":
		runtime = NewWebFetchTool()
		def = runtime.Definition()
	default:
		return pluginhost.Definition{}, nil, fmt.Errorf("unknown builtin tool plugin: %s", name)
	}

	manifest := pluginapi.Manifest{
		Name:            name,
		Version:         builtinPluginVersion,
		Kind:            pluginapi.KindTool,
		ProtocolVersion: pluginapi.ProtocolVersion,
		Entrypoint:      pluginhost.BuiltinEntrypoint,
		Args: []string{
			"plugin",
			"runtime",
			"tool",
			name,
			"--work-dir",
			workDir,
		},
		Capabilities: []pluginapi.Capability{
			pluginapi.CapabilityToolCall,
			pluginapi.CapabilityHealthCheck,
			pluginapi.CapabilityGracefulShutdown,
		},
		Tool: toolSpecFrom(def),
		Metadata: map[string]any{
			"work_dir":      workDir,
			"user_data_dir": userDataDir,
		},
	}
	if userDataDir != "" {
		manifest.Args = append(manifest.Args, "--user-data-dir", userDataDir)
	}

	return pluginhost.Definition{
		Manifest: manifest,
		RootDir:  cwd,
	}, runtime, nil
}

func toolSpecFrom(def toolspec.Definition) *pluginapi.ToolSpec {
	return &pluginapi.ToolSpec{
		Name:        def.Name,
		Description: def.Description,
		InputSchema: def.InputSchema,
	}
}
