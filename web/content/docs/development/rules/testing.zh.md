---
title: 测试
description: Stella Go、系统、浏览器、API 和性能测试的运行、分层与落点规则。
---

Stella 将测试覆盖放在三个代码位置，并提供两个主要测试命令、一个性能命令和 eval 命令。选择能触及 seam 的最低层；层级越高，运行和保持确定性的成本越高。

## 怎么跑

```bash
mise run test
mise run test:e2e
mise run test:e2e -- --grep-invert @model  # 可选，不跑真实模型回合
mise run perf -- <label>                  # render + load 性能测量
mise run eval:loop                        # Harbor 行为评估
```

`mise run test` 运行包内 Go 测试、前端单测和子进程 system suite。system 部分会下载支持的内置 PostgreSQL runtime、构建 `dist/bin/stellad`，并在不支持的平台获取资源前跳过。`mise run test:e2e` 运行一次性 testbed 上的全部功能 Playwright spec。grep-invert 命令排除标题带 `@model` 的测试；默认 retries 为 0，确实不稳定的 spec 必须在自身声明 retry。

`mise run perf` 一次运行 `test/e2e/perf/render.spec.ts` 和 `load.spec.ts`，使用 testbed 内嵌的假模型。`mise run testbed:start` 和 `mise run testbed:stop` 保留给手工 API/浏览器探索。`test:web`、coverage、race 和 `eval:*` 是专门命令，不是额外的功能测试层。

## 写在哪

每个行为有三个可能落点：

- **包内 Go 测试**：确定性逻辑、单个 handler、DB invariant，以及不需要真实服务进程的工具行为，使用 `mise run test`。
- **`test/system`**：只有下层无法触及进程启动、真实 HTTP 认证、SSE/流式传输、跨请求流程或异步 worker 时，才添加 system journey。它们针对一个真实 `stellad` 子进程和内置 PostgreSQL 运行有序 journey，由 `mise run test` 统一执行。
- **`test/e2e`**：浏览器/UI、实时 API、DB 断言、远程 MCP fixture 和功能用户路径。功能 spec 直接放在 `test/e2e/`；性能 spec 是例外，放在 `test/e2e/perf/`。

不要为了重复单测或浏览器断言而添加 system journey，也不要把性能测量写成功能 pass/fail 测试。

## testbed 怎么用

testbed 是一次性应用 fixture，不是 system harness 的进程生命周期验证器。它管理内置 PostgreSQL、真实 `stellad` 子进程、fixture 身份和清理，默认监听 `http://127.0.0.1:25777`。

```bash
mise run testbed:start
# 使用命令打印的凭据路径，但不要打印凭据内容
mise run testbed:stop
```

CLI 支持 `testbed start --fake-model`。库调用方式是：

```go
instance, err := testbed.Start(ctx, testbed.Options{
    RepoRoot: repoRoot,
    Port: 25777,
    Bootstrap: true,
    FakeModel: true,
})
defer instance.Stop()
```

`Instance` 提供 `BaseURL()`、`Credentials()`、`DatabaseURL()`、`Fake()`、`Stop()`、`Kill()`、`Restart()` 和 `LogTail(n)`。`Kill` 强杀整个自有进程组；`Restart` 保留临时 home、数据库 DSN 和 vault identity。system 测试通过 `Fake()` 在进程内 enqueue 脚本响应。perf setup 使用凭据文件中的假模型 URL，不再启动另一个 provider 进程。

凭据文件权限为 `0600`，包含 admin/user 身份、PAT、数据库 DSN，以及请求 `--fake-model` 时的 fake provider ID 和 base URL。只在内存中使用，绝不打印、提交或暴露 `.env`。checked-in 浏览器 spec 使用 `test/e2e/lib/fixtures.ts` 的 `admin`、`user`、`db` 和 `loginAsAdmin`；认证 JSON/SSE 调用使用 `test/e2e/lib/api.ts`，行级断言使用 `db`。

使用 API 创建 API 能创建的状态，例如 draft goal、agent、MCP server、provider 配置和 scheduler job。直接 DB 伪造只用于视觉验证，行为测试必须走真实路径。手工测试每个退出路径都要停止 testbed。禁止使用 `~/.stella-dev`、端口 `25678`、手工 fixture 账号、手工 CDP 注册或外部 PostgreSQL。

## Gotchas：system

