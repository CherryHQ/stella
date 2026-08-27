#!/usr/bin/env bash
# Lifecycle helpers for discoverable per-run eval state.

claim_run_state() {
  local base_job=$1 runs_root=$2 max_attempts=${3:-100} owner=${4:-$$}
  local attempt suffix candidate_job candidate_state
  for ((attempt = 0; attempt < max_attempts; attempt++)); do
    suffix=""
    [ "$attempt" -eq 0 ] || suffix="-$owner-$attempt"
    candidate_job=$base_job$suffix
    candidate_state=$runs_root/$(basename "$candidate_job")
    # Completed jobs reserve their output name even after normal run-state
    # cleanup. The active owner is claimed only by atomic mkdir without -p.
    [ ! -e "$candidate_job" ] && [ ! -e "$candidate_job.manifest.json" ] || continue
    if mkdir "$candidate_state" 2>/dev/null; then
      # Recheck after the claim. Every current loop must own this state before
      # creating the job, so the claim serializes same-name creators.
      if [ -e "$candidate_job" ] || [ -e "$candidate_job.manifest.json" ]; then
        rmdir "$candidate_state" 2>/dev/null || true
        continue
      fi
      if ! printf '%s\n' "$owner" >"$candidate_state/owner.pid"; then
        rmdir "$candidate_state" 2>/dev/null || true
        return 1
      fi
      # shellcheck disable=SC2034  # read by loop.sh after this returns
      CLAIMED_JOB=$candidate_job
      # shellcheck disable=SC2034  # read by loop.sh after this returns
      CLAIMED_RUN_STATE=$candidate_state
      return 0
    fi
  done
  echo "eval:loop: could not claim a unique run state after $max_attempts attempts for $base_job" >&2
  return 1
}

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
