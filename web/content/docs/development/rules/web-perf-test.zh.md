---
title: Web 性能测试
description: 使用 test/perf/ 性能测量工具对 Web UI 做可复现的优化前后对比。
---

关于 Web UI 的任何性能结论都必须是实测的前后对比差值，不能靠肉眼感觉。
`test/perf/` 下的测量工具负责产出这个差值：它在一个**独立的 stellad 实例**
（`~/.stella-perf`，端口 25911）上运行确定性的聊天场景，配合**假 Anthropic
provider**（固定回复内容、固定流式节奏），保证两次运行之间的唯一变量就是被测
代码。结果以带 git commit 标记的 JSON 文件写入 `test/perf/results/`。

本规则只讲何时测、如何测。具体机制——场景细节、指标定义、环境变量——见
`test/perf/README.md`。UI 功能验证用 `web-ui-test.md`；纯后端行为用
`api-test.md`。

## 何时测量

- 开始任何优化之前：先采集 baseline，否则"提升"无法证伪。
- 每个优化阶段完成后：一个阶段一个 commit，实测差值写进 commit message 和
  PR 描述。
- 怀疑性能回归时：对可疑 commit 与其父 commit 分别测量，而不是对着 diff 争论。

## 工作流

服务器从 `web/static/dist` 嵌入 Web UI，前端构建过期会导致悄悄测到旧代码——
`setup` 前必须两个都重新构建：

```bash
cd web && vp build && cd .. && go build -o dist/bin/stellad ./cmd/stellad

./test/perf/run.sh setup                 # 独立服务器 + 假 provider + 种子数据
./test/perf/run.sh measure baseline      # 渲染场景 -> results/baseline.json
./test/perf/run.sh measure-load baseline # 加载场景 -> results/load-baseline.json
# ...修改代码，重建 UI 和二进制...
./test/perf/run.sh teardown && ./test/perf/run.sh setup
./test/perf/run.sh measure after
./test/perf/run.sh teardown
```

`measure` 覆盖渲染路径场景（长历史、流式帧时间、逐键输入开销）；
`measure-load` 覆盖加载路径场景（1000 条消息的会话、内嵌多个数 MB 图片和
PDF 附件的历史）。对比时取**多次重复的中位数**，不看单次结果，且只在同一台
机器的运行之间对比：

```bash
jq -r '.runs | [.[].streaming.avgFrameMs] | sort | .[length/2|floor]' \
  test/perf/results/baseline.json
```

## 解读结果

- 绝对数值依赖机器；只有同一台机器上的前后差值有意义。
- 认识下限：120 Hz 屏幕上平均帧时间的下限约 8.3 ms。某阶段推不动已到下限的
  指标，不代表没有收益——看随历史长度增长的指标（最大帧时间、逐键开销）。
- localhost 会掩盖网络成本。涉及传输量或缓存的结论，用 Resource Timing API
  读 `transferSize`（缓存命中的 304 约 0.3 KB，完整响应体是数 MB），不要看
  墙钟加载时间。

## 坑（每一条都踩过）

- **Chrome 会限流隐藏标签页。** 隐藏或被遮挡的窗口里 rAF 停摆，所有帧指标归
  零。工具会把窗口置前并保持可见；如果 `frames` 为 0，检查
  `document.visibilityState`。
- **`content-visibility` 下 `innerText` 不可信。** Chrome 会把被跳过的（视口
  外）行从 `innerText` 中剔除。断言要用 `textContent`——注意它拼接节点时没有
  分隔符，锚定换行或边界的正则会产生歧义，改用计数匹配。
- **`performance.memory` 是进程级且 GC 前的读数。** 跨运行攀升通常是 GC 惰
  性，不是泄漏。泄漏定论需要 CDP：连接浏览器 profile 目录下
  `DevToolsActivePort` 文件里的端口，调用 `HeapProfiler.collectGarbage`，确认
  旧视图中被 `WeakRef` 钉住的节点已回收、多周期后 GC 后堆保持平稳。
- **种子数据必须顺序写入。** 向同一个 session 并发 POST turn 会在服务端竞争
  并静默丢失。
- **加载场景的种子数据跨 label 复用是刻意的。** 加载场景从不修改会话，所有
  label 面对完全相同的数据；渲染场景因为流式回复会追加消息，每个 label 重新
  播种。

## 相关

- `test/perf/README.md` — 场景机制、指标、环境变量。
- `web-ui-test.md` — 功能性浏览器验证（同样的 `tap` 工具，不同目的）。
- `system-test.md` — 测试分层；性能工具在分层之外，是测量工具而非通过/失败
  门禁。
