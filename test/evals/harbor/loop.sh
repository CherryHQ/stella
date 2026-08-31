#!/usr/bin/env bash
# The fix-iteration loop in one command: build, disposable testbed, provision,
# Harbor run, report, manifest. PROTOCOL.md owns what the numbers mean; this
# script only makes producing them a single step.
#
# Gateway credentials are yours to export (`set -a; . ./.env; set +a`). This
# script never reads .env, never prints a secret, and never puts one in a
# process argument: request bodies go in from private files, bearer tokens go
# in through a curl config on stdin.
set -euo pipefail
umask 077

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
HARBOR_DIR=$REPO_ROOT/test/evals/harbor
# shellcheck source=stellad_wrapper.sh
source "$HARBOR_DIR/stellad_wrapper.sh"
# shellcheck source=run_state.sh
source "$HARBOR_DIR/run_state.sh"
DATASET=terminal-bench/terminal-bench-2-1
PROVIDER_ID=${STELLA_EVAL_PROVIDER_ID:-gateway}
PROVIDER_TYPE=${STELLA_EVAL_PROVIDER_TYPE:-openai-response}
MODEL_ID=${STELLA_EVAL_MODEL_ID:-gpt-5.6-luna}
MODEL=$PROVIDER_ID/$MODEL_ID
READY_TIMEOUT=${STELLA_EVAL_READY_TIMEOUT:-120}
# Any fixed port collides on someone's machine. The testbed port uses a kernel
# allocation; Docker does the same for OTel's published ports below.
TIER=full OTEL="" REUSE_TESTBED=0 PLAN=0 AGAINST="" EXCLUDED_TOOLS=""

die() { echo "eval:loop: $*" >&2; exit 1; }
step() { echo "==> $*"; }
free_port() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()'; }
usage() {
  cat <<'EOF'
usage: mise run eval:loop [-- [--tier quick|full] [--otel|--no-otel]
                           [--excluded-tools TOOL,...] [--reuse-testbed]
                           [--plan] [--against REF_JOB] [harbor args...]]

  --tier TIER       quick (k=1, six tasks) or full (12-task baseline); default full
  --otel            force OTel on (quick defaults on, full defaults off)
  --no-otel         force OTel off
  --excluded-tools  additional comma-separated tool names to hide for each session run
  --reuse-testbed   reuse a healthy, already-running bridge testbed with OTel off
  --plan            print the safe execution plan, run nothing
  --against REF     compare the completed job against REF_JOB

Harbor always excludes view_image and vllm. Code Mode keeps every other
registered Stella capability enabled; only bash is counted as task-container
execution because the bridge can attribute it. The selected taskset owns
concurrency; eval:loop always passes its explicit -n.
EOF
}

while [ $# -gt 0 ]; do
  case $1 in
    --tier) [ $# -ge 2 ] || die "--tier needs quick or full"; TIER=$2; shift ;;
    --tier=*) TIER=${1#*=} ;;
    --otel) OTEL=1 ;;
    --no-otel) OTEL=0 ;;
    --excluded-tools) [ $# -ge 2 ] || die "--excluded-tools needs a comma-separated list"; EXCLUDED_TOOLS=$2; shift ;;
    --excluded-tools=*) EXCLUDED_TOOLS=${1#*=} ;;
    --reuse-testbed) REUSE_TESTBED=1 ;;
    --plan) PLAN=1 ;;
    --against) [ $# -ge 2 ] || die "--against needs a reference job directory"; AGAINST=$2; shift ;;
    --against=*) AGAINST=${1#*=} ;;
    -h|--help) usage; exit 0 ;;
    --) shift; break ;;
    *) break ;;
  esac
  shift
