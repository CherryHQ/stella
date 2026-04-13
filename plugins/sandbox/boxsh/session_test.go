package boxsh

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/vaayne/anna/plugins/sandbox/boxsh/boxshclient"
)

func TestBoxshSessionHostFilesystemOperationsUseSandboxView(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("boxsh integration tests require Linux/macOS")
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
	writeSessionMockBoxsh(t, annaHome)
	t.Setenv("ANNA_HOME", annaHome)

	session, err := NewFactory().CreateSession(ctx, Policy{
		Backend:    "boxsh",
		Filesystem: FilesystemPolicy{WorkspaceRoot: workspace, WorkingDir: "/docs"},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	if !session.Alive() {
		t.Fatal("session should be alive after CreateSession")
	}
	if session.Policy().Filesystem.WorkingDir != "/docs" {
		t.Fatalf("Policy WorkingDir = %q, want /docs", session.Policy().Filesystem.WorkingDir)
	}

	host := session.Host()
	if host.WorkingDir() != "/docs" {
		t.Fatalf("WorkingDir = %q, want /docs", host.WorkingDir())
	}

	readResult, err := host.ReadFile(ctx, "source.txt", 2, 3)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(readResult.Content) != "cde" {
		t.Fatalf("ReadFile byte slice = %q, want cde", readResult.Content)
	}

	stat, err := host.Stat(ctx, "source.txt")
	if err != nil {
		t.Fatalf("Stat source file: %v", err)
	}
	if !stat.Exists || stat.IsDir || stat.Size != int64(len("abcdef\n")) || stat.Mode != 0o644 {
		t.Fatalf("Stat source file = %+v", stat)
	}

	entries, err := host.ListDir(ctx, ".")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if !boxshDirEntriesContain(entries, "source.txt") {
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

	tempFile, err := host.CreateTemp(ctx, "", "host-*.tmp")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if _, err := tempFile.Write([]byte("temp content")); err != nil {
		t.Fatalf("Write temp file: %v", err)
	}
	stat, err = host.Stat(ctx, tempFile.Path())
	if err != nil {
		t.Fatalf("Stat temp file: %v", err)
	}
	if !stat.Exists || stat.Size != int64(len("temp content")) {
		t.Fatalf("Stat temp file = %+v", stat)
	}
	if err := tempFile.Close(); err != nil {
		t.Fatalf("Close temp file: %v", err)
	}
}

func boxshDirEntriesContain(entries []DirEntry, name string) bool {
	for _, entry := range entries {
		if entry.Name == name {
			return true
		}
	}
	return false
}

func writeSessionMockBoxsh(t *testing.T, annaHome string) {
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
	if [[ "$command" =~ ^dir=\'([^\']*)\'\;[[:space:]]mkdir[[:space:]]-p[[:space:]]\"\$dir\"\;[[:space:]]mktemp[[:space:]]\'([^\']*)\' ]]; then
		dir="${BASH_REMATCH[1]}"
		template="${BASH_REMATCH[2]}"
		mkdir -p "$dir"
		mktemp "$template"
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
				if [[ -z "$limit" || "$limit" == "0" ]]; then limit=100000; fi
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
			fi
			;;
	esac
done
`

	if err := os.WriteFile(boxshPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestBoxshSessionDoneClosesWhenBackendDies(t *testing.T) {
	session := &boxshSession{
		backend: &boxshclient.SharedBackend{}, // Alive() == false because no client is attached
		done:    make(chan struct{}),
	}

	go session.watchBackend()

	select {
	case <-session.Done():
		// expected
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Done() should close when backend is no longer alive")
	}
}

func TestBoxshHostEnsureWritableBlocksReadOnlySubdirUnderWorkspaceRoot(t *testing.T) {
	host := &boxshHost{session: &boxshSession{policy: Policy{Filesystem: FilesystemPolicy{
		WorkspaceRoot: "/repo",
		WorkingDir:    "/repo",
		ReadOnlyPaths: []string{"/repo/docs"},
	}}}}

	err := host.ensureWritable("/repo/docs/file.md")
	if err == nil {
		t.Fatal("expected readonly nested path to be rejected")
	}
	if !strings.Contains(err.Error(), "fail-closed") && !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("expected readonly/fail-closed error, got %v", err)
	}
}
