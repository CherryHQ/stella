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
)

func skipIfWindowsNetwork(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Network policy tests require Linux/macOS boxsh")
	}
}

func writeNetworkMockBoxsh(t *testing.T, annaHome string) {
	t.Helper()
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

NETWORK_MODE="allow_all"
declare -a ALLOWLIST=()
while [[ $# -gt 0 ]]; do
	case "$1" in
		--new-net-ns) NETWORK_MODE="disabled"; shift ;;
		--bind) shift 2 ;;
		--sandbox|--rpc) shift ;;
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
		initialize)
			echo "{\"jsonrpc\":\"2.0\",\"result\":{\"serverInfo\":{\"name\":\"boxsh\",\"version\":\"2.0.1\"},\"protocolVersion\":\"2024-11-05\"},\"id\":$id}"
			;;
		tools/call)
			command=$(echo "$line" | sed -n 's/.*"command":"\([^"]*\)".*/\1/p')
			probe=$(printf '%s' "$command" | grep -oE 'probe [^ ]+ [0-9]+' | tail -n1 | sed 's/^probe //')
			if [[ -n "$probe" ]]; then
				host="${probe%% *}"
				port="${probe##* }"
				if ! host_allowed "$host"; then
					echo "{\"jsonrpc\":\"2.0\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"blocked\\nnetwork blocked\"}],\"structuredContent\":{\"stdout\":\"blocked\",\"stderr\":\"network blocked\",\"exit_code\":1},\"isError\":true},\"id\":$id}"
				elif probe_tcp "$host" "$port"; then
					echo "{\"jsonrpc\":\"2.0\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"connected\"}],\"structuredContent\":{\"stdout\":\"connected\",\"stderr\":\"\",\"exit_code\":0}},\"id\":$id}"
				else
					echo "{\"jsonrpc\":\"2.0\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"dial failed\"}],\"structuredContent\":{\"stdout\":\"dial failed\",\"stderr\":\"dial failed\",\"exit_code\":1},\"isError\":true},\"id\":$id}"
				fi
			else
				echo "{\"jsonrpc\":\"2.0\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":$(json_escape "$NETWORK_MODE")}],\"structuredContent\":{\"stdout\":$(json_escape "$NETWORK_MODE"),\"stderr\":\"\",\"exit_code\":0}},\"id\":$id}"
			fi
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

	cfg := BackendConfig{AnnaHome: annaHome, Workspace: workspace, WorkDir: "/", Sandbox: NetworkConfig{Mode: NetworkDisabled}}
	backend, err := NewSharedBackend(cfg)
	if err != nil {
		t.Fatalf("NewSharedBackend: %v", err)
	}
	defer func() { _ = backend.Close() }()
	if err := backend.Start(ctx, cfg); err != nil {
		t.Fatalf("backend.Start: %v", err)
	}

	result, err := testExec(ctx, backend, fmt.Sprintf("probe %s %d", host, port), 0)
	if err != nil {
		t.Fatalf("exec probe: %v", err)
	}
	if result.ExitCode == 0 {
		t.Fatalf("expected disabled mode to block network access, got %#v", result)
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

	cfg := BackendConfig{AnnaHome: annaHome, Workspace: workspace, WorkDir: "/", Sandbox: NetworkConfig{Mode: NetworkAllowAll}}
	backend, err := NewSharedBackend(cfg)
	if err != nil {
		t.Fatalf("NewSharedBackend: %v", err)
	}
	defer func() { _ = backend.Close() }()
	if err := backend.Start(ctx, cfg); err != nil {
		t.Fatalf("backend.Start: %v", err)
	}

	result, err := testExec(ctx, backend, fmt.Sprintf("probe %s %d", host, port), 0)
	if err != nil {
		t.Fatalf("expected allow_all connection to succeed, got %v", err)
	}
	if !strings.Contains(result.Stdout, "connected") {
		t.Fatalf("expected connected result, got %#v", result)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if hits.Load() == 0 {
		t.Fatal("expected at least one network hit in allow_all mode")
	}
}

func TestNetworkPolicy_WhitelistModeUnsupported(t *testing.T) {
	skipIfWindowsNetwork(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	annaHome := t.TempDir()
	workspace := t.TempDir()
	writeNetworkMockBoxsh(t, annaHome)
	host, _, _, cleanup := startTCPProbeServer(t)
	defer cleanup()

	cfg := BackendConfig{AnnaHome: annaHome, Workspace: workspace, WorkDir: "/", Sandbox: NetworkConfig{Mode: NetworkWhitelist, Allowlist: []string{host}}}
	backend, err := NewSharedBackend(cfg)
	if err != nil {
		t.Fatalf("NewSharedBackend: %v", err)
	}
	defer func() { _ = backend.Close() }()
	if err := backend.Start(ctx, cfg); err == nil || !strings.Contains(err.Error(), "whitelist network mode is not supported") {
		t.Fatalf("expected whitelist unsupported error, got %v", err)
	}
}
