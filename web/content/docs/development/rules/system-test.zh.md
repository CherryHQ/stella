---
title: 系统测试
description: Stella 使用的五层测试体系。
---

选择能够覆盖 seam 的最低层。系统测试是进程边界层，使用内置 PostgreSQL、共享脚本化假模型，并通过 TCP 驱动真实 `stellad` 子进程。

| 层级       | 覆盖内容                             | 启动器         | 命令                               | 浏览器 |
| ---------- | ------------------------------------ | -------------- | ---------------------------------- | ------ |
| 进程内     | Go 测试与实时数据库                  | Go test        | `mise run test`                    | 否     |
| 系统       | 真实子进程、TCP、SSE、重启与 worker  | system harness | `mise run system-test`             | 否     |
| 浏览器 E2E | UI、API、DB 和功能流程               | `test/testbed` | `mise run test:e2e`                | 是     |
| 性能       | testbed + 假模型上的 Playwright 测量 | perf project   | `mise run perf:measure -- <label>` | 是     |
| Eval       | Agent 行为基准                       | Harbor         | `mise run eval:loop`               | 否     |

系统 harness 不使用 testbed，是因为它必须控制进程组、强制杀进程、重启时保持身份，并快速发现启动失败。testbed 是一次性应用 fixture，不是进程生命周期验证器。

只有进程启动、真实 HTTP 认证、流式传输、跨请求流程或异步 worker 才新增系统 journey。功能浏览器覆盖放在 `test/e2e`，性能场景放在 `test/e2e/perf`，其余放在进程内测试。
