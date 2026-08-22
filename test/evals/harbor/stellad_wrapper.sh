#!/usr/bin/env bash
# Eval-only staging for testbed's intentionally sterile child environment.

# stage_otel_stellad_wrapper replaces $1 only after the real binary has moved
# to $2. Both paths live in dist/bin, so each rename is atomic. The caller
# records $2 before calling this function; restore is then safe even if an
# interrupt lands between the two renames.
stage_otel_stellad_wrapper() {
  local stellad_path=$1 real_path=$2 otlp_endpoint=$3 wrapper_path
  local stellad_dir quoted_real quoted_endpoint
  stellad_dir=$(dirname "$stellad_path")

  [ -x "$stellad_path" ] || {
    echo "eval:loop: missing executable $stellad_path" >&2
    return 1
  }
  [ ! -e "$real_path" ] || {
    echo "eval:loop: private stellad path already exists: $real_path" >&2
    return 1
  }

  printf -v quoted_real '%q' "$real_path"
  printf -v quoted_endpoint '%q' "$otlp_endpoint"
  wrapper_path=$(mktemp "$stellad_dir/.stellad-eval-wrapper.XXXXXX") || return 1
  {
    printf '%s\n' '#!/usr/bin/env bash' '# stella-eval-otel-wrapper'
    printf 'export OTEL_SERVICE_NAME=%s\n' stella-eval
    printf 'export OTEL_EXPORTER_OTLP_ENDPOINT=%s\n' "$quoted_endpoint"
    printf 'export OTEL_EXPORTER_OTLP_PROTOCOL=%s\n' http/protobuf
    printf 'export OTEL_EXPORTER_OTLP_INSECURE=%s\n' true
    # Six concurrent trials plus pgx spans overflow the SDK's 2048-span default.
    # Flush every second and retain the complete wave without recording tool IO.
    printf 'export OTEL_BSP_MAX_QUEUE_SIZE=%s\n' 16384
    printf 'export OTEL_BSP_MAX_EXPORT_BATCH_SIZE=%s\n' 2048
    printf 'export OTEL_BSP_SCHEDULE_DELAY=%s\n' 1000
    printf 'export OTEL_LOGS_EXPORTER=%s\n' none
    printf 'export OTEL_METRICS_EXPORTER=%s\n' none
    printf 'exec %s "$@"\n' "$quoted_real"
  } >"$wrapper_path"
  chmod 700 "$wrapper_path"

  if ! mv "$stellad_path" "$real_path"; then
    rm -f "$wrapper_path"
    return 1
  fi
  if ! mv "$wrapper_path" "$stellad_path"; then
    mv "$real_path" "$stellad_path" || true
    return 1
  fi
}

# restore_stellad_binary replaces the wrapper in one rename. A failed or
# interrupted stage leaves no private binary, making this a safe no-op.
restore_stellad_binary() {
  local stellad_path=$1 real_path=$2
  [ -n "$real_path" ] && [ -e "$real_path" ] || return 0
  mv -f "$real_path" "$stellad_path"
}

# Recover after SIGKILL, host crash, or power loss, where EXIT traps cannot run.
# Refuse ambiguity rather than choosing among multiple private binaries.
recover_stale_stellad_binary() {
  local stellad_path=$1 candidate
  local -a candidates=()
  [ -f "$stellad_path" ] || return 0
  grep -q '^# stella-eval-otel-wrapper$' "$stellad_path" || return 0
  while IFS= read -r candidate; do candidates+=("$candidate"); done < <(
    find "$(dirname "$stellad_path")" -maxdepth 1 -type f -name '.stellad-eval-real-*' -print
  )
  case ${#candidates[@]} in
    0) rm -f "$stellad_path" ;;
    1) mv -f "${candidates[0]}" "$stellad_path" ;;
    *)
      echo "eval:build: multiple stale eval stellad binaries; refusing recovery" >&2
      return 1
      ;;
  esac
}
