---
title: 系统测试
description: 在内置 PostgreSQL 上通过 TCP 启动真实 stellad 的子进程系统测试套件。
---

系统测试启动真实 `stellad` 二进制子进程，通过 TCP、HTTP 和 SSE 驱动它，使用内置 PostgreSQL，并由 `test/fakeanthropic/` 中共享的脚本化 Anthropic 假模型代替真实模型。它覆盖单进程 Go 测试无法触及的 seam：进程启动与迁移、真实 HTTP 认证、SSE 传输、跨请求流程和异步 worker。套件位于 `test/system/` 的 `system` build tag 下，因此普通 `go test ./...` 不会发现它。

## 测试分层

选择能够证明行为的最低层。层级越高，运行和保持确定性的成本越高，只有下层无法触及目标 seam 时才升级。

| 层级           | 覆盖内容                             | 启动器         | 命令                               | 浏览器 |
| -------------- | ------------------------------------ | -------------- | ---------------------------------- | ------ |
| **进程内集成** | Go 测试与实时 DB                     | Go test        | `mise run test`                    | 否     |
| **系统测试**   | 真实子进程、TCP、SSE、重启和 worker  | system harness | `mise run system-test`             | 否     |
| **浏览器 E2E** | UI、API、DB 和功能流程               | `test/testbed` | `mise run test:e2e`                | 是     |
| **性能**       | testbed 和假模型上的 Playwright 测量 | perf project   | `mise run perf:measure -- <label>` | 是     |
| **Eval**       | Agent 行为基准                       | Harbor         | `mise run eval:loop`               | 否     |

手工 API 探索见 `api-test.md`，浏览器行为见 `web-ui-test.md`，性能测量见 `web-perf-test.md`。

## 运行套件

```bash
mise run system-test
```

该任务依赖 `pg:runtime:download` 和 `build`，然后运行 `go test -tags system -count=1 -timeout 15m ./test/system/...`。需要时下载内置 PostgreSQL，构建 `dist/bin/stellad`，绝不使用 `go run`；启动一个 loopback TCP 子进程，让它迁移交给它的数据库，按顺序运行 journey，最后关闭子进程和数据库集群。

套件只在内置 PostgreSQL 已发布的平台运行：支持的 Debian/Ubuntu runtime 对应的 linux/amd64、linux/arm64，以及 macOS arm64。不支持的平台在获取资源前跳过。不要在套件中独立扩展平台列表，应以 `internal/db` 为准。

## Suite architecture

`TestSystem` 为整个运行持有一个服务器子进程和一个数据库。journey 是有序子测试，绝不使用 `t.Parallel()`，共享服务按顺序运行：

- `readiness`：迁移、TCP 监听和就绪状态。
- `startup_and_auth`：注册、session 认证、PAT 创建与撤销。
- `chat_sse`：通过实时 SSE 消费一次聊天回合。
- `chat_disconnect_resume`：断开后通过只读 events stream 重放，不产生第二次模型请求。
- `agent_provider_credentials`：Agent 使用加密 provider 覆盖，并验证轮换和删除。
- `image_history` 与 `view_image_tool_history`：上传媒体、规范化历史、baseline VLM 和生产 `view_image` 工具循环。
- `tool_smoke_canary`：Code Mode 调用内置工具，并通过 `tools.search` 分页读取目录；工具覆盖仍由进程内测试负责。
- `chat_provider_error`：provider 错误以内嵌错误帧结束并发送 `[DONE]`，不能挂起。
- `webhook_sync_persistent` 与 `github_webhook_compatibility`：能力调用、持久 session 和 GitHub 格式异步投递。
- `goal_lifecycle`：dispatcher 与 River worker 驱动异步分解、执行和验收。
- `scheduler_one_time_job_survives_forced_restart`：强制重启后，持久化任务在同一数据库上恰好执行一次。
- `graceful_drain`：SIGTERM 排空进行中的回合、切换 readiness、持久化已接受工作并干净退出。它消耗共享服务，因此必须最后运行。

每个 fixture（provider、agent、user、goal）都使用 harness 的 `runID` 隔离；只有 bootstrap 身份和 cookie jar 复用。HTTP client 没有 timeout，每个请求，包括 SSE，都必须使用 context deadline。独立的 `TestHarnessEarlyExit` 验证启动期间子进程死亡能被快速发现，无需 PostgreSQL。

子进程环境使用显式 allowlist。不要让本地 `STELLA_*`、`OTEL_*` 或 `AUTH_*` 设置泄漏进来，造成非确定性。system harness 自己负责进程组、强杀、重启身份和启动失败检测，不依赖 testbed，因为 testbed 是一次性应用 fixture，不是生命周期验证器。

## The fake Anthropic provider

没有模型流量离开本机。`test/fakeanthropic` 是测试进程内的 `httptest.Server`；测试创建的 provider 将其 loopback 地址设为 `base_url`，因此每个记录的请求就是系统实际发出的模型请求。

假模型只根据稳定的请求字段分支，从不读取 prompt 文案。FIFO 脚本（`enqueueText`）按顺序服务聊天和媒体 journey；`enqueueGoalControl` 根据 provider 宣布的 `goal_control` action enum 匹配 `decompose` 和 `submit`，不依赖到达顺序。未脚本化请求会立即失败，清理时若仍有未消费脚本也会失败。浏览器 perf 使用 `test/fakeanthropic/cmd` 命令包装器，系统测试直接使用普通 Go package。

### Reaching goal_control under Code Mode

Code Mode 是唯一的工具路径，因此 `goal_control` 是 cold tool，不会出现在 provider-facing tool list 中。区分信息转移到了每次 attempt 的 Code catalog。假模型分两步取得它：

1. **Probe**：新的 Goal 回合先返回 `code` 调用，执行 `tools.describe("goal_control")`，读取 `inputSchema.properties.action.enum`。不会执行终态操作，因此该 attempt 必须继续询问。
2. **Stage**：下一次请求带有该 enum。假模型选择非 `fail` action，服务对应的 `tools.invoke("goal_control", …)` 脚本，并在服务脚本时记录阶段。

假模型只读取自己在脚本返回值中植入的 marker，从不读取 prompt 文案，因此普通 prompt 修改不会改变匹配规则。

### Goal trailing-turn gotcha

终态 `goal_control` 调用后，Goal 工具循环可能竞争性地再发一个 `/v1/messages` 请求。任意 attempt 都可能有或没有这个尾随回合，因此不能断言精确调用次数。假模型按 action enum 匹配，为尾随请求返回无害的 `end_turn`，并在服务阶段脚本时记录，而不是等待 marker 返回，因为 attempt 可能在 marker 到达前结束。

应断言所有脚本都已消费且没有未脚本化请求，不要断言 provider 调用的精确数量。

## Diagnostics

日志保存在 `dist/logs/system-test/server-<runid>-g<generation>-a<attempt>.log`，运行结束后仍保留。重启 journey 会保留每个进程 generation。失败输出包含相关日志尾部；goal 和 scheduler-restart journey 还会输出持久化行和假模型请求日志，使异步卡住的问题无需重跑即可诊断。

## When to add a system-test journey

只有下层无法触及的 seam 才新增 journey：进程启动、真实 HTTP 认证、SSE 或其他面向客户端的流式传输、跨多个请求的流程，或 dispatcher、scheduler、River job 等异步 worker。纯逻辑、单 handler 行为和进程内可触及的 DB invariant 应放在 Go 测试。浏览器功能放在 `test/e2e`，性能场景放在 `test/e2e/perf`。需要改生产代码、扩展不支持的平台或访问外部网络的 journey，落地前必须重新进行设计评审。
