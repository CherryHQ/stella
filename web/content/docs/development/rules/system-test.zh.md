---
title: 系统测试
description: 以子进程方式在嵌入式 PostgreSQL 上通过 TCP 启动真实 stellad 的系统测试套件。
---

系统测试套件以子进程方式启动真实的 `stellad` 二进制，通过 TCP（HTTP 与 SSE）驱动它，
后端是嵌入式 PostgreSQL 集群，并用脚本化的 fake Anthropic provider 代替真实模型。它验证
单进程 Go 测试无法触达的接缝（seam）：进程启动与迁移、真实 HTTP 认证、面向客户端的 SSE 传输、
跨请求流程，以及异步 worker（goal 派发器及其 River 任务）。整个套件位于 `test/system/` 下的
`system` 构建标签内，所以普通的 `go test ./...` 永远不会发现它。

## 测试分层

在能证明该行为的最低层做测试。每上升一层，运行成本与保持确定性的成本都更高，因此只有当下层
够不到该接缝时才向上爬。

| 层级           | 运行内容                                                        | 命令                   | 浏览器 |
| -------------- | --------------------------------------------------------------- | ---------------------- | ------ |
| **进程内集成** | Go 测试，进程内，针对真实 Postgres —— 无服务器子进程            | `mise run test`        | 否     |
| **系统**       | 真实 `stellad` 子进程 + 嵌入式 PostgreSQL，脚本化 fake provider | `mise run system-test` | 否     |
| **浏览器 E2E** | 完整用户路径 `browser → API → DB`                               | 见 `web-ui-test.md`    | 是     |

若要用 `curl` 加数据库断言手动、探索式地驱动一个运行中的服务器，见 `api-test.md`；系统测试
套件就是那套子进程覆盖的自动化、可重复形式。

## 运行套件

```bash
mise run system-test
```

该任务依赖 `pg:runtime:download` 和 `build`，随后运行
`go test -tags system -count=1 -timeout 15m ./test/system/...`。它会：

- 若嵌入式 PostgreSQL runtime 尚未安装则下载；
- 构建 `dist/bin/stellad`（套件执行的是这个二进制，绝不是 `go run`）；
- 启动一个绑定真实回环 TCP 端口的服务器子进程，后端是一个由该子进程自行迁移的嵌入式
  PostgreSQL 集群；
- 按顺序运行各 journey，然后拆除子进程与集群。

## 支持的平台

套件只在嵌入式 PostgreSQL runtime 已发布的平台上运行；该平台集合由 `internal/db`
拥有，套件绝不复制这份清单。在其他任何主机上，`skipUnsupportedHost` 会在占用任何资源之前
跳过套件 —— 不是失败。已发布的平台：

- **linux/amd64** 与 **linux/arm64**，对应 Debian/Ubuntu runtime 源 `bookworm`、`noble`、
  `trixie`；
- **macOS arm64**。

在不支持的开发主机上，可将 `STELLA_DATABASE_URL` 指向一个带 `pg_search` 与 `pgvector` 的
外部 PostgreSQL 来手动运行服务器，或为该平台提 issue。Tag Release workflow 固定使用受支持的
Ubuntu runner，并调用 `mise run system-test`；若该 runner 将来变得不受支持，它的 runtime 下载
依赖会在套件执行前失败，因此发布流程不会把“不支持平台”的 skip 误算成 pass。

## 套件架构

`TestSystem` 在整个运行期间拥有唯一的服务器子进程及其数据库。各 journey 是**有序子测试**，
绝不使用 `t.Parallel()`，因此同一个共享服务器与同一个共享数据库按顺序服务它们全部：

- `readiness` —— 子进程迁移了交给它的数据库、绑定了 TCP 监听、并报告 ready。
- `startup_and_auth` —— bootstrap 注册与 session 认证后的访问。
- `chat_sse` —— 一次端到端 chat 轮次，以实时 SSE 流的方式消费。
- `chat_disconnect_resume` —— 在 turn 中途断开发消息的初始 stream，通过只读 events
  stream 重连，回放前半段并继续完成，且不发起第二次模型请求。
