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

func TestBoxshHostMediatesCOWFilesystemView(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(workspace, "docs", "source.txt"), []byte("abcdef\n"), 0o644); err != nil {
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

	host := session.Host()
	readResult, err := host.ReadFile(ctx, "source.txt", 2, 3)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(readResult.Content) != "cde" {
		t.Fatalf("ReadFile byte slice = %q, want %q", readResult.Content, "cde")
	}

	stat, err := host.Stat(ctx, "source.txt")
	if err != nil {
		t.Fatalf("Stat source file: %v", err)
	}
	if !stat.Exists || stat.IsDir || stat.Size != int64(len("abcdef\n")) {
		t.Fatalf("Stat source file = %+v", stat)
	}

	entries, err := host.ListDir(ctx, ".")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if !dirEntriesContain(entries, "source.txt") {
		t.Fatalf("ListDir should include source-backed file, got %+v", entries)
	}

	if err := host.MkdirAll(ctx, "nested", 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if _, err := host.WriteFile(ctx, "nested/file.txt", []byte("overlay")); err != nil {
		t.Fatalf("WriteFile nested: %v", err)
	}
	if err := host.Rename(ctx, "nested/file.txt", "renamed.txt"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	stat, err = host.Stat(ctx, "renamed.txt")
	if err != nil {
		t.Fatalf("Stat renamed file: %v", err)
	}
	if !stat.Exists || stat.Size != int64(len("overlay")) {
		t.Fatalf("Stat renamed file = %+v", stat)
	}
	if err := host.Remove(ctx, "renamed.txt", false); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	stat, err = host.Stat(ctx, "renamed.txt")
	if err != nil {
		t.Fatalf("Stat removed file: %v", err)
	}
	if stat.Exists {
		t.Fatalf("removed file should not exist, got %+v", stat)
	}
}

func dirEntriesContain(entries []DirEntry, name string) bool {
	for _, entry := range entries {
		if entry.Name == name {
			return true
		}
	}
	return false
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

json_arg() {
	printf '%s' "$1" | python3 -c 'import json,sys; req=json.loads(sys.stdin.read()); args=req.get("params",{}).get("arguments",{}); print(args.get(sys.argv[1],""))' "$2"
}

json_edit_arg() {
	printf '%s' "$1" | python3 -c 'import json,sys; req=json.loads(sys.stdin.read()); edits=req.get("params",{}).get("arguments",{}).get("edits",[]); print(edits[0].get(sys.argv[1],"") if edits else "")' "$2"
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

overlay_exists() {
	local requested="$1"
	local rel
	rel=$(resolve_overlay_path "$requested")
	[[ -e "$DST/$rel" || -e "$SRC/$rel" ]]
}

overlay_is_dir() {
	local requested="$1"
	local rel
	rel=$(resolve_overlay_path "$requested")
	[[ -d "$DST/$rel" || -d "$SRC/$rel" ]]
}

overlay_size() {
	local requested="$1"
	local rel
	rel=$(resolve_overlay_path "$requested")
	if [[ -e "$DST/$rel" ]]; then
		stat -c %s "$DST/$rel" 2>/dev/null || stat -f %z "$DST/$rel"
	elif [[ -e "$SRC/$rel" ]]; then
		stat -c %s "$SRC/$rel" 2>/dev/null || stat -f %z "$SRC/$rel"
	else
		printf '0'
	fi
}

overlay_list_dir() {
	local requested="$1"
	local rel
	rel=$(resolve_overlay_path "$requested")
	local seen=""
	for base in "$SRC/$rel" "$DST/$rel"; do
		[[ -d "$base" ]] || continue
		for x in "$base"/* "$base"/.[!.]* "$base"/..?*; do
			[[ -e "$x" ]] || continue
			local name="${x##*/}"
			[[ "$seen" == *"|$name|"* ]] && continue
			seen="$seen|$name|"
			local isdir=0
			[[ -d "$x" ]] && isdir=1
			local size
			size=$(stat -c %s "$x" 2>/dev/null || stat -f %z "$x")
			printf '%s\t%s\t%s\n' "$name" "$isdir" "$size"
		done
	done
}

handle_bash_command() {
	local command="$1"
	local p=""
	if [[ "$command" =~ cat[[:space:]]+(/[^[:space:]]+) ]]; then
		read_overlay_file "${BASH_REMATCH[1]}"
		return 0
	fi
	if [[ "$command" =~ ^p=\'([^\']*)\' ]]; then
		p="${BASH_REMATCH[1]}"
	fi
	if [[ -n "$p" && "$command" == *"printf '0\\t0\\t0\\t0\\t0\\n'"* ]]; then
		if overlay_exists "$p"; then
			local isdir=0
			overlay_is_dir "$p" && isdir=1
			printf '1\t%s\t%s\t644\t0\n' "$isdir" "$(overlay_size "$p")"
		else
			printf '0\t0\t0\t0\t0\n'
		fi
		return 0
	fi
	if [[ -n "$p" && "$command" == *"for x in"* ]]; then
		overlay_list_dir "$p"
		return 0
	fi
	if [[ -n "$p" && "$command" == *"mkdir -p"* ]]; then
		rel=$(resolve_overlay_path "$p")
		mkdir -p "$DST/$rel"
		return 0
	fi
	if [[ -n "$p" && "$command" == *"rm -rf"* ]]; then
		rel=$(resolve_overlay_path "$p")
		rm -rf "$DST/$rel"
		return 0
	fi
	if [[ -n "$p" && "$command" == *"; rm "* ]]; then
		rel=$(resolve_overlay_path "$p")
		rm "$DST/$rel"
		return 0
	fi
	if [[ "$command" =~ ^old=\'([^\']*)\'\;[[:space:]]new=\'([^\']*)\' ]]; then
		old="${BASH_REMATCH[1]}"
		new="${BASH_REMATCH[2]}"
		old_rel=$(resolve_overlay_path "$old")
		new_rel=$(resolve_overlay_path "$new")
		mv "$DST/$old_rel" "$DST/$new_rel"
		return 0
	fi
	printf '%s' "$command"
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
					command=$(json_arg "$line" command)
					stdout=$(handle_bash_command "$command")
					echo "{\"jsonrpc\":\"2.0\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":$(json_escape "$stdout")}],\"structuredContent\":{\"stdout\":$(json_escape "$stdout"),\"stderr\":\"\",\"exit_code\":0}},\"id\":$id}"
				elif [[ "$line" == *'"name":"read"'* ]]; then
					path=$(json_arg "$line" path)
					content=$(read_overlay_file "$path")
					offset=$(json_arg "$line" offset)
					limit=$(json_arg "$line" limit)
					if [[ -z "$offset" ]]; then offset=1; fi
					if [[ -z "$limit" || "$limit" == "0" ]]; then limit=3; fi
					json=$(printf '%s' "$content" | python3 -c 'import json,sys; content=sys.stdin.read(); offset=int(sys.argv[1]); limit=int(sys.argv[2]); ident=int(sys.argv[3]); lines=content.splitlines(True); start=max(offset-1,0); selected=lines[start:start+limit]; truncated=start+limit < len(lines); print(json.dumps({"jsonrpc":"2.0","result":{"content":[{"type":"text","text":"".join(selected)}],"structuredContent":{"truncation":{"line_count":len(lines),"truncated":truncated}}},"id":ident}))' "$offset" "$limit" "$id")
					echo "$json"
				elif [[ "$line" == *'"name":"write"'* ]]; then
					path=$(json_arg "$line" path)
					content=$(json_arg "$line" content)
					rel=$(resolve_overlay_path "$path")
					mkdir -p "$(dirname "$DST/$rel")"
					printf '%s' "$content" > "$DST/$rel"
					bytes=$(printf '%s' "$content" | wc -c | tr -d ' ')
					echo "{\"jsonrpc\":\"2.0\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"written $bytes bytes\"}]},\"id\":$id}"
				elif [[ "$line" == *'"name":"edit"'* ]]; then
					path=$(json_arg "$line" path)
					old=$(json_edit_arg "$line" oldText)
					new=$(json_edit_arg "$line" newText)
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