done
HARBOR_ARGS=("$@")
EXCLUDED_TOOLS=$(python3 - "$EXCLUDED_TOOLS" <<'PY'
import sys
excluded = {name.strip() for name in sys.argv[1].split(",") if name.strip()}
# The bridge ledger has no child provenance for read_file. Pin the harness to
# bash, whose successful and nonzero paths are uniquely corroborated by exec.
excluded.update({"view_image", "vllm"})
print(",".join(sorted(excluded)))
PY
)
case $TIER in
  quick) TASKSET=$HARBOR_DIR/tasksets/quick.yaml ;;
  full) TASKSET=$HARBOR_DIR/tasksets/loop.yaml ;;
  *) die "unknown tier $TIER (want quick or full)" ;;
esac
[ -f "$TASKSET" ] || die "missing taskset: $TASKSET"
[ -n "$OTEL" ] || { [ "$TIER" = quick ] && OTEL=1 || OTEL=0; }
[ "$REUSE_TESTBED" = 0 ] || [ "$OTEL" = 0 ] || die "--reuse-testbed cannot retrofit OTel into an already-started testbed; use --no-otel"

# Harbor refuses a task filter on top of --config. Keep its prior caller-source
# behavior, but always make the selected taskset's concurrency explicit.
source_args=(-c "$TASKSET")
source_kind="tier $TIER ($TASKSET)"
using_taskset=1
caller_config=""
caller_concurrency=0
arg_index=0
arg_count=$#
while [ "$arg_index" -lt "$arg_count" ]; do
  arg=${HARBOR_ARGS[$arg_index]}
  case $arg in
    -c|--config)
      arg_index=$((arg_index + 1)); [ "$arg_index" -lt "$arg_count" ] || die "$arg needs a config path"
      caller_config=${HARBOR_ARGS[$arg_index]}; source_args=(); source_kind="caller-supplied"; using_taskset=0 ;;
    --config=*) caller_config=${arg#*=}; source_args=(); source_kind="caller-supplied"; using_taskset=0 ;;
    -c?*) caller_config=${arg#-c}; source_args=(); source_kind="caller-supplied"; using_taskset=0 ;;
    -d|--dataset|--dataset=*|--task|--task=*|--path|--path=*|-d?*) source_args=(); source_kind="caller-supplied"; using_taskset=0 ;;
    -i|--include-task-name|--exclude-task-name|-i=*|--include-task-name=*|--exclude-task-name=*|-i?*) source_args=(-d "$DATASET"); source_kind="tier $TIER dataset $DATASET (task filter given)"; using_taskset=0 ;;
    -n|--n-concurrent|--n-concurrent=*|-n?*) caller_concurrency=1 ;;
  esac
  arg_index=$((arg_index + 1))
done

# Harbor does not preserve YAML concurrency in job config. Parse it from the
# selected taskset, or from a local caller-supplied config when there is one.
concurrency_taskset=$TASKSET
[ -z "$caller_config" ] || [ ! -f "$caller_config" ] || concurrency_taskset=$caller_config
TASKSET_CONCURRENCY=$(python3 - "$concurrency_taskset" <<'PY'
import re, sys
for line in open(sys.argv[1]):
    m = re.fullmatch(r"n_concurrent_trials:\s*([1-9][0-9]*)\s*", line)
    if m:
        print(m.group(1))
        break
else:
    raise SystemExit("taskset has no positive n_concurrent_trials")
PY
)

