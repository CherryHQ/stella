#!/usr/bin/env bash
# The fix-iteration loop in one command: build, disposable testbed, provision,
# Harbor run, report, manifest. PROTOCOL.md owns what the numbers mean; this
# script only makes producing them a single step.
#
# Gateway credentials are yours to export (`set -a; . ./.env; set +a`). This
# script never reads .env, never prints a secret, and never puts one in a
# process argument: request bodies go in from private files, bearer tokens go
# in through a curl config on stdin.
#
#   mise run eval:loop                              # the default 12-task set
#   mise run eval:loop -- -i terminal-bench/build-cython-ext -k 5
#   mise run eval:loop -- --against dist/evals/jobs/loop-<earlier>
#   mise run eval:loop -- --plan                    # print the steps, run none

set -euo pipefail
umask 077 # every file this script creates is private from birth

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
HARBOR_DIR=$REPO_ROOT/test/evals/harbor
TASKSET=$HARBOR_DIR/tasksets/loop.yaml
DATASET=terminal-bench/terminal-bench-2-1
PROVIDER_ID=${STELLA_EVAL_PROVIDER_ID:-gateway}
PROVIDER_TYPE=${STELLA_EVAL_PROVIDER_TYPE:-openai-response}
MODEL_ID=${STELLA_EVAL_MODEL_ID:-gpt-5.6-luna}
MODEL=$PROVIDER_ID/$MODEL_ID
# Any fixed port collides on someone's machine: 25678 is a dev server, 25679 a
# production stellad. Ask the kernel for a free one instead. There is a race
# between this bind and the server's, and it is deliberately not defended
# against: losing it fails loudly at startup with the port in the message.
# testbed:stop is keyed by repository root, not by port, so it cleans up
# whatever port the run picked.
STELLA_TESTBED_PORT=${STELLA_TESTBED_PORT:-$(python3 -c 'import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()')}
STELLA_URL=${STELLA_URL:-http://127.0.0.1:$STELLA_TESTBED_PORT}
READY_TIMEOUT=${STELLA_EVAL_READY_TIMEOUT:-120}
TESTBED_LOG=$REPO_ROOT/dist/logs/eval-loop-testbed.log
export PROVIDER_ID PROVIDER_TYPE MODEL_ID MODEL STELLA_URL STELLA_TESTBED_PORT

usage() {
	cat <<'EOF'
usage: mise run eval:loop [-- [--plan] [--against REF_JOB] [harbor args...]]

  --plan            print every step and the exact commands, execute nothing
  --against REF     after the run, compare it against a reference job
                    directory (the new run is the candidate)
  -h, --help        this text

Anything else is passed to `harbor run` verbatim. The default source is
tasksets/loop.yaml; passing -i/--include-task-name switches the source to the
Terminal-Bench 2.1 dataset, because Harbor refuses task filters on top of a
config file. Task names must be dataset-qualified (terminal-bench/<task>): a
bare name matches nothing and silently runs all 89 tasks.

Required in the environment: OPENAI_BASE_URL and OPENAI_API_KEY for the eval
gateway. Export them yourself; this script never reads .env.

The testbed listens on STELLA_TESTBED_PORT, a free port taken from the kernel
unless you set it, so a dev or production server on this machine is untouched.
EOF
}

die() {
	echo "eval:loop: $*" >&2
	exit 1
}
step() { echo "==> $*"; }

PLAN=0
AGAINST=""
while [ $# -gt 0 ]; do
	case $1 in
	--plan) PLAN=1 ;;
	--against)
		[ $# -ge 2 ] || die "--against needs a reference job directory"
		AGAINST=$2
		shift
		;;
	--against=*) AGAINST=${1#*=} ;;
	-h | --help)
		usage
		exit 0
		;;
	--)
		shift
		break
		;;
	*) break ;;
	esac
	shift
done
HARBOR_ARGS=("$@")

