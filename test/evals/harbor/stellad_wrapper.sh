#!/usr/bin/env bash
# Eval-only OTel wrapper for a per-run private stellad copy.

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
    # Six concurrent trials plus pgx spans can overflow the SDK's 2048-span
    # default. Flush every second and retain the wave without recording tool IO.
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
