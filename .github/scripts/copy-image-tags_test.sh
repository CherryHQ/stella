#!/usr/bin/env bash
set -euo pipefail
script="$(cd "$(dirname "$0")" && pwd)/copy-image-tags.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cat > "$tmp/docker" <<'MOCK'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$CALLS"
if [ "${FAIL_COPY:-0}" = 1 ]; then exit 42; fi
MOCK
chmod +x "$tmp/docker"
export PATH="$tmp:$PATH" CALLS="$tmp/calls"

bash "$script" source.example/stella@sha256:abc mirror.example/stella v1.2.3 latest sha-abc
cat > "$tmp/expected" <<'EXPECTED'
buildx imagetools create -t mirror.example/stella:v1.2.3 source.example/stella@sha256:abc
buildx imagetools create -t mirror.example/stella:latest -t mirror.example/stella:sha-abc mirror.example/stella:v1.2.3
EXPECTED
diff -u "$tmp/expected" "$CALLS"

: > "$CALLS"
if FAIL_COPY=1 bash "$script" source.example/stella@sha256:abc mirror.example/stella v1.2.3 latest; then
  echo 'copy failure was ignored' >&2; exit 1
fi
test "$(wc -l < "$CALLS" | tr -d ' ')" = 1
: > "$CALLS"
bash "$script" source.example/stella@sha256:abc mirror.example/stella v1.2.3-rc.1
test "$(wc -l < "$CALLS" | tr -d ' ')" = 1
if bash "$script" source.example/stella mirror.example/stella; then
  echo 'missing tags were accepted' >&2; exit 1
fi
echo 'Image promotion: one remote copy, local aliases, and failure propagation passed'