STELLA_URL_WAS_SET=${STELLA_URL+x}
STELLA_TESTBED_PORT=${STELLA_TESTBED_PORT:-$(free_port)}
STELLA_URL=${STELLA_URL:-http://127.0.0.1:$STELLA_TESTBED_PORT}
JOB_PREFIX=$TIER
[ "$TIER" != full ] || JOB_PREFIX=loop # Preserve the established full baseline job layout.
JOB_BASE=$REPO_ROOT/dist/evals/jobs/$JOB_PREFIX-$(date -u +%Y%m%dT%H%M%SZ)
JOB=$JOB_BASE
RUN_STATE=$REPO_ROOT/dist/evals/runs/$(basename "$JOB")
set_run_paths() {
  MANIFEST=$JOB.manifest.json
  TESTBED_ROOT=$RUN_STATE/testbed-root
  TESTBED_LOG=$RUN_STATE/testbed.log
}
build_harbor_cmd() {
  harbor_cmd=(uv run --project "$HARBOR_DIR" harbor run ${source_args[@]+"${source_args[@]}"} -a stella_harbor.agent:StellaAgent -m "$MODEL" -o "$JOB" ${HARBOR_ARGS[@]+"${HARBOR_ARGS[@]}"})
  if [ "$caller_concurrency" = 0 ]; then
    harbor_cmd=(uv run --project "$HARBOR_DIR" harbor run ${source_args[@]+"${source_args[@]}"} -n "$TASKSET_CONCURRENCY" -a stella_harbor.agent:StellaAgent -m "$MODEL" -o "$JOB" ${HARBOR_ARGS[@]+"${HARBOR_ARGS[@]}"})
  fi
}
set_run_paths
AGENT_BIN=$REPO_ROOT/dist/bin-eval/stella-eval-agent

if [ "$PLAN" = 1 ]; then
  gateway_state="(MISSING)"; [ -z "${OPENAI_BASE_URL:-}" ] || gateway_state="(set, host $(python3 -c 'import os,urllib.parse; print(urllib.parse.urlsplit(os.environ["OPENAI_BASE_URL"]).hostname or "?")'))"
  key_state="(MISSING)"; [ -z "${OPENAI_API_KEY:-}" ] || key_state="(set)"
  cat <<EOF
plan only, nothing is executed.

1. preflight   tier $TIER; taskset $TASKSET$( [ "$caller_concurrency" = 0 ] && echo "; explicit concurrency -n $TASKSET_CONCURRENCY" || echo "; caller-supplied concurrency" )
               excluded tools: $( [ -n "$EXCLUDED_TOOLS" ] && echo "$EXCLUDED_TOOLS" || echo "none" )
               Harbor trusted treatment: bash-only execution capability
               OPENAI_BASE_URL $gateway_state and OPENAI_API_KEY $key_state exported
2. build       mise run eval:build, only when each binary is older than its sources
3. otel        $( [ "$OTEL" = 1 ] && echo "docker run -d grafana/otel-lgtm; discover kernel-assigned OTLP HTTP and Grafana ports; install an OTel wrapper only around the private stellad copy under $TESTBED_ROOT. Before testbed start the wrapper exports the local OTLP settings, disables logs/metrics, raises OTEL_BSP_MAX_QUEUE_SIZE for the six-trial wave, and flushes once per second; shared dist/bin/stellad is never modified." || echo "disabled (full baseline default)" )
4. testbed     $( [ "$REUSE_TESTBED" = 1 ] && echo "reuse STELLA_URL after bridge and build-commit health checks, OTel must be off; credentials path and bridge dir are caller-supplied" || echo "copy eval binaries to $TESTBED_ROOT; start a fresh bridge testbed on $STELLA_URL (log $TESTBED_LOG)" )
5. provision   credentials-file path only; private cookie jar; provider and provisioning token
6. run         source: $source_kind; Harbor command $( [ "$caller_concurrency" = 0 ] && echo "uses explicit -n $TASKSET_CONCURRENCY" || echo "preserves caller-supplied concurrency" )
7. after       report$( [ "$OTEL" = 1 ] && echo ", Tempo span analysis and per-trial nonzero-span assertion" ); manifest -> $MANIFEST
   cleanup     $( [ "$REUSE_TESTBED" = 1 ] && echo "reused testbed preserved" || echo "testbed stopped" ); OTel container preserved
EOF
  exit 0
fi

step "preflight"
[ "$(git -C "$REPO_ROOT" rev-parse --show-toplevel 2>/dev/null)" = "$REPO_ROOT" ] || die "not at repository root"
for tool in mise uv curl python3 go docker; do command -v "$tool" >/dev/null || die "$tool is required"; done
docker info >/dev/null 2>&1 || die "docker is not running"
[ -n "${OPENAI_BASE_URL:-}" ] && [ -n "${OPENAI_API_KEY:-}" ] || die "export OPENAI_BASE_URL and OPENAI_API_KEY first"
[ -z "$AGAINST" ] || [ -d "$AGAINST" ] || die "--against: no such job directory: $AGAINST"
[ "$REUSE_TESTBED" = 0 ] || [ -n "$STELLA_URL_WAS_SET" ] || die "--reuse-testbed requires STELLA_URL"
[ "$REUSE_TESTBED" = 0 ] || [ -n "${STELLA_TESTBED_CREDENTIALS:-}" ] || die "--reuse-testbed requires STELLA_TESTBED_CREDENTIALS"
[ "$REUSE_TESTBED" = 0 ] || [ -n "${STELLA_EVAL_BRIDGE_DIR:-}" ] || die "--reuse-testbed requires the original STELLA_EVAL_BRIDGE_DIR"
if [ "$REUSE_TESTBED" = 1 ]; then
  python3 - "$STELLA_URL" <<'PY' || die "--reuse-testbed requires a loopback http:// STELLA_URL without credentials, query, or fragment"
import ipaddress, sys
from urllib.parse import urlsplit
url = urlsplit(sys.argv[1])
try:
    loopback = ipaddress.ip_address(url.hostname or "").is_loopback
except ValueError:
    loopback = (url.hostname == "localhost")
ok = (url.scheme == "http" and loopback and url.port is not None and
      url.username is None and url.password is None and
      url.path in ("", "/") and not url.query and not url.fragment)
raise SystemExit(0 if ok else 1)
PY
fi

RUNS_ROOT=$REPO_ROOT/dist/evals/runs
mkdir -p "$RUNS_ROOT"
# Crash residue is deliberately discoverable under dist/evals/runs. Only dead
# owners older than one day are pruned; live owners are never removed.
prune_stale_run_states "$RUNS_ROOT" 86400
claim_run_state "$JOB_BASE" "$RUNS_ROOT" 100 "$$" || die "cannot claim eval run state"
JOB=$CLAIMED_JOB
RUN_STATE=$CLAIMED_RUN_STATE
set_run_paths
build_harbor_cmd
WORK=$(mktemp -d)
COOKIE_JAR=$WORK/cookies.txt
TESTBED_STARTED=0 OTEL_CONTAINER=""
cleanup() {
  status=$?
  set +e
  if [ "$TESTBED_STARTED" = 1 ]; then
    step "stopping the testbed"
    (cd "$TESTBED_ROOT" && ./dist/bin/testbed stop) >/dev/null 2>&1
  fi
  rm -f "$COOKIE_JAR"; rm -rf "$WORK"
  if [ "$status" -eq 0 ]; then
    rm -rf "$RUN_STATE"
  else
    echo "eval:loop: run state kept at $RUN_STATE" >&2
  fi
  [ -z "$OTEL_CONTAINER" ] || echo "eval:loop: OTel is still running; stop it with: docker stop $OTEL_CONTAINER" >&2
  [ "$status" -eq 0 ] || [ ! -d "$JOB" ] || echo "eval:loop: failed; Harbor job kept at $JOB" >&2
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

api() {
  # Secrets never reach argv: bodies arrive from a private file and bearer
  # tokens from a curl config on stdin. Failures never print response bodies.
  local method=$1 path=$2 auth=$3 body=${4:-} status
  local args=(-sS -X "$method" -H 'Content-Type: application/json' -o "$WORK/response.json" -w '%{http_code}')
  [ -z "$body" ] || args+=(--data-binary "@$body")
  [ "$auth" != cookie ] || args+=(-b "$COOKIE_JAR" -c "$COOKIE_JAR")
  if [ "$auth" = bearer ]; then status=$(printf 'header = "Authorization: Bearer %s"\n' "$ADMIN_PAT" | curl "${args[@]}" -K - "$STELLA_URL$path"); else status=$(curl "${args[@]}" "$STELLA_URL$path"); fi
  case $status in 2*) cat "$WORK/response.json" ;; *) die "$method $path failed with HTTP $status" ;; esac
}

