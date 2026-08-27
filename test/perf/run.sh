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
HUGE_TURNS="${HUGE_TURNS:-500}"
IMG_COUNT="${IMG_COUNT:-10}"
PDF_COUNT="${PDF_COUNT:-3}"
REPS_LOAD="${REPS_LOAD:-3}"
EMAIL="perf@example.com"
PASSWORD="perf-admin-pw"
JAR="$PERF_HOME/cookies.txt"
FIXTURE="$PERF_HOME/fixture.json"
FIXTURE_HUGE="$PERF_HOME/fixture-huge.json"
FIXTURE_FILES="$PERF_HOME/fixture-files.json"
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

ensure_agent() { # -> echoes agent_id
  local pid="perf-fake"
  api POST /api/providers "{\"id\":\"$pid\",\"type\":\"anthropic\",\"name\":\"$pid\",\"enabled\":true,\"api_key\":\"perf-not-a-secret\",\"base_url\":\"http://127.0.0.1:$FAKE_PORT\"}" >/dev/null 2>&1 \
    || true
  local agent_id
  agent_id=$(api GET /api/agents | jq -r '.agents[]? | select(.name=="perf-agent") | .id' | head -1)
  if [ -z "$agent_id" ]; then
    agent_id=$(api POST /api/agents "{\"name\":\"perf-agent\",\"model\":\"$pid/claude-sonnet-4-6\",\"enabled\":true}" | jq -r .id)
  fi
  [ -n "$agent_id" ] && [ "$agent_id" != null ] || die "agent create failed"
  echo "$agent_id"
}

new_session() { # new_session AGENT_ID -> echoes session_id
  local session_id
  session_id=$(api POST "/api/agents/$1/sessions" '{"kind":"chat"}' | jq -r .id)
  [ -n "$session_id" ] && [ "$session_id" != null ] || die "session create failed"
  echo "$session_id"
}

post_turn() { # post_turn AGENT SESSION TEXT — one user message + synchronous fake reply
  jq -n --arg t "$3" '{parts:[{type:"text",text:$t}]}' \
    | curl -sf -b "$JAR" -X POST "$URL/api/agents/$1/sessions/$2/messages" \
        -H "Content-Type: application/json" -d @- >/dev/null
}

seed_fixture() { # seed_fixture [force] — force reseeds a fresh session
  if [ "${1:-}" = force ]; then
    rm -f "$FIXTURE"
  fi
  if [ -f "$FIXTURE" ]; then
    log "fixture exists: $(cat "$FIXTURE")"; return
  fi
  local agent_id session_id
  agent_id=$(ensure_agent)
  session_id=$(new_session "$agent_id")
  log "seeding $SEED_TURNS turns into session $session_id ..."
  for i in $(seq 1 "$SEED_TURNS"); do
    post_turn "$agent_id" "$session_id" "seed turn $i" || die "seed turn $i failed"
    [ $((i % 20)) -eq 0 ] && log "  seeded $i/$SEED_TURNS"
  done
  jq -n --arg a "$agent_id" --arg s "$session_id" '{agent_id:$a, session_id:$s}' > "$FIXTURE"
  log "fixture ready: $(cat "$FIXTURE")"
}

# Huge-history fixture: $HUGE_TURNS turns (2x messages). Reused across labels —
# the load scenarios never mutate the session, so the same data keeps
# before/after comparable and skips a slow reseed.
seed_huge_fixture() {
  if [ -f "$FIXTURE_HUGE" ]; then
    log "huge fixture exists: $(cat "$FIXTURE_HUGE")"; return
  fi
  local agent_id session_id
  agent_id=$(ensure_agent)
  session_id=$(new_session "$agent_id")
  # Sequential on purpose: concurrent turn POSTs to one session race in the
  # server and silently drop turns (observed: 500 parallel posts -> 250 turns).
  log "seeding $HUGE_TURNS turns into huge session $session_id ..."
  local i
  for i in $(seq 1 "$HUGE_TURNS"); do
    post_turn "$agent_id" "$session_id" "seed turn $i" || die "huge seed turn $i failed"
    [ $((i % 50)) -eq 0 ] && log "  seeded $i/$HUGE_TURNS"
  done
  jq -n --arg a "$agent_id" --arg s "$session_id" '{agent_id:$a, session_id:$s}' > "$FIXTURE_HUGE"
  log "huge fixture ready: $(cat "$FIXTURE_HUGE")"
}

