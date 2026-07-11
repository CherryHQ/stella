#!/usr/bin/env bash
# Lint and render-assert the Stella Helm chart. All secret material here is fake.
set -euo pipefail

CHART_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/stella" && pwd)"
ERR="$(mktemp)"
OUT="$(mktemp)"
trap 'rm -f "$ERR" "$OUT"' EXIT

rc=0

# Minimal valid values reused by success cases (all fake).
BASE=(
  --set baseURL=https://stella.example.com
  --set secrets.existingSecret=stella-secrets
)

render() { helm template stella "$CHART_DIR" "$@"; }

expect_ok() {
  local name="$1"; shift
  if render "$@" >"$OUT" 2>"$ERR"; then
    echo "ok   - $name"
  else
    echo "FAIL - $name (expected render to succeed)"
    sed 's/^/       /' "$ERR"
    rc=1
  fi
}

expect_fail() {
  local name="$1" keyword="$2"; shift 2
  if render "$@" >"$OUT" 2>"$ERR"; then
    echo "FAIL - $name (expected render to fail, but it succeeded)"
    rc=1
  elif grep -qi -- "$keyword" "$ERR"; then
    echo "ok   - $name (rejected, matched '$keyword')"
  else
    echo "FAIL - $name (failed, but message missing '$keyword')"
    sed 's/^/       /' "$ERR"
    rc=1
  fi
}

# assert the last successful render's output contains / omits a string.
assert_contains() {
  if grep -qF -- "$2" "$OUT"; then echo "ok   - render contains: $1"
  else echo "FAIL - render missing: $1 ($2)"; rc=1; fi
}
assert_absent() {
  if grep -qF -- "$2" "$OUT"; then echo "FAIL - render should omit: $1 ($2)"; rc=1
  else echo "ok   - render omits: $1"; fi
}

echo "== helm lint =="
if helm lint "$CHART_DIR" --set baseURL=https://stella.example.com \
  --set secrets.existingSecret=stella-secrets --set sandbox.backend=local; then
  echo "ok   - helm lint"
else
  echo "FAIL - helm lint"; rc=1
fi

echo
echo "== success cases =="
expect_ok "minimal (sandbox=local)" "${BASE[@]}" --set sandbox.backend=local
# Assertions run against the last render ($OUT), i.e. the minimal-local case.
assert_contains "Recreate strategy"          "type: Recreate"
assert_contains "single replica"             "replicas: 1"
assert_contains "http shutdown timeout 60s"  'value: "60s"'
assert_contains "river soft-stop 120s"       'value: "120s"'
assert_contains "preStop sleep 10"           '"/bin/sleep", "10"'
assert_contains "grace period 200"           "terminationGracePeriodSeconds: 200"
assert_contains "no api token"               "automountServiceAccountToken: false"
assert_contains "runAsNonRoot"               "runAsNonRoot: true"
assert_contains "fsGroupChangePolicy"        "fsGroupChangePolicy: OnRootMismatch"
assert_contains "startup failureThreshold"   "failureThreshold: 60"
assert_contains "mount STELLA_HOME"          "mountPath: /home/stella/.stella"
assert_contains "vault key secretKeyRef"     'key: "STELLA_VAULT_KEY"'
assert_contains "database secretKeyRef"      'key: "STELLA_DATABASE_URL"'
assert_contains "pvc kept on uninstall"      "helm.sh/resource-policy: keep"
assert_contains "image repo"                 "ghcr.io/cherryhq/stella"
assert_contains "explicit bind all"          'value: "0.0.0.0"'
assert_contains "explicit external db"       "STELLA_REQUIRE_EXTERNAL_DB"
assert_contains "readiness probe timeout"    "timeoutSeconds: 3"
assert_contains "seccomp runtime default"    "type: RuntimeDefault"
assert_absent   "no plaintext secret env"    "AGE-SECRET-KEY"
# local backend must not get the restrictive container securityContext that
# would break bubblewrap.
assert_absent   "no privilege drop on local" "allowPrivilegeEscalation"

expect_ok "image digest pins over tag" "${BASE[@]}" --set sandbox.backend=local \
  --set image.digest=sha256:0123456789abcdef
assert_contains "image pinned by digest"     "ghcr.io/cherryhq/stella@sha256:0123456789abcdef"

expect_ok "sandbox=none with confirmation" "${BASE[@]}" \
  --set sandbox.backend=none --set sandbox.allowUnsafeHostExecution=true
assert_contains "none drops escalation"      "allowPrivilegeEscalation: false"
assert_contains "none drops capabilities"    'drop: ["ALL"]'

expect_ok "ephemeral storage with confirmation" "${BASE[@]}" \
  --set sandbox.backend=local --set persistence.enabled=false \
  --set persistence.allowEphemeralDataLoss=true
assert_contains "emptyDir when unpersisted"  "emptyDir: {}"

