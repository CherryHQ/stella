package main

import (
	"context"
	"fmt"
	"os"

	agenttool "github.com/vaayne/anna/internal/agent/tool"
	"github.com/vaayne/anna/internal/pluginhost"
)

func maybeRunInternalPluginRuntime() (bool, error) {
	mode := os.Getenv("ANNA_INTERNAL_PLUGIN_MODE")
	if mode == "" {
		return false, nil
	}

	token := os.Getenv("ANNA_INTERNAL_PLUGIN_TOKEN")
	if token == "" {
		return true, fmt.Errorf("internal plugin runtime token is required")
	}

	switch mode {
	case "tool":
		name := os.Getenv("ANNA_INTERNAL_TOOL_NAME")
		if name == "" {
			return true, fmt.Errorf("internal tool plugin name is required")
		}

		workDir := os.Getenv("ANNA_PLUGIN_WORKDIR")
		userDataDir := os.Getenv("ANNA_PLUGIN_USER_DATA_DIR")

		def, runtime, err := agenttool.BuiltinToolPlugin(name, workDir, userDataDir)
		if err != nil {
			return true, err
		}
		metaToken, _ := def.Manifest.Metadata["runtime_token"].(string)
		if metaToken == "" || metaToken != token {
			return true, fmt.Errorf("internal plugin runtime token mismatch")
		}

		return true, pluginhost.ServeTool(context.Background(), def, runtime, os.Stdin, os.Stdout)
	default:
		return true, fmt.Errorf("unknown internal plugin runtime mode %q", mode)
	}
}
