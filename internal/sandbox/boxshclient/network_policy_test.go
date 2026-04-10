package boxshclient

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/embedded"
)

// skipIfWindowsNetwork skips the test on Windows
func skipIfWindowsNetwork(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Network policy tests require Linux/macOS boxsh")
	}
}

// writeMockBoxshWithNetworkLogging creates a mock boxsh that logs network configuration
func writeMockBoxshWithNetworkLogging(t *testing.T, annaHome string, logFile string) {
	t.Helper()
	_ = embedded.EnsureTools(annaHome)
	binDir := filepath.Join(annaHome, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	boxshPath := filepath.Join(binDir, "boxsh")

	script := `#!/bin/bash
if [[ "$1" == "--version" ]]; then
	echo "boxsh 2.0.1"
	exit 0
fi

LOGFILE="` + logFile + `"

# Log the arguments (including network config)
echo "boxsh started with args: $*" >> "$LOGFILE"

# Extract network mode from args
NETWORK_MODE="disabled"
ALLOWLIST=""
for ((i=1; i<=$#; i++)); do
	arg="${!i}"
	if [[ "$arg" == "--net=none" ]]; then
		NETWORK_MODE="disabled"
	elif [[ "$arg" == "--net=allow" ]]; then
		NETWORK_MODE="allow_all"
	elif [[ "$arg" == "--net=whitelist" ]]; then
		NETWORK_MODE="whitelist"
	elif [[ "$arg" == "--allow" ]]; then
		next=$((i+1))
		ALLOWLIST="$ALLOWLIST ${!next}"
	fi
done

echo "network_mode=$NETWORK_MODE" >> "$LOGFILE"
echo "allowlist=$ALLOWLIST" >> "$LOGFILE"

# JSON-RPC loop
while IFS= read -r line || [[ -n "$line" ]]; do
	method=$(echo "$line" | grep -o '"method":"[^"]*"' | cut -d'"' -f4)
	id=$(echo "$line" | grep -o '"id":[0-9]*' | cut -d: -f2)
	
	if [[ -z "$method" ]]; then
		continue
	fi
	
	case "$method" in
		ping)
			echo "{\"jsonrpc\":\"2.0\",\"result\":\"pong\",\"id\":$id}"
			;;
		exec)
			echo "{\"jsonrpc\":\"2.0\",\"result\":{\"stdout\":\"executed with network=$NETWORK_MODE\",\"stderr\":\"\",\"exit_code\":0},\"id\":$id}"
			;;
		read|write|edit)
			echo "{\"jsonrpc\":\"2.0\",\"result\":{\"content\":\"ok\",\"exists\":true},\"id\":$id}"
			;;
		close|quit)
			echo "{\"jsonrpc\":\"2.0\",\"result\":{\"status\":\"closed\"},\"id\":$id}"
			exit 0
			;;
	esac
done
`

	if err := os.WriteFile(boxshPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// TestNetworkPolicy_DisabledMode verifies that disabled network mode is properly configured.
func TestNetworkPolicy_DisabledMode(t *testing.T) {
	skipIfWindowsNetwork(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	annaHome := t.TempDir()
	workspace := t.TempDir()
	logFile := filepath.Join(annaHome, "network.log")

	writeMockBoxshWithNetworkLogging(t, annaHome, logFile)

	cfg := BackendConfig{
		AnnaHome:  annaHome,
		Workspace: workspace,
		WorkDir:   "/",
		Sandbox: config.SandboxConfig{
			Network: config.SandboxNetworkConfig{
				Mode: config.SandboxNetworkDisabled,
			},
		},
	}

	backend, err := NewSharedBackend(cfg)
	if err != nil {
		t.Fatalf("NewSharedBackend: %v", err)
	}
	defer func() { _ = backend.Close() }()

	if err := backend.Start(ctx, cfg); err != nil {
		t.Fatalf("backend.Start: %v", err)
	}

	// Verify backend is alive
	if !backend.Alive() {
		t.Fatal("backend should be alive")
	}

	// Execute a command to verify network mode was passed
	bashTool := NewBashAdapter(backend, "")
	result, err := bashTool.Execute(ctx, map[string]any{
		"command": "echo test",
	})
	if err != nil {
		t.Fatalf("bashTool.Execute: %v", err)
	}

	if !strings.Contains(result, "network=disabled") {
		t.Errorf("Expected result to contain 'network=disabled', got: %s", result)
	}

	t.Logf("Network disabled mode confirmed: %s", result)
}

// TestNetworkPolicy_AllowAllMode verifies that allow_all network mode is properly configured.
func TestNetworkPolicy_AllowAllMode(t *testing.T) {
	skipIfWindowsNetwork(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	annaHome := t.TempDir()
	workspace := t.TempDir()
	logFile := filepath.Join(annaHome, "network.log")

	writeMockBoxshWithNetworkLogging(t, annaHome, logFile)

	cfg := BackendConfig{
		AnnaHome:  annaHome,
		Workspace: workspace,
		WorkDir:   "/",
		Sandbox: config.SandboxConfig{
			Network: config.SandboxNetworkConfig{
				Mode: config.SandboxNetworkAllowAll,
			},
		},
	}

	backend, err := NewSharedBackend(cfg)
	if err != nil {
		t.Fatalf("NewSharedBackend: %v", err)
	}
	defer func() { _ = backend.Close() }()

	if err := backend.Start(ctx, cfg); err != nil {
		t.Fatalf("backend.Start: %v", err)
	}

	bashTool := NewBashAdapter(backend, "")
	result, err := bashTool.Execute(ctx, map[string]any{
		"command": "echo test",
	})
	if err != nil {
		t.Fatalf("bashTool.Execute: %v", err)
	}

	if !strings.Contains(result, "network=allow_all") {
		t.Errorf("Expected result to contain 'network=allow_all', got: %s", result)
	}

	t.Logf("Network allow_all mode confirmed: %s", result)
}

// TestNetworkPolicy_WhitelistMode verifies that whitelist network mode is properly configured
// with allowlist entries passed to the boxsh process.
func TestNetworkPolicy_WhitelistMode(t *testing.T) {
	skipIfWindowsNetwork(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	annaHome := t.TempDir()
	workspace := t.TempDir()
	logFile := filepath.Join(annaHome, "network.log")

	writeMockBoxshWithNetworkLogging(t, annaHome, logFile)

	cfg := BackendConfig{
		AnnaHome:  annaHome,
		Workspace: workspace,
		WorkDir:   "/",
		Sandbox: config.SandboxConfig{
			Network: config.SandboxNetworkConfig{
				Mode:      config.SandboxNetworkWhitelist,
				Allowlist: []string{"example.com", "192.168.1.0/24"},
			},
		},
	}

	backend, err := NewSharedBackend(cfg)
	if err != nil {
		t.Fatalf("NewSharedBackend: %v", err)
	}
	defer func() { _ = backend.Close() }()

	if err := backend.Start(ctx, cfg); err != nil {
		t.Fatalf("backend.Start: %v", err)
	}

	bashTool := NewBashAdapter(backend, "")
	result, err := bashTool.Execute(ctx, map[string]any{
		"command": "echo test",
	})
	if err != nil {
		t.Fatalf("bashTool.Execute: %v", err)
	}

	if !strings.Contains(result, "network=whitelist") {
		t.Errorf("Expected result to contain 'network=whitelist', got: %s", result)
	}

	t.Logf("Network whitelist mode confirmed: %s", result)

	// Verify the log file contains the allowlist entries
	logContent, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if !strings.Contains(string(logContent), "example.com") {
		t.Errorf("Expected log to contain 'example.com' in allowlist, got: %s", string(logContent))
	}

	t.Logf("Allowlist entries logged correctly")
}

// TestNetworkPolicy_DefaultIsDisabled verifies that the default network mode is disabled.
func TestNetworkPolicy_DefaultIsDisabled(t *testing.T) {
	skipIfWindowsNetwork(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	annaHome := t.TempDir()
	workspace := t.TempDir()
	logFile := filepath.Join(annaHome, "network.log")

	writeMockBoxshWithNetworkLogging(t, annaHome, logFile)

	// Create config with empty mode (should default to disabled)
	cfg := BackendConfig{
		AnnaHome:  annaHome,
		Workspace: workspace,
		WorkDir:   "/",
		Sandbox: config.SandboxConfig{
			Network: config.SandboxNetworkConfig{
				Mode: "", // Empty mode
			},
		},
	}

	backend, err := NewSharedBackend(cfg)
	if err != nil {
		t.Fatalf("NewSharedBackend: %v", err)
	}
	defer func() { _ = backend.Close() }()

	if err := backend.Start(ctx, cfg); err != nil {
		t.Fatalf("backend.Start: %v", err)
	}

	bashTool := NewBashAdapter(backend, "")
	result, err := bashTool.Execute(ctx, map[string]any{
		"command": "echo test",
	})
	if err != nil {
		t.Fatalf("bashTool.Execute: %v", err)
	}

	// Default should be disabled
	if !strings.Contains(result, "network=disabled") {
		t.Errorf("Expected default network mode to be 'disabled', got: %s", result)
	}

	t.Log("Default network mode is disabled as expected")
}

// TestNetworkPolicy_BuildArgs verifies that the backend correctly builds boxsh arguments
// based on the network configuration.
func TestNetworkPolicy_BuildArgs(t *testing.T) {
	tests := []struct {
		name         string
		networkMode  string
		allowlist    []string
		expectedArgs []string
	}{
		{
			name:         "disabled mode includes --net=none",
			networkMode:  config.SandboxNetworkDisabled,
			allowlist:    nil,
			expectedArgs: []string{"--rpc", "--net=none"},
		},
		{
			name:         "allow_all mode includes --net=allow",
			networkMode:  config.SandboxNetworkAllowAll,
			allowlist:    nil,
			expectedArgs: []string{"--rpc", "--net=allow"},
		},
		{
			name:        "whitelist mode includes --net=whitelist and --allow entries",
			networkMode: config.SandboxNetworkWhitelist,
			allowlist:   []string{"example.com", "192.168.1.0/24"},
			expectedArgs: []string{
				"--rpc",
				"--net=whitelist",
				"--allow", "example.com",
				"--allow", "192.168.1.0/24",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := SessionConfig{
				Src:              "/src",
				Dst:              "/dst",
				Cwd:              "/work",
				NetworkMode:      tt.networkMode,
				NetworkAllowlist: tt.allowlist,
			}

			// Create a client to test buildArgs
			client := New("/usr/bin/boxsh", cfg)
			args := client.buildArgs()

			// Verify each expected argument is present
			for _, expectedArg := range tt.expectedArgs {
				found := false
				for _, arg := range args {
					if arg == expectedArg {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected arg %q not found in %v", expectedArg, args)
				}
			}

			t.Logf("BuildArgs: %v", args)
		})
	}
}