# Harbor refuses --include-task-name on top of --config, so a targeted run
# swaps the task set for the dataset it was drawn from rather than failing.
source_args=(-c "$TASKSET")
source_kind="taskset $TASKSET"
for arg in ${HARBOR_ARGS[@]+"${HARBOR_ARGS[@]}"}; do
	case $arg in
	-c | --config | -d | --dataset | --task | --path)
		source_args=()
		source_kind="caller-supplied"
		break
		;;
	-i | --include-task-name | --exclude-task-name)
		source_args=(-d "$DATASET")
		source_kind="dataset $DATASET (task filter given)"
		;;
	esac
done

JOB=$REPO_ROOT/dist/evals/jobs/loop-$(date -u +%Y%m%dT%H%M%SZ)
MANIFEST=$JOB.manifest.json
AGENT_BIN=$REPO_ROOT/dist/bin-eval/stella-eval-agent
harbor_cmd=(uv run --project "$HARBOR_DIR" harbor run
	${source_args[@]+"${source_args[@]}"}
	-a stella_harbor.agent:StellaAgent -m "$MODEL" -o "$JOB"
	${HARBOR_ARGS[@]+"${HARBOR_ARGS[@]}"})

if [ "$PLAN" = 1 ]; then
	# Status words only, computed here rather than substituted in the heredoc:
	# `${VAR:-default}` expands to the *value* when the variable is set, which
	# is how a plan meant to say "(set)" printed the key instead.
	gateway_state="(MISSING)"
	[ -z "${OPENAI_BASE_URL:-}" ] ||
		gateway_state="(set, host $(python3 -c 'import os,urllib.parse; print(urllib.parse.urlsplit(os.environ["OPENAI_BASE_URL"]).hostname or "?")'))"
	key_state="(MISSING)"
	# Never any part of the key, not even a prefix or a length.
	[ -z "${OPENAI_API_KEY:-}" ] || key_state="(set)"
	cat <<EOF
plan only, nothing is executed.

1. preflight   repo root $REPO_ROOT; docker, uv, curl, python3 present;
               OPENAI_BASE_URL $gateway_state and OPENAI_API_KEY $key_state exported
2. build       mise run build
               go build -o $AGENT_BIN ./cmd/stella-eval-agent
3. testbed     STELLA_SANDBOX_BACKEND=bridge, fresh STELLA_EVAL_BRIDGE_DIR,
               STELLA_TESTBED_PORT=$STELLA_TESTBED_PORT (free port from the kernel; no fixed default to collide)
               mise run testbed:start (background, log $TESTBED_LOG)
               poll $STELLA_URL/api/status for sandbox_backend=bridge, ${READY_TIMEOUT}s
4. provision   read the credentials-file path the testbed prints (never its body)
               POST /api/auth/local/login    -> private cookie jar, deleted on exit
               POST /api/providers           -> $PROVIDER_ID ($PROVIDER_TYPE), model $MODEL_ID enabled
               POST /api/admin/provisioning-tokens (session cookie; a PAT gets 403)
               export STELLA_URL STELLA_EVAL_MODEL=$MODEL STELLA_EVAL_ADMIN_TOKEN STELLA_EVAL_AGENT_BIN
5. run         source: $source_kind
               ${harbor_cmd[*]}
6. after       python -m stella_harbor.report $JOB${AGAINST:+
               python -m stella_harbor.compare $JOB $AGAINST}
               manifest -> $MANIFEST
   cleanup     mise run testbed:stop, cookie jar deleted, job kept on failure
EOF
	exit 0
fi

step "preflight"
[ "$(git -C "$REPO_ROOT" rev-parse --show-toplevel 2>/dev/null)" = "$REPO_ROOT" ] ||
	die "not at the repository root: $REPO_ROOT"
for tool in mise uv curl python3 go docker; do
	command -v "$tool" >/dev/null || die "$tool is required and not on PATH"
done
docker info >/dev/null 2>&1 || die "docker is not running"
[ -n "${OPENAI_BASE_URL:-}" ] && [ -n "${OPENAI_API_KEY:-}" ] ||
	die "export OPENAI_BASE_URL and OPENAI_API_KEY first (set -a; . ./.env; set +a)"
