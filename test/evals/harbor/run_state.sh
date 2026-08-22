#!/usr/bin/env bash
# Lifecycle helpers for discoverable per-run eval state.

prune_stale_run_states() {
  local root=$1 max_age=${2:-86400} dir owner_file owner
  [ -d "$root" ] || return 0
  for dir in "$root"/*; do
    [ -d "$dir" ] || continue
    owner_file=$dir/owner.pid
    # Missing/partial ownership metadata may be a creator between mkdir and
    # writing its PID. Preserve it rather than racing that creator.
    [ -f "$owner_file" ] || continue
    owner=$(cat "$owner_file" 2>/dev/null || true)
    case $owner in *[!0-9]*|'') continue ;; esac
    # Process liveness is the safety gate. Age only decides when dead residue
    # becomes eligible; a live run is never removed, however old it is.
    kill -0 "$owner" 2>/dev/null && continue
    python3 - "$owner_file" "$max_age" <<'PY' || continue
import os, sys, time
age = time.time() - os.stat(sys.argv[1]).st_mtime
raise SystemExit(0 if age > int(sys.argv[2]) else 1)
PY
    rm -rf -- "$dir"
  done
}
