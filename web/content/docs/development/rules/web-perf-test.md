---
title: Web performance testing
description: Reproducible before/after measurement of the web UI with the perf harness in test/perf/.
---

Every performance claim about the web UI must be a measured before/after delta,
not an eyeballed impression. The harness in `test/perf/` produces that delta: it
runs deterministic chat scenarios against a **scratch stellad instance**
(`~/.stella-perf`, port 25911) with a **fake Anthropic provider** (fixed reply
content, fixed streaming cadence), so two runs differ only by the code under
test. Results are JSON files tagged with the git commit under
`test/perf/results/`.

This rule covers when and how to measure. Mechanics — scenario details, metric
definitions, env knobs — live in `test/perf/README.md`. For functional
verification of the UI, use `web-ui-test.md` instead; for backend-only
behavior, `api-test.md`.

## When to measure

- Before starting any optimization: capture a baseline, or the "improvement"
  is unfalsifiable.
- After each optimization phase: one phase per commit, with its measured delta
  in the commit message and the PR description.
- When a perf regression is suspected: measure the suspect commit against its
  parent instead of arguing from the diff.

## Workflow

The server embeds the web UI from `web/static/dist`, so a stale frontend build
silently measures old code — always rebuild both before `setup`:

```bash
cd web && vp build && cd .. && go build -o dist/bin/stellad ./cmd/stellad

./test/perf/run.sh setup                 # scratch server + fake provider + fixture
./test/perf/run.sh measure baseline      # render scenarios -> results/baseline.json
./test/perf/run.sh measure-load baseline # load scenarios   -> results/load-baseline.json
# ...change code, rebuild UI + binary...
./test/perf/run.sh teardown && ./test/perf/run.sh setup
./test/perf/run.sh measure after
./test/perf/run.sh teardown
```

`measure` covers render-path scenarios (long-history, streaming frame times,
per-keystroke cost); `measure-load` covers load-path scenarios (a
1000-message session, a history embedding multi-megabyte images and PDF
chips). Compare **medians across reps**, never single runs, and only across
runs from the same machine:

```bash
jq -r '.runs | [.[].streaming.avgFrameMs] | sort | .[length/2|floor]' \
  test/perf/results/baseline.json
```

## Interpreting results

- Absolute numbers are machine-dependent; only the before/after delta on one
  machine is meaningful.
- Know the floor: on a 120 Hz display the average frame time bottoms out at
  ~8.3 ms. A phase that doesn't move an at-floor metric may still be a win on
  a metric that scales with history length (max frame, per-keystroke cost).
- localhost hides network cost. For transfer or caching claims, read
  `transferSize` from the Resource Timing API (a cached 304 shows ~0.3 KB
  against a full body's megabytes) instead of wall-clock load time.

## Gotchas (each one has bitten)

- **Chrome throttles hidden tabs.** rAF stops in hidden or occluded windows,
  zeroing every frame metric. The harness forces the window visible and
  frontmost; if `frames` comes back 0, check `document.visibilityState`.
- **`innerText` lies under `content-visibility`.** Chrome excludes
  skipped (off-screen) rows from `innerText`. Assert on `textContent` — and
  remember it concatenates nodes with no separators, so newline- or
  boundary-anchored regexes are ambiguous; count matches instead.
- **`performance.memory` is process-level and pre-GC.** Heap values climbing
  across runs usually mean lazy GC, not a leak. A leak verdict requires CDP:
  connect to the port in the browser profile's `DevToolsActivePort` file, call
  `HeapProfiler.collectGarbage`, and check that `WeakRef`-pinned nodes from
  the old view were collected and post-GC heap is flat across cycles.
- **Seed fixtures sequentially.** Concurrent turn POSTs to one session race
  server-side and silently drop turns.
- **Fixtures are reused across labels on purpose.** The load scenarios never
  mutate their sessions, so identical data backs every label; the render
  scenarios reseed a fresh session per label because streaming appends to it.

## Related

- `test/perf/README.md` — scenario mechanics, metrics, env knobs.
- `web-ui-test.md` — functional browser verification (same `tap` tooling,
  different purpose).
- `system-test.md` — the test-layer taxonomy; the perf harness sits outside
  it as a measurement tool, not a pass/fail gate.
