---
title: Web performance testing
description: Playwright performance measurements for Stella.
---

Perf uses the Playwright project under `test/e2e/perf/`, a disposable testbed on port `25777`, and `test/fakeanthropic/cmd`. It writes ignored JSON results to `test/e2e/perf/results/<label>.json` and `load-<label>.json`.

```bash
mise run perf:measure -- baseline
mise run perf:measure-load -- baseline
```

Render scenarios are `long-history`, `streaming`, and `typing`. Load scenarios are `huge-load` and `files-load`. Set `REPS`, `HUGE_TURNS`, `IMG_COUNT`, `PDF_COUNT`, `PERF_STREAM_CHUNKS`, and `PERF_STREAM_INTERVAL_MS` for controlled runs. Seed fixture turns sequentially and reuse load fixtures across labels. Compare medians on the same machine, never a single run.

The browser must remain visible because hidden Chrome tabs throttle rendering and rAF. `textContent` is safer than `innerText` for virtualized history. localhost hides network cost, so inspect resource timing when measuring transfer. `results/baseline.json` and `results/load-baseline.json` are the first baselines from this Playwright harness. Any pre-Playwright baseline is not comparable.
