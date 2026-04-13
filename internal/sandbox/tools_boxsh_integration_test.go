package sandbox

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

func TestNewCoreToolsBoxshUsesWorkingDirAndSharedState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("boxsh integration tests require Linux/macOS")
	}
	factory := GlobalRegistry().Get("boxsh")
	if factory == nil {
		t.Skip("boxsh factory not available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	annaHome := t.TempDir()
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "docs"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "docs", "notes.txt"), []byte("old value\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	writeStatefulMockBoxshForSandboxTools(t, annaHome)

	prevAnnaHome, hadAnnaHome := os.LookupEnv("ANNA_HOME")
	if err := os.Setenv("ANNA_HOME", annaHome); err != nil {
		t.Fatalf("Setenv: %v", err)
	}
	config.ResetAnnaHome()
	defer func() {
		if hadAnnaHome {
			_ = os.Setenv("ANNA_HOME", prevAnnaHome)
		} else {
			_ = os.Unsetenv("ANNA_HOME")
		}
		config.ResetAnnaHome()
	}()

	session, err := factory.CreateSession(ctx, Policy{
		Backend: "boxsh",
		Filesystem: FilesystemPolicy{
			WorkspaceRoot: workspace,
			WorkingDir:    "/docs",
		},
		Process: ProcessPolicy{InheritEnv: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	toolByName := mapToolsByName(NewCoreTools(session.Host(), ""))

	readResult, err := toolByName["read"].Execute(ctx, map[string]any{"file_path": "notes.txt"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(readResult, "old value") {
		t.Fatalf("unexpected read result: %q", readResult)
	}

	if _, err := toolByName["write"].Execute(ctx, map[string]any{"file_path": "notes.txt", "content": "written value\n"}); err != nil {
		t.Fatalf("write: %v", err)
	}

	bashResult, err := toolByName["bash"].Execute(ctx, map[string]any{"command": "cat /docs/notes.txt"})
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	if !strings.Contains(bashResult, "written value") {
		t.Fatalf("unexpected bash result: %q", bashResult)
	}

	if _, err := toolByName["edit"].Execute(ctx, map[string]any{"file_path": "notes.txt", "old_string": "written", "new_string": "edited"}); err != nil {
		t.Fatalf("edit: %v", err)
	}

	readResult, err = toolByName["read"].Execute(ctx, map[string]any{"file_path": "notes.txt"})
	if err != nil {
		t.Fatalf("read after edit: %v", err)
	}
	if !strings.Contains(readResult, "edited value") {
		t.Fatalf("unexpected read after edit: %q", readResult)
	}

	sourceData, err := os.ReadFile(filepath.Join(workspace, "docs", "notes.txt"))
	if err != nil {
		t.Fatalf("ReadFile source: %v", err)
	}
	if string(sourceData) != "old value\n" {
		t.Fatalf("expected source workspace to remain unchanged, got %q", string(sourceData))
	}
}

func TestNewCoreToolsBoxshReadPaginationAndEditPreflightAcrossPages(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("boxsh integration tests require Linux/macOS")
	}
	factory := GlobalRegistry().Get("boxsh")
	if factory == nil {
		t.Skip("boxsh factory not available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	annaHome := t.TempDir()
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "docs"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "docs", "long.txt"), []byte("needle line1\nline2\nline3\nline4\nneedle line5\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	writeStatefulMockBoxshForSandboxTools(t, annaHome)

	prevAnnaHome, hadAnnaHome := os.LookupEnv("ANNA_HOME")
	if err := os.Setenv("ANNA_HOME", annaHome); err != nil {
		t.Fatalf("Setenv: %v", err)
	}
	config.ResetAnnaHome()
	defer func() {
		if hadAnnaHome {
			_ = os.Setenv("ANNA_HOME", prevAnnaHome)
		} else {
			_ = os.Unsetenv("ANNA_HOME")
		}
		config.ResetAnnaHome()
	}()

	session, err := factory.CreateSession(ctx, Policy{
		Backend:    "boxsh",
		Filesystem: FilesystemPolicy{WorkspaceRoot: workspace, WorkingDir: "/docs"},
		Process:    ProcessPolicy{InheritEnv: true},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	toolByName := mapToolsByName(NewCoreTools(session.Host(), ""))

	readResult, err := toolByName["read"].Execute(ctx, map[string]any{"file_path": "long.txt"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(readResult, "offset=4") {
		t.Fatalf("expected truncated boxsh read to continue at line 4, got %q", readResult)
	}

	_, err = toolByName["edit"].Execute(ctx, map[string]any{"file_path": "long.txt", "old_string": "needle", "new_string": "updated"})
	if err == nil || !strings.Contains(err.Error(), "must be unique") {
		t.Fatalf("expected edit preflight to detect duplicate match across pages, got %v", err)
	}
}

func writeStatefulMockBoxshForSandboxTools(t *testing.T, annaHome string) {
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

SRC=""
DST=""
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
		--sandbox|--rpc|--new-net-ns) shift ;;
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
				else
					stdout="$command"
				fi
				echo "{\"jsonrpc\":\"2.0\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":$(json_escape "$stdout")}],\"structuredContent\":{\"stdout\":$(json_escape "$stdout"),\"stderr\":\"\",\"exit_code\":0}},\"id\":$id}"
			elif [[ "$line" == *'"name":"read"'* ]]; then
				path=$(echo "$line" | sed -n 's/.*"path":"\([^"]*\)".*/\1/p')
				content=$(read_overlay_file "$path")
				offset=$(echo "$line" | sed -n 's/.*"offset":\([0-9]*\).*/\1/p')
				limit=$(echo "$line" | sed -n 's/.*"limit":\([0-9]*\).*/\1/p')
				if [[ -z "$offset" ]]; then offset=1; fi
				if [[ -z "$limit" || "$limit" == "0" ]]; then limit=3; fi
				json=$(printf '%s' "$content" | python3 -c 'import json,sys; content=sys.stdin.read(); offset=int(sys.argv[1]); limit=int(sys.argv[2]); ident=int(sys.argv[3]); lines=content.splitlines(True); start=max(offset-1,0); selected=lines[start:start+limit]; truncated=start+limit < len(lines); print(json.dumps({"jsonrpc":"2.0","result":{"content":[{"type":"text","text":"".join(selected)}],"structuredContent":{"truncation":{"line_count":len(lines),"truncated":truncated}}},"id":ident}))' "$offset" "$limit" "$id")
				echo "$json"
			elif [[ "$line" == *'"name":"write"'* ]]; then
				path=$(echo "$line" | sed -n 's/.*"path":"\([^"]*\)".*/\1/p')
				content=$(echo "$line" | sed -n 's/.*"content":"\([^"]*\)".*/\1/p')
				rel=$(resolve_overlay_path "$path")
				mkdir -p "$(dirname "$DST/$rel")"
				printf '%s' "$content" > "$DST/$rel"
				bytes=$(printf '%s' "$content" | wc -c | tr -d ' ')
				echo "{\"jsonrpc\":\"2.0\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"written $bytes bytes\"}]},\"id\":$id}"
			elif [[ "$line" == *'"name":"edit"'* ]]; then
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
