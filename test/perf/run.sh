#!/usr/bin/env bash
# Chat perf harness: measures the web UI chat under three reproducible
# scenarios against a scratch stellad + deterministic fake model provider.
#
#   long-history : open a ~200-message session; load long tasks, DOM size, heap
#   streaming    : one paced streamed reply (fakeprovider: N chunks × T ms);
#                  frame times + long tasks while streaming
#   typing       : 120 synthetic keystrokes into the composer; per-key cost
#
# Usage:
#   ./test/perf/run.sh setup        # build fake+server, start both, seed fixture
#   ./test/perf/run.sh measure LBL  # run all scenarios REPS times -> results/LBL.json
#   ./test/perf/run.sh teardown     # stop scratch server + fakeprovider
#
# The scratch instance lives in $PERF_HOME (default ~/.stella-perf) with its own
# embedded Postgres; it never touches your dev instance. The web UI must be
# freshly built before `setup` (cd web && vp build) because stellad embeds it.
set -euo pipefail

REPO="$(cd "$(dirname "$0")/../.." && pwd)"
PERF_DIR="$REPO/test/perf"
PERF_HOME="${PERF_HOME:-$HOME/.stella-perf}"
FAKE_PORT="${FAKE_PORT:-25901}"
SRV_PORT="${SRV_PORT:-25911}"
URL="http://localhost:$SRV_PORT"
REPS="${REPS:-5}"
SEED_TURNS="${SEED_TURNS:-100}"
EMAIL="perf@example.com"
PASSWORD="perf-admin-pw"
JAR="$PERF_HOME/cookies.txt"
FIXTURE="$PERF_HOME/fixture.json"
SENTINEL="END-OF-STREAM"
TA="textarea"

log() { echo "[perf] $*" >&2; }
die() { log "FATAL: $*"; exit 1; }

api() { # api METHOD PATH [JSON]
  local method="$1" path="$2" body="${3:-}"
  if [ -n "$body" ]; then
    curl -sf -b "$JAR" -X "$method" "$URL$path" -H "Content-Type: application/json" -d "$body"
  else
    curl -sf -b "$JAR" -X "$method" "$URL$path"
  fi
}

ev() { # ev JS -> stdout (tap browser evaluate)
  tap browser evaluate "$1"
}

inject_metrics() {
  ev "$(cat "$PERF_DIR/metrics.js")" >/dev/null
}

# ---------------------------------------------------------------- setup

start_fake() {
  if lsof -iTCP:"$FAKE_PORT" -sTCP:LISTEN -n -P >/dev/null 2>&1; then
    log "fakeprovider already on :$FAKE_PORT"; return
  fi
  mkdir -p "$PERF_HOME"
  (cd "$REPO" && go build -o "$PERF_HOME/fakeprovider" ./test/perf/fakeprovider)
  nohup "$PERF_HOME/fakeprovider" -port "$FAKE_PORT" >"$PERF_HOME/fakeprovider.log" 2>&1 &
  echo $! > "$PERF_HOME/fakeprovider.pid"
  sleep 0.5
  log "fakeprovider started on :$FAKE_PORT"
}

start_server() {
  if lsof -iTCP:"$SRV_PORT" -sTCP:LISTEN -n -P >/dev/null 2>&1; then
    log "stellad already on :$SRV_PORT"; return
  fi
  [ -x "$REPO/dist/bin/stellad" ] || die "dist/bin/stellad missing; run: cd web && vp build && mise run build"
  # Reuse the dev home's extracted PG runtime if present (binaries only; the
  # data dir stays under $PERF_HOME/home).
  local pg_runtime=""
  pg_runtime=$(find "$HOME/.stella/pg-runtime" -path '*/postgres/bin/pg_ctl' 2>/dev/null \
    | head -1 | sed 's|/postgres/bin/pg_ctl$||')
  STELLA_HOME="$PERF_HOME/home" \
  STELLA_POSTGRES_RUNTIME="${STELLA_POSTGRES_RUNTIME:-$pg_runtime}" \
  nohup "$REPO/dist/bin/stellad" serve --port "$SRV_PORT" \
    >"$PERF_HOME/stellad.log" 2>&1 &
  echo $! > "$PERF_HOME/stellad.pid"
  for _ in $(seq 1 60); do
    curl -sf "$URL/readyz" >/dev/null 2>&1 && { log "stellad ready on :$SRV_PORT"; return; }
    sleep 1
  done
  die "stellad did not become ready; see $PERF_HOME/stellad.log"
}