step "capturing build/start snapshot and preparing binaries"
SNAPSHOT_COMMIT=$(git -C "$REPO_ROOT" rev-parse HEAD); export SNAPSHOT_COMMIT
(cd "$REPO_ROOT" && mise run eval:build)
if [ "$REUSE_TESTBED" = 0 ]; then
  mkdir -p "$TESTBED_ROOT/dist/bin"
  cp "$REPO_ROOT/dist/bin/stellad" "$TESTBED_ROOT/dist/bin/stellad"
  cp "$REPO_ROOT/dist/bin/testbed" "$TESTBED_ROOT/dist/bin/testbed"
fi

if [ "$OTEL" = 1 ]; then
  step "starting OTel LGTM on kernel-assigned ports"
  OTEL_CONTAINER=$(docker run -d -p 127.0.0.1::4318 -p 127.0.0.1::3000 grafana/otel-lgtm)
  OTEL_OTLP_PORT=$(docker port "$OTEL_CONTAINER" 4318/tcp | sed -n 's/.*:\([0-9][0-9]*\)$/\1/p' | head -1)
  OTEL_GRAFANA_PORT=$(docker port "$OTEL_CONTAINER" 3000/tcp | sed -n 's/.*:\([0-9][0-9]*\)$/\1/p' | head -1)
  [ -n "$OTEL_OTLP_PORT" ] && [ -n "$OTEL_GRAFANA_PORT" ] || die "could not determine OTel host ports"
  deadline=$((SECONDS + READY_TIMEOUT))
  until curl -fsS "http://127.0.0.1:$OTEL_GRAFANA_PORT/api/datasources/proxy/uid/tempo/ready" >/dev/null 2>&1; do
    [ "$SECONDS" -lt "$deadline" ] || die "Tempo did not become ready within ${READY_TIMEOUT}s"
    sleep 1
  done
  echo "    Grafana/Tempo: http://127.0.0.1:$OTEL_GRAFANA_PORT"
  # testbed filters OTEL_* from child env. Wrap only this run's private binary;
  # concurrent evals and later no-otel runs never observe the wrapper.
  stage_otel_stellad_wrapper "$TESTBED_ROOT/dist/bin/stellad" "$TESTBED_ROOT/dist/bin/stellad.real" "http://127.0.0.1:$OTEL_OTLP_PORT"
