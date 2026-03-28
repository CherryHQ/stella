package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

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
	expectedToken, err := readRuntimeToken()
	if err != nil {
		return true, err
	}
	if token != expectedToken {
		return true, fmt.Errorf("internal plugin runtime token mismatch")
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

		return true, pluginhost.ServeTool(context.Background(), def, runtime, os.Stdin, os.Stdout)
	default:
		return true, fmt.Errorf("unknown internal plugin runtime mode %q", mode)
	}
}

func readRuntimeToken() (string, error) {
	fdRaw := os.Getenv("ANNA_INTERNAL_PLUGIN_TOKEN_FD")
	if fdRaw == "" {
		return "", fmt.Errorf("internal plugin runtime token fd is required")
	}
	fd, err := strconv.Atoi(fdRaw)
	if err != nil {
		return "", fmt.Errorf("invalid internal plugin runtime token fd %q", fdRaw)
	}
	file := os.NewFile(uintptr(fd), "anna-internal-plugin-token")
	if file == nil {
		return "", fmt.Errorf("invalid internal plugin runtime token fd %q", fdRaw)
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(file)
	if err != nil {
		return "", fmt.Errorf("read internal plugin runtime token: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("internal plugin runtime token is empty")
	}
	return token, nil
}