auth() {
  mkdir -p "$PERF_HOME"
  # First registered user is admin; register is a no-op failure if it exists.
  curl -s -X POST "$URL/api/auth/local/register" -H "Content-Type: application/json" \
    -d "{\"name\":\"perfadmin\",\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\",\"confirm_password\":\"$PASSWORD\"}" >/dev/null || true
  curl -sf -c "$JAR" -X POST "$URL/api/auth/local/login" -H "Content-Type: application/json" \
    -d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}" >/dev/null \
    || die "login failed"
  log "authenticated as $EMAIL"
}

seed_fixture() { # seed_fixture [force] — force reseeds a fresh session
  if [ "${1:-}" = force ]; then
    rm -f "$FIXTURE"
  fi
  if [ -f "$FIXTURE" ]; then
    log "fixture exists: $(cat "$FIXTURE")"; return
  fi
  local pid="perf-fake"
  api POST /api/providers "{\"id\":\"$pid\",\"type\":\"anthropic\",\"name\":\"$pid\",\"enabled\":true,\"api_key\":\"perf-not-a-secret\",\"base_url\":\"http://127.0.0.1:$FAKE_PORT\"}" >/dev/null \
    || log "provider create failed (may already exist)"
  local agent_id session_id
  agent_id=$(api GET /api/agents | jq -r '.agents[]? | select(.name=="perf-agent") | .id' | head -1)
  if [ -z "$agent_id" ]; then
    agent_id=$(api POST /api/agents "{\"name\":\"perf-agent\",\"model\":\"$pid/claude-sonnet-4-6\",\"enabled\":true}" | jq -r .id)
  fi
  [ -n "$agent_id" ] && [ "$agent_id" != null ] || die "agent create failed"
  session_id=$(api POST "/api/agents/$agent_id/sessions" '{"kind":"chat"}' | jq -r .id)
  [ -n "$session_id" ] && [ "$session_id" != null ] || die "session create failed"
  log "seeding $SEED_TURNS turns into session $session_id ..."
  for i in $(seq 1 "$SEED_TURNS"); do
    curl -sf -b "$JAR" -X POST "$URL/api/agents/$agent_id/sessions/$session_id/messages" \
      -H "Content-Type: application/json" \
      -d "{\"parts\":[{\"type\":\"text\",\"text\":\"seed turn $i\"}]}" >/dev/null \
      || die "seed turn $i failed"
    [ $((i % 20)) -eq 0 ] && log "  seeded $i/$SEED_TURNS"
  done
  jq -n --arg a "$agent_id" --arg s "$session_id" '{agent_id:$a, session_id:$s}' > "$FIXTURE"
  log "fixture ready: $(cat "$FIXTURE")"
}

# ---------------------------------------------------------------- browser

# Bring the tap-managed Chrome frontmost: Chrome throttles rAF and rendering
# in hidden/occluded tabs, which zeroes the frame metrics.
activate_browser() {
  local pid
  pid=$(pgrep -f 'MacOS/Google Chrome .*tap/browser/profiles/default' | head -1)
  [ -n "$pid" ] || return 0
  osascript -e "tell application \"System Events\" to set frontmost of (first process whose unix id is $pid) to true" 2>/dev/null || true
  sleep 0.5
  ev "document.visibilityState" | grep -q visible \
    || log "WARNING: page still hidden; frame metrics will be zero"
}

browser_login() {
  # --show: measurements are meaningless in a hidden tab (Chrome throttles
  # rAF, timers, and rendering), so the window must be visible and frontmost.
  tap browser open "$URL/login" --show >/dev/null
  sleep 1
  if ev "location.pathname" | grep -qv login; then
    log "browser already authenticated"; return
  fi
  ev "(() => {
    const inputs = document.querySelectorAll('input');
    const set = (el, v) => {
      const s = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
      s.call(el, v); el.dispatchEvent(new Event('input', { bubbles: true }));
    };
    set(inputs[0], '$EMAIL'); set(inputs[1], '$PASSWORD');
    document.querySelector('form button[type=submit], button[type=submit]').click();
    return 'submitted';
  })()" >/dev/null
  sleep 2
  ev "location.pathname" | grep -qv login || die "browser login failed"
  log "browser logged in"
}