fi

export PROVIDER_ID PROVIDER_TYPE MODEL_ID MODEL STELLA_URL STELLA_TESTBED_PORT
export STELLA_SANDBOX_BACKEND=bridge
# It must be exported before testbed:start; the server reads it once, and
# exporting it afterwards silently leaves the backend on local.
STELLA_EVAL_BRIDGE_DIR=${STELLA_EVAL_BRIDGE_DIR:-$(mktemp -d)}; export STELLA_EVAL_BRIDGE_DIR
mkdir -p "$(dirname "$TESTBED_LOG")"
if [ "$REUSE_TESTBED" = 1 ]; then
  step "checking the reused bridge testbed"
  CREDS=$STELLA_TESTBED_CREDENTIALS; [ -f "$CREDS" ] || die "STELLA_TESTBED_CREDENTIALS is not a file"
else
  step "starting a disposable testbed on the bridge backend"
  # `postgres download` is idempotent: the existing stellad checks for its
  # runtime and downloads only when absent. It does not invoke mise build or
  # generate, so a fresh dist/bin/stellad stays genuinely reusable.
  (cd "$TESTBED_ROOT" && ./dist/bin/stellad postgres download && exec ./dist/bin/testbed start) >"$TESTBED_LOG" 2>&1 &
  TESTBED_PID=$!; TESTBED_STARTED=1
fi
deadline=$((SECONDS + READY_TIMEOUT))
until curl -fsS "$STELLA_URL/api/status" -o "$WORK/status.json" 2>/dev/null &&
  python3 - "$WORK/status.json" "$SNAPSHOT_COMMIT" <<'PY' 2>/dev/null
