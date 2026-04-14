package boxshclient

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func skipIfWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("COW integration tests require Linux/macOS boxsh")
	}
}

func writeStatefulMockBoxsh(t *testing.T, annaHome string) {
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

SRC=""
DST=""
NETMODE="allow_all"
while [[ $# -gt 0 ]]; do
	case "$1" in
		--bind)
			binding="$2"
			if [[ "$binding" == cow:* ]]; then
				payload="${binding#cow:}"
				SRC="${payload%%:*}"
				DST="${payload#*:}"
			fi
			shift 2 ;;
		--new-net-ns) NETMODE="disabled"; shift ;;
		--sandbox|--rpc) shift ;;
		*) shift ;;
	esac
done

mkdir -p "$DST"

json_escape() {
	printf '%s' "$1" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))'
}

resolve_overlay_path() {
	local requested="$1"
	local rel
	if [[ "$requested" == "$DST" ]]; then
		rel=""
	elif [[ "$requested" == "$DST"/* ]]; then
		rel="${requested#"$DST"/}"
	elif [[ "$requested" == "$SRC" ]]; then
		rel=""
	elif [[ "$requested" == "$SRC"/* ]]; then
		rel="${requested#"$SRC"/}"
	elif [[ "$requested" = /* ]]; then
		rel="${requested#/}"
	else
		rel="$requested"
	fi
	printf '%s' "$rel"
}

read_overlay_file() {
	local requested="$1"
	local rel
	rel=$(resolve_overlay_path "$requested")
	if [[ -f "$DST/$rel" ]]; then
		cat "$DST/$rel"
	elif [[ -f "$SRC/$rel" ]]; then
		cat "$SRC/$rel"
	fi
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
			if [[ "$line" == *'"name":"bash"'* ]]; then
				command=$(echo "$line" | sed -n 's/.*"command":"\([^"]*\)".*/\1/p')
				stdout=""
				if [[ "$command" =~ cat[[:space:]]+(/[^[:space:]]+) ]]; then
					path="${BASH_REMATCH[1]}"
					stdout=$(read_overlay_file "$path")
				elif [[ "$command" =~ test[[:space:]]-f[[:space:]]+(/[^[:space:]]+) ]]; then
					path="${BASH_REMATCH[1]}"
					rel=$(resolve_overlay_path "$path")
					if [[ -f "$DST/$rel" || -f "$SRC/$rel" ]]; then stdout="exists"; fi
				else
					stdout="$command"
				fi
				echo "{\"jsonrpc\":\"2.0\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":$(json_escape "$stdout")}],\"structuredContent\":{\"stdout\":$(json_escape "$stdout"),\"stderr\":\"\",\"exit_code\":0}},\"id\":$id}"
			elif [[ "$line" == *'"name":"read"'* ]]; then
				path=$(echo "$line" | sed -n 's/.*"path":"\([^"]*\)".*/\1/p')
				content=$(read_overlay_file "$path")
				lines=0
				if [[ -n "$content" ]]; then lines=$(printf '%s' "$content" | awk 'END{print NR}'); fi
				echo "{\"jsonrpc\":\"2.0\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":$(json_escape "$content")}],\"structuredContent\":{\"truncation\":{\"line_count\":$lines,\"truncated\":false}}},\"id\":$id}"
			elif [[ "$line" == *'"name":"write"'* ]]; then
				path=$(echo "$line" | sed -n 's/.*"path":"\([^"]*\)".*/\1/p')
				content=$(echo "$line" | sed -n 's/.*"content":"\([^"]*\)".*/\1/p')
				rel=$(resolve_overlay_path "$path")
				mkdir -p "$(dirname "$DST/$rel")"
				printf '%s' "$content" > "$DST/$rel"
				bytes=$(printf '%s' "$content" | wc -c | tr -d ' ')
				echo "{\"jsonrpc\":\"2.0\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"written $bytes bytes\"}]},\"id\":$id}"
			else
				path=$(echo "$line" | sed -n 's/.*"path":"\([^"]*\)".*/\1/p')
				old=$(echo "$line" | sed -n 's/.*"oldText":"\([^"]*\)".*/\1/p')
				new=$(echo "$line" | sed -n 's/.*"newText":"\([^"]*\)".*/\1/p')
				rel=$(resolve_overlay_path "$path")
				content=$(read_overlay_file "$path")
				updated="${content/$old/$new}"
				mkdir -p "$(dirname "$DST/$rel")"
				printf '%s' "$updated" > "$DST/$rel"
				repl=0
				first=0
				if [[ "$content" != "$updated" ]]; then repl=1; first=1; fi
				echo "{\"jsonrpc\":\"2.0\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"OK\"}],\"structuredContent\":{\"diff\":\"diff\",\"firstChangedLine\":$first}},\"id\":$id}"
			fi
			;;
	esac
