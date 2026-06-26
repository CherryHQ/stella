#!/bin/sh
# Cross-compile the CGO spike for darwin+linux from a single macOS host.
# Prereqs: zig (mise), per-target libtokenizers.a under libs/<target>/.
set -e
cd "$(dirname "$0")"
mkdir -p out

echo ">> darwin/arm64 (native clang)"
CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 CGO_LDFLAGS="-L$PWD/libs/darwin-arm64" \
  go build -o out/darwin-arm64 .

echo ">> darwin/amd64 (native clang, cross-arch)"
CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 CGO_LDFLAGS="-L$PWD/libs/darwin-amd64" \
  go build -o out/darwin-amd64 .

echo ">> linux/amd64 (zig cc + libstdc++ shim)"
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 CC="$PWD/cc/zigcc-linux-amd64" \
  CGO_LDFLAGS="-L$PWD/libs/linux-amd64" go build -o out/linux-amd64 .

echo ">> linux/arm64 (zig cc + libstdc++ shim)"
CGO_ENABLED=1 GOOS=linux GOARCH=arm64 CC="$PWD/cc/zigcc-linux-arm64" \
  CGO_LDFLAGS="-L$PWD/libs/linux-arm64" go build -o out/linux-arm64 .

ls -la out/