expect_ok "existingClaim reuses PVC" "${BASE[@]}" --set sandbox.backend=local \
  --set persistence.existingClaim=my-stella-data
assert_absent "no chart-created PVC with existingClaim" "kind: PersistentVolumeClaim"
assert_contains "mounts the existing claim"  "claimName: my-stella-data"

expect_ok "storageClass '-' disables provisioning" "${BASE[@]}" \
  --set sandbox.backend=local --set 'persistence.storageClass=-'
assert_contains "empty storageClassName"     'storageClassName: ""'

expect_ok "seccomp Unconfined override for local" "${BASE[@]}" \
  --set sandbox.backend=local --set sandbox.seccompProfile=Unconfined
assert_contains "seccomp unconfined rendered"  "type: Unconfined"

expect_ok "custom podLabels render" "${BASE[@]}" --set sandbox.backend=local \
  --set 'podLabels.team=platform'
assert_contains "custom pod label"           "team: platform"
assert_contains "selector label kept"        "app.kubernetes.io/name:"

expect_ok "ingress + tls" "${BASE[@]}" --set sandbox.backend=local \
  --set ingress.enabled=true \
  --set 'ingress.className=nginx' \
  --set 'ingress.hosts[0].host=stella.example.com' \
  --set 'ingress.hosts[0].paths[0].path=/' \
  --set 'ingress.hosts[0].paths[0].pathType=Prefix' \
  --set 'ingress.tls[0].secretName=stella-tls' \
  --set 'ingress.tls[0].hosts[0]=stella.example.com'
assert_contains "ingress rendered" "kind: Ingress"

echo
echo "== must-fail cases =="
expect_fail "missing baseURL" "baseURL" \
  --set secrets.existingSecret=stella-secrets --set sandbox.backend=local
expect_fail "missing existingSecret" "existingSecret" \
  --set baseURL=https://stella.example.com --set sandbox.backend=local
expect_fail "replicaCount=2" "replica" \
  "${BASE[@]}" --set sandbox.backend=local --set replicaCount=2
expect_fail "sandbox backend missing" "sandbox.backend" "${BASE[@]}"
expect_fail "sandbox=none without confirmation" "allowUnsafeHostExecution" \
  "${BASE[@]}" --set sandbox.backend=none
expect_fail "grace period too small" "terminationGracePeriodSeconds" \
  "${BASE[@]}" --set sandbox.backend=local \
  --set shutdown.terminationGracePeriodSeconds=50
expect_fail "persistence off without confirmation" "allowEphemeralDataLoss" \
  "${BASE[@]}" --set sandbox.backend=local --set persistence.enabled=false
expect_fail "extraEnv overrides sandbox" "extraEnv must not set STELLA_SANDBOX_BACKEND" \
  "${BASE[@]}" --set sandbox.backend=local \
  --set 'extraEnv[0].name=STELLA_SANDBOX_BACKEND' --set 'extraEnv[0].value=none'
expect_fail "extraEnv injects vault key" "extraEnv must not set STELLA_VAULT_KEY" \
  "${BASE[@]}" --set sandbox.backend=local \
  --set 'extraEnv[0].name=STELLA_VAULT_KEY' --set 'extraEnv[0].value=fake'
expect_fail "extraEnv overrides HOST" "extraEnv must not set HOST" \
  "${BASE[@]}" --set sandbox.backend=local \
  --set 'extraEnv[0].name=HOST' --set 'extraEnv[0].value=127.0.0.1'
expect_fail "baseURL without scheme" "scheme" \
  --set baseURL=stella.example.com --set secrets.existingSecret=stella-secrets \
  --set sandbox.backend=local
expect_fail "baseURL loopback host" "loopback" \
  --set baseURL=http://localhost:25678 --set secrets.existingSecret=stella-secrets \
  --set sandbox.backend=local
expect_fail "baseURL IPv6 loopback" "loopback" \
  --set 'baseURL=https://[::1]:8443' --set secrets.existingSecret=stella-secrets \
  --set sandbox.backend=local
expect_fail "podLabels overrides selector" "podLabels must not set" \
  "${BASE[@]}" --set sandbox.backend=local \
  --set 'podLabels.app\.kubernetes\.io/name=evil'
expect_fail "invalid seccompProfile rejected" "seccompProfile" \
  "${BASE[@]}" --set sandbox.backend=local --set sandbox.seccompProfile=Wide
expect_fail "none must not weaken seccomp" "seccompProfile=RuntimeDefault" \
  "${BASE[@]}" --set sandbox.backend=none --set sandbox.allowUnsafeHostExecution=true \
  --set sandbox.seccompProfile=Unconfined
expect_fail "ingress enabled without hosts" "ingress.hosts" \
  "${BASE[@]}" --set sandbox.backend=local --set ingress.enabled=true --set 'ingress.hosts=null'

echo
if [ "$rc" -eq 0 ]; then echo "All Helm checks passed."; else echo "Helm checks FAILED."; fi
exit "$rc"
