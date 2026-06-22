#!/bin/sh
if ! command -v mise >/dev/null 2>&1; then
  echo "[post-checkout] mise not found. Install it with:"
  echo "  curl https://mise.run | sh"
  exit 0
fi

mise trust