import json, sys
status = json.load(open(sys.argv[1]))
commit = status.get("commit") or ""
ok = status.get("sandbox_backend") == "bridge" and commit and sys.argv[2].startswith(commit)
raise SystemExit(0 if ok else 1)
PY
do
  if [ "$REUSE_TESTBED" = 0 ]; then kill -0 "$TESTBED_PID" 2>/dev/null || die "testbed exited early; see $TESTBED_LOG"; fi
  [ "$SECONDS" -lt "$deadline" ] || die "testbed did not report bridge mode at build commit $SNAPSHOT_COMMIT within ${READY_TIMEOUT}s"
  sleep 2
done

step "provisioning the provider, model, and a provisioning token"
if [ "$REUSE_TESTBED" = 0 ]; then
	# mise prefixes every line with the task name, so the anchor is anywhere on
	# the line, not at its start.
	# The status endpoint can answer before the credentials line reaches the log,
	# so poll for it rather than reading once and giving up on a lost race.
	creds_deadline=$((SECONDS + 60))
	while :; do
		CREDS=$(sed -n 's/^.*Credentials: //p' "$TESTBED_LOG" | head -1)
		[ -n "$CREDS" ] && [ -f "$CREDS" ] && break
		[ "$SECONDS" -lt "$creds_deadline" ] || die "the testbed printed no credentials-file path within 60s; see $TESTBED_LOG"
		sleep 1
	done
fi
# Only the path is read from stdout and only fields are read from the file;
# neither is ever echoed.
ADMIN_PAT=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["admin"]["token"])' "$CREDS")
python3 - "$WORK/login.json" "$CREDS" <<'PY'
import json, sys
admin = json.load(open(sys.argv[2]))["admin"]
json.dump({"email": admin["email"], "password": admin["password"]}, open(sys.argv[1], "w"))
PY
api POST /api/auth/local/login cookie "$WORK/login.json" >/dev/null
python3 - "$WORK/provider.json" <<'PY'
import json, os, sys
# Per-million-token prices, the same numbers the pi baseline is scored with, so
# the two cost columns mean the same thing.
cost = {"input": float(os.environ.get("EVAL_COST_INPUT", "0.20")), "output": float(os.environ.get("EVAL_COST_OUTPUT", "1.20")), "cacheRead": float(os.environ.get("EVAL_COST_CACHE_READ", "0.02")), "cacheWrite": float(os.environ.get("EVAL_COST_CACHE_WRITE", "0.25"))}
json.dump({"id": os.environ["PROVIDER_ID"], "type": os.environ["PROVIDER_TYPE"], "name": "Eval gateway", "enabled": True, "api_key": os.environ["OPENAI_API_KEY"], "base_url": os.environ["OPENAI_BASE_URL"], "models": {os.environ["MODEL_ID"]: {"enabled": True, "cost": cost}}}, open(sys.argv[1], "w"))
PY
api POST /api/providers bearer "$WORK/provider.json" >/dev/null
python3 - "$WORK/provisioning.json" <<'PY'
import datetime, json, sys
expires = datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(hours=6)
json.dump({"name": "eval-loop", "expires_at": expires.strftime("%Y-%m-%dT%H:%M:%SZ")}, open(sys.argv[1], "w"))
PY
# A PAT gets 403 here; this one needs the interactive session cookie.
api POST /api/admin/provisioning-tokens cookie "$WORK/provisioning.json" >"$WORK/token.json"
STELLA_EVAL_ADMIN_TOKEN=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["token"])' "$WORK/token.json")
# The admin PAT retrieves one safe DTO before Harbor starts. The host driver
# gets only this private file path, never an admin credential or provider JSON.
# The script-wide umask above already keeps this file private.
STELLA_EVAL_PROVIDER_EVIDENCE_FILE=$WORK/provider-evidence.json
# Model ids carry slashes and colons; they are a query value, not a path.
MODEL_ID_ENCODED=$(python3 -c 'import sys, urllib.parse; print(urllib.parse.quote(sys.argv[1], safe=""))' "$MODEL_ID")
api GET "/api/providers/$PROVIDER_ID/evidence?model_id=$MODEL_ID_ENCODED" bearer >"$STELLA_EVAL_PROVIDER_EVIDENCE_FILE"
export STELLA_EVAL_ADMIN_TOKEN STELLA_EVAL_PROVIDER_EVIDENCE_FILE STELLA_EVAL_MODEL=$MODEL STELLA_EVAL_AGENT_BIN=$AGENT_BIN
export STELLA_EVAL_EXCLUDED_TOOLS=$EXCLUDED_TOOLS