done
`
	if err := os.WriteFile(boxshPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestSharedCOWView_AllToolsSeeSameSession(t *testing.T) {
	skipIfWindows(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	annaHome := t.TempDir()
	workspace := t.TempDir()
	src := filepath.Join(workspace, "users", "1", "data")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "notes.txt"), []byte("old value\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	writeStatefulMockBoxsh(t, annaHome)

	cfg := BackendConfig{
		AnnaHome:    annaHome,
		SandboxRoot: src,
		WorkDir:     "/",
		Sandbox:     NetworkConfig{Mode: NetworkDisabled},
	}

	backend, err := NewSharedBackend(cfg)
	if err != nil {
		t.Fatalf("NewSharedBackend: %v", err)
	}
	defer func() { _ = backend.Close() }()
	if err := backend.Start(ctx, cfg); err != nil {
		t.Fatalf("backend.Start: %v", err)
	}

	result, err := testRead(ctx, backend, "/notes.txt", 0, 0)
	if err != nil {
		t.Fatalf("initial read: %v", err)
	}
	if !strings.Contains(result.Content, "old value") {
		t.Fatalf("expected source content, got %q", result.Content)
	}

	if _, err := testWrite(ctx, backend, "/notes.txt", "written value\n"); err != nil {
		t.Fatalf("write: %v", err)
	}

	result, err = testRead(ctx, backend, "/notes.txt", 0, 0)
	if err != nil {
		t.Fatalf("read after write: %v", err)
	}
	if !strings.Contains(result.Content, "written value") {
		t.Fatalf("expected updated content visible to read, got %q", result.Content)
	}

	bashResult, err := testExec(ctx, backend, "cat /notes.txt", 0)
	if err != nil {
		t.Fatalf("bash cat: %v", err)
	}
	if !strings.Contains(bashResult.Stdout, "written value") {
		t.Fatalf("expected bash to see overlay content, got %#v", bashResult)
	}

	if _, err := testEdit(ctx, backend, "/notes.txt", "written", "edited"); err != nil {
		t.Fatalf("edit: %v", err)
	}

	result, err = testRead(ctx, backend, "/notes.txt", 0, 0)
	if err != nil {
		t.Fatalf("read after edit: %v", err)
	}
	if !strings.Contains(result.Content, "edited value") {
		t.Fatalf("expected edited content visible to read, got %q", result.Content)
	}

	bashResult, err = testExec(ctx, backend, "cat /notes.txt", 0)
	if err != nil {
		t.Fatalf("bash cat after edit: %v", err)
	}
	if !strings.Contains(bashResult.Stdout, "edited value") {
		t.Fatalf("expected bash to see edited overlay content, got %#v", bashResult)
	}

	sourceData, err := os.ReadFile(filepath.Join(src, "notes.txt"))
	if err != nil {
		t.Fatalf("ReadFile source: %v", err)
	}
	if string(sourceData) != "old value\n" {
		t.Fatalf("source workspace should remain unchanged, got %q", string(sourceData))
	}
}

func TestSharedCOWView_ClientAndSessionLifecycle(t *testing.T) {
	skipIfWindows(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	annaHome := t.TempDir()
	workspace := t.TempDir()
	writeStatefulMockBoxsh(t, annaHome)

	cfg := BackendConfig{
		AnnaHome:    annaHome,
		SandboxRoot: workspace,
		Sandbox:     NetworkConfig{Mode: NetworkDisabled},
	}

	backend, err := NewSharedBackend(cfg)
	if err != nil {
		t.Fatalf("NewSharedBackend: %v", err)
	}
	if err := backend.Start(ctx, cfg); err != nil {
		t.Fatalf("backend.Start: %v", err)
	}

	client1 := backend.Client()
	client2 := backend.Client()
	if client1 == nil || client2 == nil || client1 != client2 {
		t.Fatal("expected one shared client instance")
	}

	sessionDir := backend.SessionDir()
	if sessionDir == "" {
		t.Fatal("expected session dir after Start")
	}
	if _, err := os.Stat(sessionDir); err != nil {
		t.Fatalf("Stat session dir: %v", err)
	}

	if err := backend.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Fatalf("expected session dir cleanup, stat err = %v", err)
	}
}

func TestSharedCOWView_MultipleBackendsUseDistinctUpperdirs(t *testing.T) {
	skipIfWindows(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	annaHome := t.TempDir()
	workspace := t.TempDir()
	writeStatefulMockBoxsh(t, annaHome)

	cfg := BackendConfig{
		AnnaHome:    annaHome,
		SandboxRoot: workspace,
		Sandbox:     NetworkConfig{Mode: NetworkDisabled},
	}

	backend1, err := NewSharedBackend(cfg)
	if err != nil {
		t.Fatalf("NewSharedBackend 1: %v", err)
	}
	defer func() { _ = backend1.Close() }()
	backend2, err := NewSharedBackend(cfg)
	if err != nil {
		t.Fatalf("NewSharedBackend 2: %v", err)
	}
	defer func() { _ = backend2.Close() }()

	if err := backend1.Start(ctx, cfg); err != nil {
		t.Fatalf("backend1.Start: %v", err)
	}
	if err := backend2.Start(ctx, cfg); err != nil {
		t.Fatalf("backend2.Start: %v", err)
	}

	if backend1.SessionDir() == "" || backend2.SessionDir() == "" {
		t.Fatal("expected session dirs for both backends")
	}
	if backend1.SessionDir() == backend2.SessionDir() {
		t.Fatal("expected distinct session dirs for separate backends")
	}
}
