# Chat web UI perf harness

Measures the chat UI under three reproducible scenarios so optimizations can be
compared before/after. Everything runs against a **scratch stellad instance**
(`~/.stella-perf`, port 25911) with a **deterministic fake Anthropic provider**
(`fakeprovider/`, port 25901) — no real model calls, no effect on your dev
instance.

## Scenarios and metrics

| Scenario     | What happens                                                         | Key metrics                                   |
| ------------ | -------------------------------------------------------------------- | --------------------------------------------- |
| long-history | Open a seeded ~200-message session, scroll-load the full history     | `domNodes`, `jsHeapMB`, `bufferedLongTask*`   |
| streaming    | One streamed reply (default 1500 chunks × 10 ms) into that history   | `avgFrameMs`, `p95FrameMs`, `longTaskTotalMs` |
| typing       | 120 synthetic keystrokes into the composer with full history mounted | `avgKeyMs`, `p95KeyMs`, `maxKeyMs`            |

`measure-load <label>` runs two additional load-focused scenarios (3 reps by
default, results in `results/load-<label>.json`):

| Scenario   | What happens                                                                                | Key metrics                                        |
| ---------- | ------------------------------------------------------------------------------------------- | -------------------------------------------------- |
| huge-load  | Open a `HUGE_TURNS` (default 500-turn / 1000-message) session, then scroll-mount everything | `fcpMs`, `resLastEndMs`, `fullMountMs`, `domNodes` |
| files-load | Open a session whose history embeds `IMG_COUNT` ~1.9 MB images + `PDF_COUNT` PDF chips      | `resTotalKB`, `resLastEndMs`, `loaded`/`total`     |

The huge and files fixtures are seeded once and **reused across labels** (the
load scenarios never mutate them), so before/after runs see identical data.
Note the browser cache also persists across labels: `files-load` rep 1 after a
server restart is a cold view; later reps measure the repeat-view path.

Determinism: message history is seeded through the real API against the fake
provider (fixed markdown reply per turn), the streamed reply is fixed content at
a fixed cadence, and each `measure` run seeds a **fresh session** so every label
starts from identical state.

## Usage

```bash
# once per checkout state: rebuild UI + binary the server will embed
cd web && vp build && cd .. && go build -o dist/bin/stellad ./cmd/stellad

./test/perf/run.sh setup            # start fakeprovider + scratch stellad, seed fixture
./test/perf/run.sh measure baseline # 5 reps -> test/perf/results/baseline.json
# ...apply optimizations, rebuild UI + binary, restart scratch server...
./test/perf/run.sh teardown && ./test/perf/run.sh setup
./test/perf/run.sh measure after
./test/perf/run.sh teardown
```

Env knobs: `REPS` (default 5), `SEED_TURNS` (default 100 → 200 messages),
`HUGE_TURNS` (500), `IMG_COUNT` (10), `PDF_COUNT` (3), `REPS_LOAD` (3),
`PERF_STREAM_CHUNKS` / `PERF_STREAM_INTERVAL_MS` (fakeprovider pacing),
`PERF_HOME`, `FAKE_PORT`, `SRV_PORT`.

## Requirements & caveats

- `tap` CLI (drives the browser), `jq`, macOS (`osascript` is used to bring the
  measurement window frontmost).
- **The Chrome window must stay visible during `measure`** — Chrome throttles
  rAF and rendering in hidden/occluded tabs, which zeroes the frame metrics.
  Don't cover the window while a run is in progress.
- Numbers are machine-dependent; only compare results captured on the same
  machine under similar load.
- First run: the scratch home needs a vault key
  (`./dist/bin/stellad vault keygen` → `~/.stella-perf/home/.env` as
  `STELLA_VAULT_KEY=...`). The embedded Postgres runtime is reused from
  `~/.stella/pg-runtime` if present.
