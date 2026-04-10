package boxshclient

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/embedded"
)

func skipIfWindowsNetwork(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Network policy tests require Linux/macOS boxsh")
	}
}

func writeNetworkMockBoxsh(t *testing.T, annaHome string) {
	t.Helper()
	_ = embedded.EnsureTools(annaHome)
	binDir := filepath.Join(annaHome, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	boxshPath := filepath.Join(binDir, "boxsh")

	script := `#!/bin/bash
set -euo pipefail

if [[ "${1:-}" == "--version" ]]; then
	echo "boxsh 2.0.1"
	exit 0
fi

NETWORK_MODE="disabled"
declare -a ALLOWLIST=()
while [[ $# -gt 0 ]]; do
	case "$1" in
		--net=none) NETWORK_MODE="disabled"; shift ;;
		--net=allow) NETWORK_MODE="allow_all"; shift ;;
		--net=whitelist) NETWORK_MODE="whitelist"; shift ;;
		--allow) ALLOWLIST+=("$2"); shift 2 ;;
		--src|--dst|--cwd) shift 2 ;;
		--rpc) shift ;;
		*) shift ;;
	esac
done

json_escape() {
	printf '%s' "$1" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))'
}

host_allowed() {
	local host="$1"
	if [[ "$NETWORK_MODE" == "allow_all" ]]; then
		return 0
	fi
	if [[ "$NETWORK_MODE" != "whitelist" ]]; then
		return 1
	fi
	for allowed in "${ALLOWLIST[@]}"; do
		if [[ "$host" == "$allowed" ]]; then
			return 0
		fi
	done
	return 1
}

probe_tcp() {
	local host="$1"
	local port="$2"
	if exec 3<>"/dev/tcp/$host/$port"; then
		printf 'ping' >&3
		exec 3>&-
		exec 3<&-
		return 0
	fi
	return 1
}

while IFS= read -r line || [[ -n "$line" ]]; do
	method=$(echo "$line" | grep -o '"method":"[^"]*"' | cut -d'"' -f4)
	id=$(echo "$line" | grep -o '"id":[0-9]*' | cut -d: -f2)
	[[ -z "$method" ]] && continue

	case "$method" in
		ping)
			echo "{\"jsonrpc\":\"2.0\",\"result\":\"pong\",\"id\":$id}"
			;;
		exec)
			command=$(echo "$line" | sed -n 's/.*"command":"\([^"]*\)".*/\1/p')
			if [[ "$command" =~ ^probe[[:space:]]+([^[:space:]]+)[[:space:]]+([0-9]+)$ ]]; then
				host="${BASH_REMATCH[1]}"
				port="${BASH_REMATCH[2]}"
				if ! host_allowed "$host"; then
					echo "{\"jsonrpc\":\"2.0\",\"result\":{\"stdout\":\"blocked\",\"stderr\":\"network blocked\",\"exit_code\":1},\"id\":$id}"
				elif probe_tcp "$host" "$port"; then
					echo "{\"jsonrpc\":\"2.0\",\"result\":{\"stdout\":\"connected\",\"stderr\":\"\",\"exit_code\":0},\"id\":$id}"
				else
					echo "{\"jsonrpc\":\"2.0\",\"result\":{\"stdout\":\"dial failed\",\"stderr\":\"dial failed\",\"exit_code\":1},\"id\":$id}"
				fi
			else
				echo "{\"jsonrpc\":\"2.0\",\"result\":{\"stdout\":$(json_escape "$NETWORK_MODE"),\"stderr\":\"\",\"exit_code\":0},\"id\":$id}"
			fi
			;;
		quit|close)
			echo "{\"jsonrpc\":\"2.0\",\"result\":\"bye\",\"id\":$id}"
			exit 0
			;;
	esac
done
`
	if err := os.WriteFile(boxshPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func startTCPProbeServer(t *testing.T) (host string, port int, hits *atomic.Int32, cleanup func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	counter := &atomic.Int32{}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			counter.Add(1)
			_ = conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
			buf := make([]byte, 4)
			_, _ = conn.Read(buf)
			_ = conn.Close()
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port, counter, func() { _ = ln.Close() }
}

func TestNetworkPolicy_DisabledModeBlocksConnections(t *testing.T) {
	skipIfWindowsNetwork(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	annaHome := t.TempDir()
	workspace := t.TempDir()
	writeNetworkMockBoxsh(t, annaHome)
	host, port, hits, cleanup := startTCPProbeServer(t)
	defer cleanup()

	cfg := BackendConfig{AnnaHome: annaHome, Workspace: workspace, WorkDir: "/", Sandbox: config.SandboxConfig{Network: config.SandboxNetworkConfig{Mode: config.SandboxNetworkDisabled}}}
	backend, err := NewSharedBackend(cfg)
	if err != nil {
		t.Fatalf("NewSharedBackend: %v", err)
	}
	defer func() { _ = backend.Close() }()
	if err := backend.Start(ctx, cfg); err != nil {
		t.Fatalf("backend.Start: %v", err)
	}

	_, err = NewBashAdapter(backend, "").Execute(ctx, map[string]any{"command": fmt.Sprintf("probe %s %d", host, port)})
	if err == nil {
		t.Fatal("expected disabled mode to block network access")
	}
	if hits.Load() != 0 {
		t.Fatalf("expected zero network hits in disabled mode, got %d", hits.Load())
	}
}

func TestNetworkPolicy_AllowAllModePermitsConnections(t *testing.T) {
	skipIfWindowsNetwork(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	annaHome := t.TempDir()
	workspace := t.TempDir()
	writeNetworkMockBoxsh(t, annaHome)
	host, port, hits, cleanup := startTCPProbeServer(t)
	defer cleanup()

	cfg := BackendConfig{AnnaHome: annaHome, Workspace: workspace, WorkDir: "/", Sandbox: config.SandboxConfig{Network: config.SandboxNetworkConfig{Mode: config.SandboxNetworkAllowAll}}}
	backend, err := NewSharedBackend(cfg)
	if err != nil {
		t.Fatalf("NewSharedBackend: %v", err)
	}
	defer func() { _ = backend.Close() }()
	if err := backend.Start(ctx, cfg); err != nil {
		t.Fatalf("backend.Start: %v", err)
	}

	result, err := NewBashAdapter(backend, "").Execute(ctx, map[string]any{"command": fmt.Sprintf("probe %s %d", host, port)})
	if err != nil {
		t.Fatalf("expected allow_all connection to succeed, got %v", err)
	}
	if !strings.Contains(result, "connected") {
		t.Fatalf("expected connected result, got %q", result)
	}
	if hits.Load() == 0 {
		t.Fatal("expected at least one network hit in allow_all mode")
	}
}

func TestNetworkPolicy_WhitelistModePermitsOnlyListedDestinations(t *testing.T) {
	skipIfWindowsNetwork(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	annaHome := t.TempDir()
	workspace := t.TempDir()
	writeNetworkMockBoxsh(t, annaHome)
	host, port, hits, cleanup := startTCPProbeServer(t)
	defer cleanup()

	cfg := BackendConfig{AnnaHome: annaHome, Workspace: workspace, WorkDir: "/", Sandbox: config.SandboxConfig{Network: config.SandboxNetworkConfig{Mode: config.SandboxNetworkWhitelist, Allowlist: []string{host}}}}
	backend, err := NewSharedBackend(cfg)
	if err != nil {
		t.Fatalf("NewSharedBackend: %v", err)
	}
	defer func() { _ = backend.Close() }()
	if err := backend.Start(ctx, cfg); err != nil {
		t.Fatalf("backend.Start: %v", err)
	}

	if _, err := NewBashAdapter(backend, "").Execute(ctx, map[string]any{"command": fmt.Sprintf("probe %s %d", host, port)}); err != nil {
		t.Fatalf("expected whitelisted host connection to succeed, got %v", err)
	}
	allowedHits := hits.Load()
	if allowedHits == 0 {
		t.Fatal("expected whitelisted host to receive a connection")
	}

	_, err = NewBashAdapter(backend, "").Execute(ctx, map[string]any{"command": fmt.Sprintf("probe %s %d", "203.0.113.10", port)})
	if err == nil {
		t.Fatal("expected non-whitelisted host to be blocked")
	}
	if hits.Load() != allowedHits {
		t.Fatalf("expected blocked host not to reach listener, hits before=%d after=%d", allowedHits, hits.Load())
	}
}