- `image_history` —— 上传图片通过 fake provider 完成 baseline 渲染和当前回答，随后持久化为
  canonical media 与该精确 baseline；下一次回答请求不含像素、只投影 baseline，并可通过鉴权
  历史接口逐字节加载原图。
- `view_image_tool_history` —— fake 回答模型调用生产 `view_image` tool 查看上传的 PNG；tool image 经由
  fake baseline VLM，在同一 tool loop 的后续回答中仍携带 pixels，持久化为 canonical tool
  history，并在下一用户回合只投影 baseline。
- `tool_smoke_canary` —— 一次 Code Mode 调用串起三个内置 tool，经 HTTP 与 SSE 传输，
  每个子 tool 各自回报自己的结果帧，并通过 `tools.search` 分页读出守护进程装配出的
  tool catalog。内置 tool 面的覆盖是一个进程内门禁（`cmd/stellad` 的
  `TestToolSmoke`），以严格集合等式闭合，并有三条写明的协议例外；这条 journey
  只证明它外面的那层传输，绝不该长成第二份覆盖清单。
- `chat_provider_error` —— 一次失败的模型调用以带内 error 帧的方式出现在发送流上，随后是
  finish 与 [DONE]——该轮次绝不挂起。
- `webhook_sync_persistent` —— 两次无认证 capability 调用同步返回 fake-model 输出，并跨请求
  复用同一个持久 Webhook session。
- `goal_lifecycle` —— 一个 Goal 从创建被派发器的异步 worker 驱动到自主验收。
- `github_webhook_compatibility` —— 一个 GitHub 风格的 JSON push 投递通过无 cookie jar 的普通个人 Webhook
  发送，收到异步 `202`，并且其原始 payload 恰好一次、完整地抵达 fake model。
- `scheduler_one_time_job_survives_forced_restart` —— 持久化一个未来触发的一次性 chat job，在到期前
  强杀服务器，再由连接同一数据库的替代进程恰好执行并退役该 job 一次。
- `graceful_drain` —— 在一个轮次仍在途中时发送 SIGTERM：`/readyz` 从 ready 翻转，attach 与
  send 观察者迅速断开，服务端持有的 turn 在 accepted-work drain 内持久化完整回复，进程以 0
  退出。它最后运行，因为会消费掉共享服务器。

`startup_and_auth` 还覆盖个人访问令牌（PAT）的 bearer 生命周期：一个 session 铸造出一个
PAT，仅凭该令牌即可用其所有者当前的权限认证普通 API 路由，撤销后同一个 bearer 会 fail closed。

每个 fixture（provider、agent、user、goal）都以 harness 的 `runID` 作用域隔离，因此没有任何
journey 依赖另一个 journey 的业务数据 —— 唯一的复用是共享的 bootstrap 用户与 cookie jar。
共享 HTTP 客户端**没有超时**；每个请求（含 SSE）必须自带 `context` deadline。
`TestHarnessEarlyExit` 是一个独立的顶层测试，证明启动过程中夭折的子进程会被快速检测，且既不
需要 PostgreSQL 也不需要 runtime。

子进程的环境是显式白名单，而非开发者继承来的环境，因此本地的 `STELLA_*`/`OTEL_*`/`AUTH_*`
设置无法泄漏进来、使运行变得不确定。

## fake Anthropic provider

没有任何模型流量离开主机：fake 是测试进程内的 `httptest.Server`，子进程之所以能访问它，仅仅
是因为测试创建的 provider 的 `base_url` 就是 fake 的回环地址。因此 fake 记录的每一个请求，
就是系统发出的每一个模型请求。

fake **绝不根据 prompt 文案分支** —— 只有稳定的结构化字段（model、tool 名、`goal_control` 的
action 枚举，以及 fake 自己植入、又在 tool result 里回到它手上的 marker）才选择响应，所以普通
的 prompt 改动永远不会变成系统测试失败。它有两种脚本模式：