- journey 有序运行，不得使用 `t.Parallel()`，因为共享一个服务和数据库。
- HTTP client 没有全局 timeout；每个请求，包括 SSE，都需要 context deadline。
- fixture 名称用 `runID` 隔离，只有 bootstrap 身份和 cookie jar 复用。
- 子进程使用显式环境 allowlist，本地 `STELLA_*`、`OTEL_*`、`AUTH_*` 不得泄漏。
- 假模型只根据稳定请求字段分支，不读 prompt 文案。普通 FIFO 用 `enqueueText`，Goal 用 `enqueueGoalControl` 匹配 `goal_control` action enum。
- Code Mode 下 `goal_control` 是 cold tool；假模型先 probe `tools.describe("goal_control")`，再依据 action enum stage `tools.invoke("goal_control", ...)`。
- Goal attempt 在终态 `goal_control` 后可能竞争性地再发 `/v1/messages`。断言脚本已消费且没有未脚本化请求，不断言精确 provider 调用数。
- 日志保存在 `dist/logs/system-test/`，重启 journey 保留各 generation 日志；启动早退必须快速发现并报告日志路径。
- 只有进程启动、真实认证、流式传输、跨请求流程或异步 worker 才新增 system journey，其余放在更低层。

## Gotchas：浏览器 E2E

- `test:e2e` 使用端口 `25777` 的一次性 testbed 和一个 worker；`@model` 只是标题标签，不是 Playwright project。
- 每个有意义的动作后断言 text/role、必要时的 URL 和错误状态；验证写入时同时断言 `admin` 或 `db`。
- `codegen` 只用于发现交互，不用于创建 fixture；稳定 locator 要复制进 checked-in spec。
- 导航或 rerender 后 snapshot 和 locator 假设会失效，应重新定位。
- 普通 setup 使用 API fixture；只有注册 UI 是被测对象时才使用浏览器注册。
- 隐藏 Chrome tab 会节流渲染和 rAF。虚拟化或 `content-visibility` 历史使用 `textContent`，文本无分隔符时要计数。
- 登录表单使用 `username`、`password` placeholder，密码至少 8 个字符，通常重定向到 `/agents`。

## Gotchas：API 与 DB

- 某些 shell 中 `GID` 是整数变量，`GID="019f..."` 可能产生 `bad math expression`；UUID 使用 `GOAL` 等变量名。
- Session 位于 `ctx_conversation`（`session_id`、`kind`、`archived`），不是 `session` 表；`ON DELETE RESTRICT` 可能让回滚写入留下 orphan。
- 存储和序列化使用 UTC，`created_at` 与 UTC run-start 比较。
- 后台工作使用 `stella_goal_tick` 等 River queue；断言前给 dispatcher 几个 tick，约每次 2 秒。
- API 响应只覆盖 happy path；对 orphan、archived、计数和 FK invariant 查询 PostgreSQL，并报告 expected/actual。
- goal lifecycle + agent review：POST 带 leaf judgment contract 的 model-configured goal，轮询 `/api/goals/<id>/children` 到 `acceptance_state=passed`，通常约 30 秒，不能用固定 sleep 替代有界轮询。
- 断言 `goal_control` 日志顺序 `decompose` -> `submit` -> `verdict` 且 `pass=true`，并断言本次 run 的 session 都是 `archived=false`。rollback disposal 由 `TestReview_DisposesSessionOnRollback` 和 `TestDisposeOnRollback` 确定性覆盖。

## Gotchas：性能

- 优化前、每个阶段后、怀疑回归时都要测量；同一台机器比较多次运行的中位数，不比较单次。
- render 输出为 `test/e2e/perf/results/<label>.json`，load 输出为 `load-<label>.json`。关键字段包括 `longHistory.domNodes/jsHeapMB/fcpMs`、`streaming.durationMs/avgFrameMs/p95FrameMs/maxFrameMs/jankFramesPct`、`typing.avgKeyMs/p95KeyMs`、`hugeLoad.resCount/resTotalKB/resLastEndMs/domNodes/jsHeapMB/fullMountMs`、`filesLoad.total/loaded/resTotalKB/settleMs`。
- 绝对数值依赖机器；120 Hz 屏幕平均帧时间下限约 8.3 ms，max frame 和 typing cost 仍可能改善。
- localhost 隐藏网络成本；传输/缓存结论要检查 resource timing 和 `filesLoad.resTotalKB`。
- 隐藏 tab 可能让 `streaming.frames` 为 0，先检查 `document.visibilityState`。
- `performance.memory` 是进程级且发生在 GC 前；泄漏结论需要 CDP `HeapProfiler.collectGarbage`、`WeakRef` 和多轮 post-GC heap。
- fixture turn 要顺序 seed；对同一 session 并发 POST 可能静默丢 turn。
- load fixture 跨 label 有意复用；render fixture 要重新 seed，因为 streaming 会追加 session。
- 旧 `test/perf` TAP 输出不可比较；checked-in 的 `baseline.json` 与 `load-baseline.json` 是第一批 Playwright baseline。
