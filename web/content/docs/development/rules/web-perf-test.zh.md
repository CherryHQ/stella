---
title: Web 性能测试
description: Stella 的 Playwright 性能测量。
---

性能 harness 位于 `test/e2e/perf/`，使用端口 `25777` 的一次性 testbed 和 `test/fakeanthropic/cmd`。结果写入被 gitignore 的 `test/e2e/perf/results/<label>.json` 与 `load-<label>.json`。

```bash
mise run perf:measure -- baseline
mise run perf:measure-load -- baseline
```

渲染场景是 `long-history`、`streaming`、`typing`，加载场景是 `huge-load`、`files-load`。可设置 `REPS`、`HUGE_TURNS`、`IMG_COUNT`、`PDF_COUNT`、`PERF_STREAM_CHUNKS`、`PERF_STREAM_INTERVAL_MS`。fixture 要顺序 seed，并在不同 label 间复用加载 fixture。只在同一台机器上比较中位数，不比较单次结果。

浏览器窗口必须可见，隐藏 Chrome tab 会节流渲染和 rAF。虚拟化历史使用 `textContent`，不要依赖 `innerText`。localhost 会隐藏网络成本，传输测量应查看 resource timing。`results/baseline.json` 和 `results/load-baseline.json` 是本 Playwright harness 的首批 baseline。任何 Playwright 之前的 baseline 都不可比较。