open_session() {
  local agent_id session_id
  agent_id=$(jq -r .agent_id "$FIXTURE")
  session_id=$(jq -r .session_id "$FIXTURE")
  tap browser open "$URL/agents/$agent_id/sessions/$session_id" >/dev/null
  # Wait until the transcript has content (seeded replies mention "cache").
  for _ in $(seq 1 60); do
    # textContent, not innerText: rows use content-visibility:auto, and Chrome
    # excludes skipped (off-screen) content from innerText.
    if ev "document.body.textContent.includes('cache key derived')" | grep -q true; then return; fi
    sleep 0.5
  done
  die "session transcript did not render"
}

# Scroll the transcript to top repeatedly (each scroll loads one older page)
# until the first seeded turn is mounted, so the full history is in the DOM
# before measuring.
load_full_history() {
  inject_metrics
  local done=""
  for _ in $(seq 1 80); do
    ev "window.__perf.scrollTopOnce()" >/dev/null
    sleep 0.5
    # Count turns instead of matching "seed turn 1": textContent has no element
    # separators, so adjacent digit-leading text (timestamps) merges into the
    # turn number and any single-turn regex is ambiguous.
    if ev "(document.body.textContent.match(/seed turn /g)||[]).length >= $SEED_TURNS" | grep -q true; then
      done=1; break
    fi
  done
  [ -n "$done" ] || die "full history did not load (seed turn 1 never appeared)"
  ev "window.__perf.scrollBottom()" >/dev/null
  sleep 0.5
}

# ---------------------------------------------------------------- scenarios

scenario_long_history() {
  open_session
  sleep 2   # let auto-fill pagination settle
  load_full_history
  ev "JSON.stringify(window.__perf.loadStats())"
}

scenario_streaming() {
  inject_metrics
  local nonce="n$(date +%s)"
  ev "window.__perf.start()" >/dev/null
  ev "window.__perf.send('$TA', 'stream $nonce')" >/dev/null
  local waited=0
  while [ "$waited" -lt 120 ]; do
    sleep 2; waited=$((waited + 2))
    if ev "window.__perf.streamDone('$SENTINEL $nonce')" | grep -q true; then
      ev "JSON.stringify(window.__perf.stop())"
      return
    fi
  done
  die "stream did not finish in 120s"
}

scenario_typing() {
  inject_metrics
  local text
  text=$(printf 'the quick brown fox %.0s' $(seq 1 6))   # 120 chars
  local out
  out=$(ev "JSON.stringify(window.__perf.typeInto('$TA', '$text'))")
  ev "window.__perf.clearComposer('$TA')" >/dev/null
  echo "$out"
}

measure() {
  local label="${1:?usage: run.sh measure <label>}"
  command -v jq >/dev/null || die "jq required"
  auth
  # Fresh session per measurement label so baseline and after runs start from
  # an identical 200-message history.
  seed_fixture force
  browser_login
  activate_browser
  local out="$PERF_DIR/results/$label.json"
  local runs="[]"
  for rep in $(seq 1 "$REPS"); do
    log "=== rep $rep/$REPS ==="
    local lh st ty
    lh=$(scenario_long_history)
    log "long-history: $lh"
    st=$(scenario_streaming)
    log "streaming:    $st"
    # Fresh reload so typing measures a quiet page with full history mounted.
    open_session
    load_full_history
    ty=$(scenario_typing)
    log "typing:       $ty"
    runs=$(jq -n --argjson acc "$runs" \
      --argjson lh "$lh" --argjson st "$st" --argjson ty "$ty" \
      '$acc + [{longHistory:($lh|fromjson), streaming:($st|fromjson), typing:($ty|fromjson)}]')
  done
  jq -n --arg label "$label" --arg date "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg commit "$(git -C "$REPO" rev-parse --short HEAD)" \
    --argjson reps "$REPS" --argjson runs "$runs" \
    '{label:$label, date:$date, commit:$commit, reps:$reps, runs:$runs}' > "$out"
  log "wrote $out"
}

teardown() {
  for f in stellad.pid fakeprovider.pid; do
    if [ -f "$PERF_HOME/$f" ]; then
      kill "$(cat "$PERF_HOME/$f")" 2>/dev/null || true
      rm -f "$PERF_HOME/$f"
    fi
  done
  log "stopped scratch processes (data in $PERF_HOME preserved)"
}

case "${1:-}" in
  setup)    start_fake; start_server; auth; seed_fixture ;;
  measure)  shift; measure "$@" ;;
  teardown) teardown ;;
  *) echo "usage: $0 {setup|measure <label>|teardown}"; exit 2 ;;
esac