[ -z "$AGAINST" ] || [ -d "$AGAINST" ] || die "--against: no such job directory: $AGAINST"

WORK=$(mktemp -d)
COOKIE_JAR=$WORK/cookies.txt
TESTBED_STARTED=0
cleanup() {
	status=$?
	set +e
	rm -f "$COOKIE_JAR"
	rm -rf "$WORK"
	if [ "$TESTBED_STARTED" = 1 ]; then
		step "stopping the testbed"
		(cd "$REPO_ROOT" && mise run testbed:stop) >/dev/null 2>&1
	fi
	if [ "$status" -ne 0 ] && [ -d "$JOB" ]; then
		echo "eval:loop: failed; the Harbor job is kept at $JOB" >&2
	fi
}
trap cleanup EXIT

# curl wrapper. Secrets never reach argv: bodies arrive from a private file and
# the bearer token from a curl config on stdin. Failures report the status code
# and never the response body, which can quote what was sent.
api() {
	local method=$1 path=$2 auth=$3 body=${4:-} status
	local args=(-sS -X "$method" -H 'Content-Type: application/json'
		-o "$WORK/response.json" -w '%{http_code}')
	[ -z "$body" ] || args+=(--data-binary "@$body")
	[ "$auth" != cookie ] || args+=(-b "$COOKIE_JAR" -c "$COOKIE_JAR")
	if [ "$auth" = bearer ]; then
		status=$(printf 'header = "Authorization: Bearer %s"\n' "$ADMIN_PAT" |
			curl "${args[@]}" -K - "$STELLA_URL$path")
	else
		status=$(curl "${args[@]}" "$STELLA_URL$path")
	fi
	case $status in
	2*) cat "$WORK/response.json" ;;
	*) die "$method $path failed with HTTP $status" ;;
	esac
}

step "building stellad and the eval driver"
(cd "$REPO_ROOT" && mise run build)
# Outside dist/bin on purpose: mise run build clears that directory, and the
# driver must survive the rebuild the next loop iteration performs.
(cd "$REPO_ROOT" && go build -o "$AGENT_BIN" ./cmd/stella-eval-agent)

step "starting a disposable testbed on the bridge backend"
export STELLA_SANDBOX_BACKEND=bridge
STELLA_EVAL_BRIDGE_DIR=${STELLA_EVAL_BRIDGE_DIR:-$(mktemp -d)}
export STELLA_EVAL_BRIDGE_DIR
# Both variables must be exported before testbed:start; the server reads them
# once, and exporting them afterwards silently leaves the backend on local.
mkdir -p "$(dirname "$TESTBED_LOG")"
(cd "$REPO_ROOT" && mise run testbed:start) >"$TESTBED_LOG" 2>&1 &
TESTBED_PID=$!
TESTBED_STARTED=1

deadline=$((SECONDS + READY_TIMEOUT))
until curl -fsS "$STELLA_URL/api/status" 2>/dev/null |
	python3 -c 'import json,sys; sys.exit(0 if json.load(sys.stdin).get("sandbox_backend")=="bridge" else 1)' 2>/dev/null; do
	kill -0 "$TESTBED_PID" 2>/dev/null || die "the testbed exited early; see $TESTBED_LOG"
	[ "$SECONDS" -lt "$deadline" ] || die "the testbed did not report sandbox_backend=bridge within ${READY_TIMEOUT}s; see $TESTBED_LOG"
	sleep 2
done