# Files fixture: $IMG_COUNT ~1.9MB noise PNGs + $PDF_COUNT small PDFs uploaded
# to the session workspace, one user message per file referencing it the same
# way the composer does ([file: <path>]). Reused across labels.
seed_files_fixture() {
  if [ -f "$FIXTURE_FILES" ]; then
    log "files fixture exists: $(cat "$FIXTURE_FILES")"; return
  fi
  local agent_id session_id srcdir
  agent_id=$(ensure_agent)
  session_id=$(new_session "$agent_id")
  srcdir="$PERF_HOME/assets-src"
  mkdir -p "$srcdir"
  log "generating + uploading $IMG_COUNT images and $PDF_COUNT pdfs ..."
  for i in $(seq 1 "$IMG_COUNT"); do
    [ -f "$srcdir/img-$i.png" ] || python3 - "$srcdir/img-$i.png" <<'PYEOF'
import os, struct, sys, zlib
w = h = 800  # random RGB compresses to ~1.9MB: a realistic photo-sized payload
raw = b"".join(b"\x00" + os.urandom(w * 3) for _ in range(h))
def chunk(t, d):
    return struct.pack(">I", len(d)) + t + d + struct.pack(">I", zlib.crc32(t + d) & 0xFFFFFFFF)
png = (b"\x89PNG\r\n\x1a\n"
       + chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 2, 0, 0, 0))
       + chunk(b"IDAT", zlib.compress(raw, 0))
       + chunk(b"IEND", b""))
open(sys.argv[1], "wb").write(png)
PYEOF
    local path
    path=$(curl -sf -b "$JAR" -X POST \
      "$URL/api/agents/$agent_id/sessions/$session_id/workspace/upload" \
      -F "file=@$srcdir/img-$i.png" | jq -r .path)
    [ -n "$path" ] && [ "$path" != null ] || die "image upload $i failed"
    post_turn "$agent_id" "$session_id" "[file: $path]
please look at image $i" || die "image message $i failed"
  done
  for i in $(seq 1 "$PDF_COUNT"); do
    [ -f "$srcdir/doc-$i.pdf" ] || printf '%%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 612 792]>>endobj\ntrailer<</Root 1 0 R>>\n%%%%EOF\n' > "$srcdir/doc-$i.pdf"
    local path
    path=$(curl -sf -b "$JAR" -X POST \
      "$URL/api/agents/$agent_id/sessions/$session_id/workspace/upload" \
      -F "file=@$srcdir/doc-$i.pdf" | jq -r .path)
    [ -n "$path" ] && [ "$path" != null ] || die "pdf upload $i failed"
    post_turn "$agent_id" "$session_id" "[file: $path]
please read document $i" || die "pdf message $i failed"
  done
  jq -n --arg a "$agent_id" --arg s "$session_id" '{agent_id:$a, session_id:$s}' > "$FIXTURE_FILES"
  log "files fixture ready: $(cat "$FIXTURE_FILES")"
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

open_fixture() { # open_fixture FIXTURE_FILE READY_JS — fresh navigation, wait until READY_JS is true
  local agent_id session_id
  agent_id=$(jq -r .agent_id "$1")
  session_id=$(jq -r .session_id "$1")
  tap browser open "$URL/agents/$agent_id/sessions/$session_id" >/dev/null
  for _ in $(seq 1 120); do
    if ev "$2" | grep -q true; then return; fi
    sleep 0.5
  done
  die "fixture page did not become ready ($1)"
}