- **FIFO 轮次**（`enqueueText`）—— 一个按到达顺序回放的有序队列；由 `chat_sse`、
  `image_history` 和 `view_image_tool_history` 使用。未脚本化的请求会让测试失败。
- **goal_control 变体匹配**（`enqueueGoalControl`）—— 响应按服务器向该 attempt 广告的
  `goal_control` action（`decompose`、`submit`）作键，按该稳定字段而非到达顺序匹配；由
  `goal_lifecycle` 使用。

清理阶段会在任何脚本化响应未被消费时让测试失败，从而捕获"系统实际发起的模型调用比 journey
假设的更少"这种情况。

### Code Mode 下如何够到 goal_control

Code Mode 是唯一的 tool 路径，因此 `goal_control` 是**冷** tool：它不出现在请求的 tool 列表
里，面向 provider 的 tool surface 里没有任何东西能把 decomposition 轮次和 execution 轮次区分
开。prompt 与 history 确实不同，但那是 fake 绝不能分支的文案。判别维度并没有消失，只是搬了家
—— 每个 attempt 的 `goal_control` schema 现在经 code catalog 抵达模型 —— 所以 fake 分两步
从那里取：

1. **探针。** 一个全新的 Goal 轮次会得到一个 `code` 调用，返回
   `tools.describe("goal_control").inputSchema.properties.action.enum`。这一步没有任何终止性
   动作，因此该 attempt 必然会再问一次。
2. **Stage。** 回答探针的那次请求带着这个枚举，fake 取其中非 `fail` 的 action，发出该 stage
   的 `tools.invoke("goal_control", …)` 调用，并**在此刻**把该 stage 记为已请求 —— 为什么不能
   等，见下面的陷阱。

fake 只读自己在脚本返回值里植入的 marker，绝不读 prompt 文案，所以匹配字段依然是 action 枚举，
普通的 prompt 改动依然不会让套件变红。

### Goal trailing-turn 陷阱

一个 Goal attempt 的 agent tool loop 可能在终止性的 `goal_control` 调用之后**再打一次竞态的
tool-result 后续调用**，因此每个 attempt 的 `/v1/messages` 调用次数是不确定的。这是竞态，不是
purpose 的属性：任何 attempt 都可能有、也可能没有这一轮，实测两种都出现过（实测那次
`goal_lifecycle` 里两个 attempt 都没有，序列为 `<call>, decompose, <call>, submit`）。

由此有两个后果。其一，这正是 goal 模式按 action 枚举而非到达顺序作键的原因：每个 stage 只发
一次，而后续轮次会得到一个无害的 `end_turn` 文本，使循环终止而不消费另一个 stage 的脚本。
其二，fake 必须在**发出某个 stage 的脚本时**就把它记为已请求，绝不能等脚本的 marker 回来 ——
attempt 就终止在那次调用上，marker 可能根本不会到达。

断言应是"所有脚本已消费且无未脚本请求"，绝不断言精确调用次数。

## 诊断

服务器日志写到仓库内的
`dist/logs/system-test/server-<runid>-g<generation>-a<attempt>.log`（运行结束后仍保留），
因此 restart journey 会保留每一代进程，失败信息也始终能指向一个真实文件。失败会附带相关日志
的尾部；goal 与 scheduler restart journey 还会额外 dump 持久化行和 fake 请求日志，使卡住的
异步任务无需重跑即可诊断。

## 何时新增一个系统测试 journey

只有当改动涉及**下层够不到的接缝**时才新增 journey：进程启动、真实 HTTP 认证、面向客户端的
SSE（或其他流式）传输、跨多个请求的流程，或异步 worker（派发器、调度器、River 任务）。其余
一切 —— 纯逻辑、单 handler 行为、进程内可达的数据库不变量 —— 都应在最低足够层的进程内 Go
测试里做。一个需要改动生产代码、需要扩展不受支持主机、或引入任何外部网络依赖的新 journey，
会在落地前重启设计评审。