step "running Harbor: $source_kind"; echo "    job: $JOB"
# The first run of a task pays the image pull; Harbor has no separate prefetch.
"${harbor_cmd[@]}" --print-config >"$WORK/config.json"
"${harbor_cmd[@]}"
step "report"
uv run --project "$HARBOR_DIR" python -m stella_harbor.report "$JOB"
if [ -n "$AGAINST" ]; then
  step "comparing against $AGAINST (candidate)"
  # Harbor dumps the run config with exclude_defaults=True, so a k=1 job never
  # records n_attempts and the comparator cannot verify the budget. Supplying
  # the k we actually ran is the only way a quick-tier comparison is not
  # refused; it is rejected outright if it disagrees with what a job recorded.
  RUN_K=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("n_attempts", 1))' "$WORK/config.json")
  uv run --project "$HARBOR_DIR" python -m stella_harbor.compare "$JOB" "$AGAINST" --k "$RUN_K" || true
fi

: >"$WORK/args.txt"; [ -z "${HARBOR_ARGS[*]-}" ] || printf '%s\n' "${HARBOR_ARGS[@]}" >"$WORK/args.txt"
TASKSET_PATH=$([ "$using_taskset" = 1 ] && echo "${TASKSET#"$REPO_ROOT"/}" || echo ""); export TASKSET_PATH JOB OTEL EXCLUDED_TOOLS
python3 - "$MANIFEST" "$WORK/config.json" "$WORK/args.txt" <<'PY'
import datetime, hashlib, json, os, subprocess, sys
from urllib.parse import urlsplit
config = json.load(open(sys.argv[2]))
tasks = sorted({t for d in config.get("datasets", []) for t in (d.get("task_names") or [])})
git = lambda *a: subprocess.run(["git", *a], capture_output=True, text=True).stdout.strip()
# Values are already represented by normalized config fields above. Persist only
# option names, never arbitrary values that may be credentials or private paths.
harbor_flags = [arg.split("=", 1)[0] for arg in open(sys.argv[3]).read().split() if arg.startswith("-")]
json.dump({"created_at": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"), "job": os.path.basename(os.environ["JOB"]), "commit": os.environ["SNAPSHOT_COMMIT"], "dirty": bool(git("status", "--porcelain")), "taskset": os.environ["TASKSET_PATH"] or None, "task_names": tasks, # Canonical over sorted dataset-qualified names.
"task_hash": "sha256:" + hashlib.sha256("\n".join(tasks).encode()).hexdigest(), "k": config.get("n_attempts", 1), "concurrency": config.get("n_concurrent_trials"), "model": os.environ["MODEL"], # Host only: the path can carry a deployment id.
"requested_gateway_host": urlsplit(os.environ["OPENAI_BASE_URL"]).hostname, "harbor_args": harbor_flags, "otel": os.environ["OTEL"] == "1",
"excluded_tools": os.environ["EXCLUDED_TOOLS"].split(",") if os.environ["EXCLUDED_TOOLS"] else []}, open(sys.argv[1], "w"), indent=2)
PY
step "manifest: $MANIFEST"
if [ "$OTEL" = 1 ]; then
  step "analyzing OTel spans (Grafana Tempo proxy)"
  uv run --project "$HARBOR_DIR" python -m stella_harbor.otel "$JOB" --grafana-url "http://127.0.0.1:$OTEL_GRAFANA_PORT"
fi
