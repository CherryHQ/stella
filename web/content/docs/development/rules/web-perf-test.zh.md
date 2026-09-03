---
title: Web 性能测试
description: 使用 Playwright perf project 对 Web UI 做可复现的前后测量。
---

所有 Web UI 性能结论都必须来自可测量的 before/after 差异，不能凭肉眼判断。`test/e2e/perf/` 下的 Playwright project 使用一次性 testbed 和 Anthropic 假模型运行确定性场景。结果是 `test/e2e/perf/results/` 下的 JSON；生成的测量结果默认被 gitignore，只有 checked-in baseline 例外。

功能验证见 `web-ui-test.md`，后端行为见 `api-test.md`。

## When to measure

- 开始任何优化前先采集 baseline，否则改进无法证伪。
- 每个优化阶段之后测量；每个阶段保持一个 commit，并把差异写入 commit message 和 PR 描述。
- 怀疑性能回归时，测量可疑 commit 与 parent 的差异，不要只争论 diff。

## Workflow

perf task 会管理 testbed 和假模型的启动与停止。先重新构建，确保嵌入的 UI 是最新的：

```bash
mise run build
mise run perf:measure -- baseline
mise run perf:measure-load -- baseline
# 修改代码后：
mise run build
mise run perf:measure -- after
mise run perf:measure-load -- after
```

`perf:measure` 覆盖 `long-history`、`streaming`、`typing`；`perf:measure-load` 覆盖 `huge-load`、`files-load`，包括 1000 消息 session 和 image/PDF fixture。可设置 `REPS`、`HUGE_TURNS`、`IMG_COUNT`、`PDF_COUNT`、`PERF_STREAM_CHUNKS`、`PERF_STREAM_INTERVAL_MS`。每个 label 的 render fixture 要顺序 seed；load fixture 会持久化并在不同 label 间复用，保证数据相同。

结果文件是 render 的 `results/<label>.json`，load 的 `results/load-<label>.json`。重要 render 字段包括 `longHistory.domNodes`、`longHistory.jsHeapMB`、`longHistory.fcpMs`、`streaming.durationMs`、`streaming.avgFrameMs`、`streaming.p95FrameMs`、`streaming.maxFrameMs`、`streaming.jankFramesPct` 和 `typing.avgKeyMs`/`p95KeyMs`。load 字段包括 `hugeLoad.resCount`、`hugeLoad.resTotalKB`、`hugeLoad.resLastEndMs`、`hugeLoad.domNodes`、`hugeLoad.jsHeapMB`、`hugeLoad.fullMountMs`，以及 `filesLoad.total`、`filesLoad.loaded`、`filesLoad.resTotalKB`、`filesLoad.settleMs`。

只在同一台机器上比较多次运行的中位数，不比较单次运行：

```bash
jq -r '[.runs[].streaming.avgFrameMs] | sort | .[length/2|floor]' test/e2e/perf/results/baseline.json
```

## Interpreting results

- 绝对数值取决于机器；只有同一台机器上的 before/after 差异有意义。
- 了解下限：120 Hz 屏幕的平均帧时间下限约为 8.3 ms。处于下限的指标可能不变，但 max frame 或随历史增长的单键成本仍可能改善。
- localhost 会隐藏网络成本。传输和缓存结论要检查 `filesLoad.resTotalKB` 与 resource timing，不能只看 wall-clock；缓存资源可能只有几 KB，而首次加载是数 MB。
- Heap 只是诊断信息，不是结论。判定泄漏前，应比较多轮 GC 后的行为。

## Gotchas (each one has bitten)

- **Chrome 会节流隐藏 tab。** 隐藏或被遮挡的 tab 会节流 rAF，使 `streaming.frames` 变成 0 或扭曲 `avgFrameMs`。perf project 保持浏览器可见；若 frames 为 0，先检查 `document.visibilityState`。
- **`content-visibility` 下 `innerText` 会骗人。** 虚拟化行可能被排除。断言使用 `textContent`；它不会插入分隔符，因此应计数，不要依赖含糊的换行或边界正则。
- **`performance.memory` 是进程级且发生在 GC 前。** `longHistory.jsHeapMB` 上升可能只是 lazy GC，不一定是泄漏。泄漏结论需要 CDP `HeapProfiler.collectGarbage`、用 `WeakRef` 检查旧 view 节点，并确认多轮 post-GC heap 平稳。
- **fixture 要顺序 seed。** 对同一 session 并发 POST turn 会在服务端竞争，并静默丢 turn，污染 `long-history` 或 `huge-load` 指标。
- **fixture 跨 label 复用是有意的。** `files-load` 和 `huge-load` 不修改 session，因此 load 数据可以复用；render 场景每个 label 重新 seed，因为 streaming 会追加消息。
- **不要比较旧 harness 输出。** `test/perf` 及 TAP 结果已退役。`results/baseline.json` 和 `results/load-baseline.json` 是 Playwright harness 的首批 baseline，Playwright 之前的数据不可比较。