# ---------------------------------------------------------------- scenarios

# Initial page load of a $HUGE_TURNS-turn session: paint/fetch timings for the
# first screen, then time-to-full-mount via scroll-up paging.
scenario_huge_load() {
  open_fixture "$FIXTURE_HUGE" "document.body.textContent.includes('cache key derived')"
  sleep 2   # let auto-fill + rendering settle
  inject_metrics
  local initial
  initial=$(ev "JSON.stringify(Object.assign(window.__perf.navStats('/messages'), window.__perf.loadStats()))")
  local mounted="" i
  for i in $(seq 1 300); do
    ev "window.__perf.scrollTopOnce()" >/dev/null
    sleep 0.3
    if ev "(document.body.textContent.match(/seed turn /g)||[]).length >= $HUGE_TURNS" | grep -q true; then
      mounted=$(ev "JSON.stringify({fullMountMs: +performance.now().toFixed(0), domNodes: document.querySelectorAll('*').length})")
      break
    fi
  done
  [ -n "$mounted" ] || die "huge history never fully mounted"
  jq -n --argjson a "$(echo "$initial" | jq fromjson)" --argjson b "$(echo "$mounted" | jq fromjson)" '$a + $b'
}

# Load of a session whose history embeds images + pdf chips: paint timings,
# per-image network cost, and time until every transcript image is decoded.
scenario_files_load() {
  open_fixture "$FIXTURE_FILES" "[...document.images].some(i => i.src.includes('file-content'))"
  inject_metrics
  # Walk to the top so lazy images outside the initial viewport get triggered.
  ev "window.__perf.scrollTopOnce()" >/dev/null
  local done="" i
  for i in $(seq 1 240); do
    if ev "(() => { const p = window.__perf.imgProgress(); return p.total >= $IMG_COUNT && p.loaded >= p.total; })()" | grep -q true; then
      done=1; break
    fi
    sleep 0.5
  done
  [ -n "$done" ] || die "transcript images never finished loading"
  ev "JSON.stringify(Object.assign(window.__perf.navStats('file-content'), window.__perf.imgProgress(), {settleMs: +performance.now().toFixed(0)}))"
}

scenario_long_history() {
  open_session
  sleep 2   # let auto-fill pagination settle
  load_full_history
  ev "JSON.stringify(window.__perf.loadStats())"
}

scenario_streaming() {
  inject_metrics
  local nonce
  nonce="n$(date +%s)"
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

measure_load() { # measure_load LBL -> results/load-LBL.json {hugeLoad, filesLoad} x REPS_LOAD
  local label="${1:?usage: run.sh measure-load <label>}"
  command -v jq >/dev/null || die "jq required"
  auth
  seed_huge_fixture
  seed_files_fixture
  browser_login
  activate_browser
  local out="$PERF_DIR/results/load-$label.json"
  local runs="[]"
  for rep in $(seq 1 "$REPS_LOAD"); do
    log "=== load rep $rep/$REPS_LOAD ==="
    local hg fl
    hg=$(scenario_huge_load)
    log "huge-load:  $hg"
    fl=$(scenario_files_load)
    log "files-load: $fl"
    runs=$(jq -n --argjson acc "$runs" --argjson hg "$hg" --argjson fl "$fl" \
      '$acc + [{hugeLoad:$hg, filesLoad:($fl|fromjson)}]')
  done
  jq -n --arg label "$label" --arg date "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg commit "$(git -C "$REPO" rev-parse --short HEAD)" \
    --argjson turns "$HUGE_TURNS" --argjson imgs "$IMG_COUNT" --argjson runs "$runs" \
    '{label:$label, date:$date, commit:$commit, huge_turns:$turns, img_count:$imgs, runs:$runs}' > "$out"
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
  measure-load) shift; measure_load "$@" ;;
  teardown) teardown ;;
  *) echo "usage: $0 {setup|measure <label>|measure-load <label>|teardown}"; exit 2 ;;
esac
