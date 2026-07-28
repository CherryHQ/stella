#!/usr/bin/env bash
# Exercise the exact Linux amd64 release candidate through its real systemd
# adapter. This script must run only on an ephemeral release runner because the
# production CLI intentionally owns the fixed stella.service and stella user.
set -euo pipefail

candidate="${1:?usage: systemd-smoke.sh /absolute/path/to/stellad}"
if [[ "$candidate" != /* || ! -x "$candidate" ]]; then
  echo "candidate must be an absolute executable path" >&2
  exit 1
fi
if [[ "$(uname -s)" != "Linux" || "$(uname -m)" != "x86_64" ]]; then
  echo "systemd release smoke requires native Linux amd64" >&2
  exit 1
fi
if [[ "$(ps -p 1 -o comm=)" != "systemd" ]]; then
  echo "systemd is not PID 1 on this runner" >&2
  exit 1
fi
run_hash="$(printf '%s' "${STELLA_RELEASE_RUN_ID:?set STELLA_RELEASE_RUN_ID}" | sha256sum | cut -c1-8)"
installed="/usr/local/bin/stellad-release-${run_hash}"
if id stella >/dev/null 2>&1 ||
  getent group stella >/dev/null 2>&1 ||
  sudo test -e /etc/systemd/system/stella.service ||
  sudo test -e "$installed" ||
  sudo test -e /var/lib/stella ||
  sudo test -e /var/cache/stella ||
  sudo test -e /var/log/stella; then
  echo "refusing to replace pre-existing Stella service state" >&2
  exit 1
fi

expected_failure="$(mktemp)"
env_file="$(mktemp)"
cleanup_done=0

cleanup() {
  local status="${1:-0}"
  if [[ "$cleanup_done" -eq 0 ]]; then
    cleanup_done=1
    set +e
    sudo systemctl disable --now stella >/dev/null 2>&1
    sudo rm -f /etc/systemd/system/stella.service
    sudo systemctl daemon-reload >/dev/null 2>&1
    sudo rm -f "$installed"
    if id stella >/dev/null 2>&1; then
      sudo userdel --remove stella >/dev/null 2>&1
    fi
    if getent group stella >/dev/null 2>&1; then
      sudo groupdel stella >/dev/null 2>&1
    fi
    sudo rmdir /var/cache/stella /var/log/stella >/dev/null 2>&1
    rm -f "$expected_failure" "$env_file"
    set -e
  fi
  return "$status"
}

on_exit() {
  local status=$?
  trap - EXIT
  cleanup "$status" || true
  exit "$status"
}
trap on_exit EXIT

sudo install -o root -g root -m 0755 "$candidate" "$installed"
"$installed" version
"$installed" --help >/dev/null
"$installed" service --help >/dev/null
"$installed" postgres --help >/dev/null

# The first install must fail safely because no vault configuration exists. It
# also creates the production service account and directories, allowing the
# test to populate them exactly as an operator would before retrying.
if sudo "$installed" service install >"$expected_failure" 2>&1; then
  echo "service install unexpectedly succeeded without /var/lib/stella/.env" >&2
  exit 1
fi
if ! grep -q '/var/lib/stella/.env is required' "$expected_failure"; then
  sed 's/^/expected-install-failure: /' "$expected_failure" >&2
  echo "service install failed for an unexpected reason" >&2
  exit 1
fi
id stella >/dev/null

# Downloading through the candidate proves that the service user can install
# and later discover the same pinned Stella PG Runtime used by other slices.
sudo -u stella env STELLA_HOME=/var/lib/stella "$installed" postgres download-runtime
vault_key="$("$installed" vault keygen)"
chmod 0600 "$env_file"
printf '%s\n' \
  "STELLA_VAULT_KEY=$vault_key" \
  "STELLA_SANDBOX_BACKEND=none" \
  "STELLA_HTTP_SHUTDOWN_TIMEOUT=5s" \
  "STELLA_RIVER_SOFT_STOP_TIMEOUT=10s" \
  >"$env_file"
unset vault_key
sudo install -o stella -g stella -m 0600 "$env_file" /var/lib/stella/.env

wait_ready() {
  local deadline=$((SECONDS + 180))
  until curl --fail --silent --show-error http://127.0.0.1:25678/readyz >/dev/null; do
    if (( SECONDS >= deadline )); then
      echo "stella.service did not become ready" >&2
      sudo journalctl -u stella --no-pager -n 100 >&2
      return 1
    fi
    sleep 1
  done
}

sudo "$installed" service install
wait_ready
sudo "$installed" service status
sudo "$installed" service logs

sudo "$installed" service restart
wait_ready
sudo "$installed" service stop
sudo systemctl is-active --quiet stella && {
  echo "stella.service remained active after stop" >&2
  exit 1
}
sudo "$installed" service start
wait_ready
sudo "$installed" service uninstall

if sudo test -e /etc/systemd/system/stella.service; then
  echo "stella.service unit remains after uninstall" >&2
  exit 1
fi

cleanup 0
trap - EXIT
if id stella >/dev/null 2>&1 ||
  getent group stella >/dev/null 2>&1 ||
  [[ -e "$installed" ]] ||
  sudo test -e /etc/systemd/system/stella.service ||
  sudo test -e /var/lib/stella ||
  sudo test -e /var/cache/stella ||
  sudo test -e /var/log/stella; then
  echo "systemd release smoke left service resources behind" >&2
  exit 1
fi
echo "systemd candidate lifecycle passed and cleaned up"