step "provisioning the provider, model, and a provisioning token"
# mise prefixes every line with the task name, so the anchor is anywhere on
# the line, not at its start.
CREDS=$(sed -n 's/^.*Credentials: //p' "$TESTBED_LOG" | head -1)
[ -n "$CREDS" ] && [ -f "$CREDS" ] || die "the testbed printed no credentials-file path; see $TESTBED_LOG"
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
cost = {
    "input": float(os.environ.get("EVAL_COST_INPUT", "0.20")),
    "output": float(os.environ.get("EVAL_COST_OUTPUT", "1.20")),
    "cacheRead": float(os.environ.get("EVAL_COST_CACHE_READ", "0.02")),
    "cacheWrite": float(os.environ.get("EVAL_COST_CACHE_WRITE", "0.25")),
}
json.dump({
    "id": os.environ["PROVIDER_ID"],
    "type": os.environ["PROVIDER_TYPE"],
    "name": "Eval gateway",
    "enabled": True,
    "api_key": os.environ["OPENAI_API_KEY"],
    "base_url": os.environ["OPENAI_BASE_URL"],
    "models": {os.environ["MODEL_ID"]: {"enabled": True, "cost": cost}},
}, open(sys.argv[1], "w"))
PY
api POST /api/providers bearer "$WORK/provider.json" >/dev/null

python3 - "$WORK/provisioning.json" <<'PY'
import datetime, json, sys
expires = datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(hours=6)
json.dump({"name": "eval-loop", "expires_at": expires.strftime("%Y-%m-%dT%H:%M:%SZ")},
          open(sys.argv[1], "w"))
PY
# A PAT gets 403 here; this one needs the interactive session cookie.
api POST /api/admin/provisioning-tokens cookie "$WORK/provisioning.json" >"$WORK/token.json"
STELLA_EVAL_ADMIN_TOKEN=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["token"])' "$WORK/token.json")
export STELLA_EVAL_ADMIN_TOKEN
export STELLA_EVAL_MODEL=$MODEL
export STELLA_EVAL_AGENT_BIN=$AGENT_BIN

step "running Harbor: $source_kind"
echo "    job: $JOB"
# The first run of a task pays the image pull; Harbor has no separate prefetch.
"${harbor_cmd[@]}" --print-config >"$WORK/config.json"
"${harbor_cmd[@]}"

step "report"
uv run --project "$HARBOR_DIR" python -m stella_harbor.report "$JOB"
if [ -n "$AGAINST" ]; then
	step "comparing against $AGAINST (this run is the candidate)"
	uv run --project "$HARBOR_DIR" python -m stella_harbor.compare "$JOB" "$AGAINST" || true
fi

: >"$WORK/args.txt"
[ ${#HARBOR_ARGS[@]} -eq 0 ] || printf '%s\n' "${HARBOR_ARGS[@]}" >"$WORK/args.txt"
TASKSET_PATH=$([ "$source_kind" = "taskset $TASKSET" ] && echo "${TASKSET#"$REPO_ROOT"/}" || echo "")
export TASKSET_PATH JOB
python3 - "$MANIFEST" "$WORK/config.json" "$WORK/args.txt" <<'PY'
import datetime, hashlib, json, os, subprocess, sys
from urllib.parse import urlsplit

config = json.load(open(sys.argv[2]))
tasks = sorted({t for d in config.get("datasets", []) for t in (d.get("task_names") or [])})
git = lambda *a: subprocess.run(["git", *a], capture_output=True, text=True).stdout.strip()
json.dump({
    "created_at": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
    "job": os.path.basename(os.environ["JOB"]),
    "commit": git("rev-parse", "HEAD"),
    "dirty": bool(git("status", "--porcelain")),
    "taskset": os.environ["TASKSET_PATH"] or None,
    "task_names": tasks,
    # Canonical over the sorted, newline-joined, dataset-qualified names, so a
    # taskset file and an identical explicit list hash the same.
    "task_hash": "sha256:" + hashlib.sha256("\n".join(tasks).encode()).hexdigest(),
    "k": config.get("n_attempts", 1),
    "concurrency": config.get("n_concurrent_trials", 4),
    "model": os.environ["MODEL"],
    # Host only: the path can carry a deployment id and the key never leaves.
    "gateway_host": urlsplit(os.environ["OPENAI_BASE_URL"]).hostname,
    "harbor_args": open(sys.argv[3]).read().split(),
}, open(sys.argv[1], "w"), indent=2)
PY
step "manifest: $MANIFEST"
