---
title: Web performance testing
description: Reproducible before/after measurement of the web UI with the Playwright perf projects.
---

Every performance claim about the web UI must be a measured before/after delta, not an eyeballed impression. The Playwright projects under `test/e2e/perf/` run deterministic scenarios against the disposable testbed and the fake Anthropic provider. Results are JSON files under `test/e2e/perf/results/`; generated measurements are ignored except for the checked-in baseline files.

Functional verification belongs in `web-ui-test.md`; backend behavior belongs in `api-test.md`.

## When to measure

- Before starting an optimization, capture a baseline. Without it, the improvement is unfalsifiable.
- After each optimization phase, keep one phase per commit and record its measured delta in the commit message and PR description.
- When a regression is suspected, measure the suspect commit against its parent instead of arguing from the diff.

## Workflow

The testbed and fake provider are started and stopped by the perf tasks. Rebuild first so the embedded UI is current:

```bash
mise run build
mise run perf:measure -- baseline
mise run perf:measure-load -- baseline
# after changing code:
mise run build
mise run perf:measure -- after
mise run perf:measure-load -- after
```

`perf:measure` covers `long-history`, `streaming`, and `typing`. `perf:measure-load` covers `huge-load` and `files-load`, including a 1000-message session and image/PDF fixtures. Set `REPS`, `HUGE_TURNS`, `IMG_COUNT`, `PDF_COUNT`, `PERF_STREAM_CHUNKS`, and `PERF_STREAM_INTERVAL_MS` for controlled runs. Render fixtures are seeded sequentially per label; load fixtures are persisted and reused across labels so the data is identical.

Result files use `results/<label>.json` for render measurements and `results/load-<label>.json` for load measurements. Important render fields include `longHistory.domNodes`, `longHistory.jsHeapMB`, `longHistory.fcpMs`, `streaming.durationMs`, `streaming.avgFrameMs`, `streaming.p95FrameMs`, `streaming.maxFrameMs`, `streaming.jankFramesPct`, and `typing.avgKeyMs`/`p95KeyMs`. Load fields include `hugeLoad.resCount`, `hugeLoad.resTotalKB`, `hugeLoad.resLastEndMs`, `hugeLoad.domNodes`, `hugeLoad.jsHeapMB`, `hugeLoad.fullMountMs`, and `filesLoad.total`, `filesLoad.loaded`, `filesLoad.resTotalKB`, and `filesLoad.settleMs`.

Compare medians across repetitions on the same machine, never a single run:

```bash
jq -r '[.runs[].streaming.avgFrameMs] | sort | .[length/2|floor]' test/e2e/perf/results/baseline.json
```

## Interpreting results

- Absolute numbers are machine-dependent; only a before/after delta on one machine is meaningful.
- Know the floor: on a 120 Hz display, average frame time bottoms out around 8.3 ms. A metric at the floor may still improve in max frame time or per-keystroke cost as history grows.
- localhost hides network cost. For transfer and caching claims, inspect `filesLoad.resTotalKB` and resource timing, not only wall-clock load time. Cached resources can report only a few KB against a multi-megabyte first load.
- Heap is diagnostic, not a verdict. Compare post-GC behavior across cycles before calling a change a leak.

## Gotchas (each one has bitten)

- **Chrome throttles hidden tabs.** Hidden or occluded tabs throttle rAF and can make `streaming.frames` zero or distort `avgFrameMs`. The perf project keeps the browser visible; if frames are zero, check `document.visibilityState`.
- **`innerText` lies under `content-visibility`.** Virtualized rows may be excluded. Assertions use `textContent`; because it concatenates nodes without separators, count matches rather than relying on ambiguous newline or boundary regexes.
- **`performance.memory` is process-level and pre-GC.** Rising `longHistory.jsHeapMB` across runs may be lazy GC, not a leak. A leak verdict requires CDP `HeapProfiler.collectGarbage`, `WeakRef` checks for old-view nodes, and flat post-GC heap across cycles.
- **Seed fixtures sequentially.** Concurrent turn POSTs to one session race server-side and silently drop turns, corrupting `long-history` or `huge-load` metrics.
- **Fixtures are reused across labels on purpose.** `files-load` and `huge-load` do not mutate their sessions, so load data is reusable. Render scenarios reseed a fresh session per label because streaming appends to it.
- **Do not compare old harness output.** `test/perf` and its TAP results are retired. `results/baseline.json` and `results/load-baseline.json` are the first Playwright-harness baselines; pre-Playwright values are not comparable.
