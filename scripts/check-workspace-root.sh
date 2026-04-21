#!/usr/bin/env bash
# check-workspace-root.sh
#
# Bans .WorkspaceRoot() method call-sites inside runner, sandbox, and plugin-tools
# code. WorkspaceRoot is a FilesystemPolicy field accessed during session setup; it
# must NOT become a Session method that tools call directly.  Runner-side file I/O
# must use session.ResolvePath(path) + os.* instead.
#
# Allowlist: scripts/workspace-root-allowlist.txt
#   Each line is a <file>:<line-number> or a plain <file> that may contain any
#   number of WorkspaceRoot() occurrences.  Blank lines and lines starting with
#   '#' are ignored.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ALLOWLIST="$REPO_ROOT/scripts/workspace-root-allowlist.txt"

DIRS=(
  "internal/agent/runner"
  "internal/sandbox"
  "plugins/tools"
)

# Collect all matches of WorkspaceRoot() in the target dirs.
MATCHES=()
for dir in "${DIRS[@]}"; do
  while IFS= read -r line; do
    MATCHES+=("$line")
  done < <(grep -rn --include="*.go" '\.WorkspaceRoot()' "$REPO_ROOT/$dir" 2>/dev/null || true)
done

if [ ${#MATCHES[@]} -eq 0 ]; then
  echo "check-workspace-root: OK (no WorkspaceRoot() call-sites found)"
  exit 0
fi

# Load allowlist entries (skip blank lines and comments).
declare -A ALLOWED_FILES
declare -A ALLOWED_LINES
if [ -f "$ALLOWLIST" ]; then
  while IFS= read -r entry; do
    [[ -z "$entry" || "$entry" == \#* ]] && continue
    if [[ "$entry" == *:* ]]; then
      file="${entry%%:*}"
      lineno="${entry##*:}"
      ALLOWED_LINES["$file:$lineno"]=1
    else
      ALLOWED_FILES["$entry"]=1
    fi
  done < "$ALLOWLIST"
fi

VIOLATIONS=()
for match in "${MATCHES[@]}"; do
  # match format: /abs/path/to/file.go:42:    ...code...
  rel="${match#$REPO_ROOT/}"
  file="${rel%%:*}"
  rest="${rel#*:}"
  lineno="${rest%%:*}"

  # Check whole-file allowlist.
  if [[ -n "${ALLOWED_FILES[$file]+x}" ]]; then
    continue
  fi
  # Check per-line allowlist.
  if [[ -n "${ALLOWED_LINES[$file:$lineno]+x}" ]]; then
    continue
  fi

  VIOLATIONS+=("$rel")
done

if [ ${#VIOLATIONS[@]} -eq 0 ]; then
  echo "check-workspace-root: OK"
  exit 0
fi

echo "check-workspace-root: FAIL — banned WorkspaceRoot() call-sites found:" >&2
for v in "${VIOLATIONS[@]}"; do
  echo "  $v" >&2
done
echo "" >&2
echo "Runner-side file I/O must use session.ResolvePath(path) + os.* instead." >&2
echo "Add a 'file:line' entry to scripts/workspace-root-allowlist.txt for" >&2
echo "legitimate exceptions (session-setup logging, test fakes)." >&2
exit 1
