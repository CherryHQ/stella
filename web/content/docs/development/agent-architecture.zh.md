---
title: Agent 架构
---

> 本页面面向修改 Stella agent runtime、session、memory、channel、scheduler、内部 delegate adapter 或 task system 的开发者。

Stella 的 agent 架构围绕一条规则拆分：

**调用方表达业务意图；`agent.Service` 选择 session policy；`session.Registry` 验证 session；`runtime.Runtime` 执行一轮对话；`memory.Provider` 存储和组装内容。**

```text
channel / server / scheduler / task / delegate
        |
        v
internal/agent.Service        业务意图 seam
        |
        +--> internal/agent/session.Registry   session 生命周期和策略
        |
        +--> internal/agent/runtime.Runtime    runner cache 和 turn 执行
                 |
                 v
              internal/memory.Provider         messages、summaries、profile、snapshots
```

旧的 `Pool` 形态混合了这些职责。新代码不要因为方便就把行为塞回某个调用方自己的路径里。

## 回合完成与截止时间

系统提示要求 agent 完成已授权操作，并在最终回复前核对每个交付要求。验证应能发现生成步骤中的错误；仅显示生成结果不足以验证正确性。这是模型指引，不保证任务一定通过检查。

Responses 适配器将工具参数和文本的流式增量与最终快照核对，只补发缺失的后缀。重复快照不会重复调用工具。快照冲突或缺少终止事件的流会报错，避免静默报告完成。

普通对话保留默认 30 分钟时限。可信沙箱可以提供绝对截止时间；Harbor bridge 用它将原始任务预算跨 HTTP 传入模型执行。显式对话时限和更早的父上下文截止时间仍会限制回合。预算从 bridge 发现前开始计算，服务端接受请求时不会重新计时。外部计时回合会在首次模型调用时收到剩余预算，已有的按耗时提醒继续生效。

## Module 职责

### `agent.Service`

`agent.Service` 是生产代码执行 agent 工作的 seam。调用方应该请求它做一件具体的事：

- 向一个已有 Web session 发送消息
- 用 Stella 派生的 key 执行非私有 channel/group chat
- 用 scheduler 派生的 session ID 执行定时任务
- 运行或恢复 delegate child session
- 在 resolved executor agent 名下创建 task session
- 解析 private user main session

`Service` 可以把这些意图翻译成 `session.Request`。边缘调用方不应该自己设置 `CreateIfMissing`、`AllowExactIDCreate` 或 `RequireKind`。

当前重要方法：

| Method                                                        | 用途                                                      | ID trust model                                                    |
| ------------------------------------------------------------- | --------------------------------------------------------- | ----------------------------------------------------------------- |
| `Chat`                                                        | 前台 chat，使用已有 session 或生成新 session              | caller-supplied `SessionID` 是 resume-only                        |
| `ChatForScheduler`                                            | scheduler 发起的 run                                      | 允许 exact-create，因为 `SessionID` 由 scheduler 派生             |
| `ResolvePrivateChannelSession` / `ResolveGroupChannelSession` | 只解析 private 或 group channel session，不执行 chat turn | trusted channel key，要求 `KindChat`；group id 拥有 group session |
| `NewSession`                                                  | HTTP/Web UI 创建 session                                  | 只能生成 ID                                                       |
| `MintTaskSession`                                             | task system 创建 worker session                           | 在 resolved executor agent 名下生成 ID                            |
| `Delegate`                                                    | 运行/恢复 delegate child session                          | caller/model supplied `SessionID` 是 resume-only                  |
| `ResolveMainSession`                                          | 解析或创建 private main session                           | 生成或提升 main session                                           |

### `session.Registry`

`session.Registry` 拥有 session 生命周期和策略：

- 创建和恢复 session record
- 验证 user 和 agent ownership
- 恢复时验证 kind
- 拒绝 archived session
- 解析 main session
- 列出 session 和 review candidates
- 通过 `MemoryScope` 把验证过的 `session.Info` 转成 `memory.Session`

`session.Request` 是低层 plumbing。它很灵活，因为 `Service` 和测试需要表达不同策略。生产边缘调用方不应该直接构造它。

### `runtime.Runtime`

`runtime.Runtime` 执行已经验证过的 session。它不创建 session、不修补缺失 metadata，也不决定调用方是否有权使用某个 session。

Runtime 拥有每一轮执行流程：

1. 为验证过的 session 获取或创建 runner
2. 把 user、agent、project、session、channel 写入 context
3. 执行 pre-agent hooks
4. 必要时 compaction
5. 更新 session last-active/title metadata
6. 从 memory 组装 history
7. 构建 effective system prompt，包括 session snapshot
8. 执行 before-run hooks
9. 应用 system override 和 excluded tools
10. append user message
11. stream runner events
12. 持久化 assistant/tool output
13. 处理 timeout notice 和错误
14. 执行 post-agent hooks

Runtime 在准备已受理回合前获取 PostgreSQL `AgentRun`。部分唯一索引保证每个 Session 最多有一个 running Run；第二个并发 admission 返回 `ErrSessionBusy`。进程内 active map 继续用于快速拒绝和本地取消。

### `memory.Provider`

Memory 存储和组装内容。它不拥有 session 授权或生命周期策略。

Memory 负责：

- 初始化 conversation storage
- append messages
- 在 token budget 内组装 history
- compaction 和 summaries
- profile、soul、constraints、knowledge、changelog
- session snapshots
- 通过 `SessionManager` 存储 session metadata

Provider 可以存 session metadata，但使用 metadata 的策略归 `session.Registry`。

## Session 和 memory 的边界

最重要的边界是：

**Session 决定一个 conversation container 能不能被使用。Memory 决定这个 container 里有什么内容。**

Session 拥有：

- `UserID`
- `AgentID`
- `ProjectID`
- `Kind`
- `Channel`
- archived state
- title 和 last-active metadata
- exact-ID creation policy
- resume/kind validation
- main session resolution
- review candidate policy

Memory 拥有：

- messages
- context assembly
- summaries 和 compaction
- profile/soul/constraints/knowledge
- snapshots 和 changelog

硬规则：

```go
// 生产代码只从验证过的 session.Info 派生 memory scope。
// MemoryScope 会校验 session invariant，遇到非法 Info 时 fail closed 返回错误。
scope, err := svc.Sessions.MemoryScope(validatedInfo)
```

`session.Info` 是独立的、经过验证的 session-domain 类型，不是 `memory.SessionInfo` 的别名。所有生产代码（包括 `runtime.Runtime`）都只通过 `MemoryScope` 获取 `memory.Session`，不存在 runtime 直接构造的例外。group session 带有持久且经过验证的 `GroupID`（持久化在 `ctx_conversation.group_id`，且 `UserID == GroupID`）。对 private session，read、write、compaction 落在同一个 partition。group session 的 read 和 write 共享同一个持久 canonical scope；group compaction 不被支持——`CompactSession` 会用 `ErrGroupCompactionUnsupported` 拒绝它，因为 group history 来自 group event log，而不是 LCM conversation。低层 memory 测试仍可以直接构造 `memory.Session`。

## Session kinds 和 channels

Session kind 描述 session 为什么存在。Channel 描述 session 从哪里来。

| Kind        | Owner                 | 用户可见？ | 创建路径                               |
| ----------- | --------------------- | ---------- | -------------------------------------- |
| `main`      | private user chat     | 是         | `ResolveMainSession`                   |
| `chat`      | 普通前台/channel chat | 是         | channel resolver、`Chat`、`NewSession` |
| `delegate`  | child agent work      | 通常隐藏   | `Delegate`                             |
| `task`      | async task worker run | 默认隐藏   | `MintTaskSession`                      |
| `scheduler` | scheduled job run     | 隐藏或过滤 | `ChatForScheduler`                     |

Typed resume 必须验证 kind。即使 ID 一样，scheduler run 也不能恢复 delegate session。Channel session 虽然 key 是 trusted，也必须要求 `KindChat`。

所有 kind 都接受人工消息。Web UI 可以像给 `chat` Session 发消息一样，给 `delegate`、`task`、`scheduler` Session 发消息。所有入口获取同一套 `AgentRun` 租约。Agent 发起的 Session 输入在持久 inbox 中保留精确来源、目标和 actor；实时受理在同一事务中将 receipt 关联到最多一个目标 Run 并写入输入。有界本地 FIFO 会等待 busy owner；排队超时、无 capacity 和 admission 前取消只留下 failed、未关联的 receipt。

## ID trust model

不是所有 session ID 都一样。

### Untrusted IDs：resume-only

这些 ID 来自用户、HTTP path、model tool call，或者任何 agent 能影响的地方。

- `POST /sessions/{sessionId}/messages`
- session tool `session_id`
- 普通 `Service.Chat` request `SessionID`

这些路径里，非空 ID 的意思是：**加载已有 session 并验证 kind/ownership**。如果不存在，返回 not found。不要用这个 exact ID 创建新行。

### Trusted IDs：只能在 Service 方法背后 exact-create

这些 ID 由 Stella 系统派生，不由用户或模型提供。

- channel/group session keys
- scheduler run session IDs

只有专门的 Service 方法可以 exact-create trusted ID。这些方法也必须设置 `RequireKind`，这样和其他 kind 撞 ID 时会 fail closed。

## Runtime turn preparation

Runtime 每一轮都构建 effective prompt，而不是只在 runner 启动时构建一次。

### Snapshot prompt

Session snapshots 防止后台 memory 更新意外改变正在进行的 conversation。

Runtime snapshot flow：

1. 如果存在 per-run system override，把它作为 base system，并跳过 snapshot reconstruction。
2. 否则，如果 memory 实现 `SessionSnapshotStore`，每轮对 `(session_id, user_id, agent_id)` 调用 `GetOrCreateSessionSnapshot`。
3. 把 `SnapshotVersion` 传入 prompt builder。
4. 用这个 base system 执行 before-run hooks。
5. 把最终 system prompt 通过 per-run context override 传给 runner。

为什么有 `systemOverride` 时跳过 snapshot：delegate turn 会传入由 parent runner base system 加 preset 组装出的显式 system prompt。再重建一次 snapshot prompt 容易重复或冲突。

### Timeout semantics

Chat timeout 是可恢复停止，不是硬失败。Runtime 会持久化并 stream 一个友好的 continuation notice，不会把 `ErrChatTimeout` 转发给调用方。非 timeout 错误仍然以 `Event{Err: ...}` 形式 stream。

这对 delegate 和 scheduler 很重要，因为它们通常把任何 stream error 当成 run failed。

### Concurrency

Agent 发起的发送保留进程内 FIFO，最多 32 条待处理输入，admission 等待上限为 30 秒。它轮询 busy admission，不重放已经受理的 Run。来源 deadline 和取消覆盖排队等待及嵌套回合。

每次进程启动都会生成新的 executor identity。Run 在整个生命周期内属于同一次启动。heartbeat、abort、completion 和 expiry 使用 PostgreSQL 时间与条件更新。过期 Run 进入 interrupted，其他 executor 不能续租、接管或恢复它。

Run 所属的数据库写入在业务变更的同一事务中校验 Run ID、executor boot、running 状态、abort 状态和租约。在开启事务前检查并不足够，因为另一个 executor 可能在检查与提交之间终结 Run。标记 Session 已读等经过授权的展示状态写入仍独立于执行权。

abort intent 与 completion 在 PostgreSQL 中竞争，获胜的终态转换在同一事务中记录 Run 结果和 Session turn activity。因此正常 Stop 会持久化 `canceled`，旧 executor 也不能覆盖后继回合的 activity。

模型 EOF 允许来源适配器完成收尾。仍需外发或记录来源业务结果的适配器会继续持有租约，在操作完成后明确确认结果。确认丢失或结果无法判断时记为 unknown，AgentRun 恢复不会重新执行该回合。渠道持久发布使用同一个确认边界，详见下文。

已关联 inbox 的恢复只跟随 Run 终态，不调用模型或工具。启动恢复可以重新鉴权并追加 legacy 或未关联 receipt，但不能为它们创建新 Run。崩溃可能留下已受理却没有回复的输入；自动重放会带来重复工具调用或外发副作用。

计算资源使用独立的 Session 代次，由同一个不可变 executor boot 持有。普通 runner 回收会保留健康计算资源；执行结果不确定时会封锁资源，直到取得终止证明，详见[沙箱所有权](./sandbox#会话所有权)。部署仍限制为单副本。这些所有权与恢复机制不能证明多副本已就绪；#637 还要求共享存储和部署一致性验收后才能开放多副本。

### 渠道持久受理与恢复

渠道先提交标准化来源信封、不可变媒体、回复能力、去重标识、序号和配额预留，再确认来源消息。队列预算分为三级：每个 binding 1,000 行 / 64 MiB，每个 principal 10,000 行 / 512 MiB，整个部署 100,000 行 / 8 GiB。字节计费覆盖已受理的 payload、媒体和加密回复能力，不代表数据库物理存储总量的上限。预留失败会回滚受理并施加背压。

四个后台 worker 按序处理各 binding 的队首。尚未关联 Run 的过期 claim 可以恢复；一旦关联 Run，恢复只跟随该 Run 的结果，不再次调用模型或工具。受理和关联 Run 时会重新校验持久化 principal，防止账号关联变化后用新所有者执行旧输入。群消息分类在同一事务提交路由决定与 responder FIFO 项，并拒绝过期 claimant 的提交。

`/new` 是携带受理时目标 Session 的 FIFO 屏障。条件轮换保证幂等：同一来源消息重复投递不会再次轮换，不同命令并发指向同一个旧 Session 时也只轮换一次。屏障之后的文本到达队首时才解析最终 Session。

最终发布和来源业务记录完成后，来源才确认 Run 完成。发送结果不确定或缺失确认时会阻塞 binding，交给管理员检查，不重放副作用。拒绝阻塞项会记录审计并释放队列屏障，但不会恢复丢失的回复，也不会重试原执行。操作入口见[渠道故障排除](../channels/telegram#故障排除)。

Publisher 报告外发结果时不等待 Run 完成，后续顺序由 FIFO 消费者负责：先完成队列项和配额收尾，再把结果转交给 Run。群消息发布和私聊恢复发布在这个流程内直接执行。Claim 时长不充当请求时限，较慢的有效回合仍受实际 Run deadline、取消和所有权检查约束。群消息在 Run 受理前遇到可重试错误时，派发表保持非终态，下一次 FIFO 尝试才会真正执行工作。派发表保留已接受和已发布的事实，重试时间只由 FIFO 决定。

每个进程在查询连接池之外持有一条串行使用的 PostgreSQL 控制连接，用于入口领导权和通知；连接丢失时取消并等待入口退出，重连后全量扫描。已知的 transaction 或 statement pooling 配置会被拒绝，因为领导权依赖稳定的数据库会话。Telegram 持久化已确认的 update offset；Discord 持久化可恢复的 gateway cursor，先受理 replay 再推进 cursor。Discord resume 状态失效时会阻塞入口，不会静默建立新会话并丢弃缺口。

### 实时事件扇出

每个已受理的 turn 都归服务端生命周期所有，而不是归某条 HTTP 连接所有。Runtime 把事件经由每个 runtime 一份的 `SessionHub` tee 出去，因此浏览器切换页面、刷新或短暂断线都不会停止 agent：

- `Runtime.Chat` 除了写给调用方的 channel，还把每个事件发布到 hub。发消息的初始 stream 断开时只移除该观察者，turn 继续运行。
- 发布永不阻塞 turn。Hub 会合并相邻的 text/reasoning delta，并为新观察者保留最多 4,096 条 replay entry 或 8 MiB 的进程内 replay；超过上限后，重连只接收后续事件，并在 turn 结束后从持久化历史对齐最终状态。
- turn 结束时，hub 关闭其订阅 channel。`POST /api/agents/{agentId}/sessions/{sessionId}/stop` 是独立、显式的取消路径。

`GET /api/agents/{agentId}/sessions/{sessionId}/events` 订阅只读 SSE 流，复用发消息端点的 AI-SDK 编码。本地流必须匹配数据库中的活跃 Run ID。如果该 Run 属于另一进程，端点返回结构化 `503`、`Retry-After: 3` 和 `error.details.run_id`，Web UI 每三秒轮询持久化历史。只有数据库证明不存在活跃 Run 后才返回 `204`；租约虽已过期但尚未终结的 Run 仍视为活跃。主 token 流和 replay 保留在本地，不做跨进程 token 转发。

## Caller flows

### Web/API message

```text
HTTP POST /api/agents/{agentId}/sessions/{sessionId}/messages
    -> Server 验证 auth 和 agent access
    -> Service.Chat(SessionID=sessionId, ...)
    -> Registry 加载已有 session，验证 ownership 和 kind
    -> Runtime.Chat(validatedInfo, message)
```

HTTP path 里的 `sessionId` 是 untrusted 且 resume-only。未知 ID 不能创建 session。

### Web/API create session

```text
HTTP POST /api/agents/{agentId}/sessions { kind: main|chat }
    -> Service.ResolveMainSession 或 Service.NewSession
    -> Registry 创建/提升/生成 session
```

公共 create API 不应该创建内部 `scheduler`、`task` 或 `delegate` sessions。

### Private channel direct message

已受理的渠道输入在 PostgreSQL 中保存规范化信封与媒体引用。恢复发送时从渠道配置重建 publisher，不依赖原入口连接。回复凭据使用部署 vault 密钥加密，信封只保存不透明引用。钉钉恢复遵守 webhook 自带的过期时间。微信恢复将 context token 绑定到原收件人，并设置本地 24 小时重建上限；该上限不保证平台有效期，也不定义物理保留时间，平台可能更早使 token 失效。微信发送结果不确定时记为 unknown，不自动重发或改用 fallback。

```text
channel resolves user + agent
    -> Service.ResolveMainSession
    -> Service.Chat(existing main session)
```

Private user channels 收敛到 main session。

### Group 或 shared channel message

```text
channel 派生 SessionKey 并解析 canonical GroupID
    -> Service.ResolveGroupChannelSession(SessionKey, GroupID, AgentID, Channel)
    -> Registry exact-create，因为 key 由 Stella 派生
    -> Service.Chat(validated group session)
    -> Runtime.Chat
```

Channel key 是 trusted，但 resume 仍然要求 `KindChat`。

### Scheduler job

```text
scheduler derives run session ID
    -> Service.ChatForScheduler(SessionID, KindScheduler, ChannelScheduler)
    -> Registry exact-create，因为 scheduler 拥有 ID 派生规则
    -> Runtime.Chat
```

如果 job 有明确 `AgentID`，不要 fallback 到任意 default service；那会掩盖 routing bug。

### Session managed-run adapter

```text
parent runner executes session_create / session_send
    -> session tool passes message + optional preset/session_id
    -> internal DelegateTool resolves preset and run options
    -> Service.RunDelegateSession
    -> Service.Delegate
    -> Registry 创建生成的 delegate session 或恢复已有 delegate session
    -> per-Session FIFO fairness
    -> 原子 inbox receipt / AgentRun admission
    -> Runtime.ChatAdmitted
```

规则：

- 面向模型的注册表暴露 `session`，不暴露 `delegate`
- supplied legacy delegate `session_id` 是 resume-only
- `session_create` 通过内部 adapter 创建生成的 delegate session
- child sessions 继承 user、agent、project scope
- 调用深度和 Session 祖先链通过 context 传递；runtime 拒绝深度溢出和循环
- 同级与嵌套调用共享由根 runtime 回合分配的 16 次原子调用预算
- 根 deadline 和取消覆盖队列等待及所有嵌套回合
- runtime option 合并祖先的 excluded tools，嵌套运行不会继承 channel chat binding
- Agent 输入持久化 actor ID 和来源 Session ID，provider 渲染将其标记为 information-only

### Task worker session

```text
task creation resolves owner agent and optional project
    -> Service.MintTaskSession(userID, agentID, projectID)
    -> task row stores session_id
    -> run row records the task session_id and executor_agent_id
    -> worker runner uses the resolved executor agent scope
```

Task worker session 创建在 task owner/manager agent 和可选 project 下。后续 run 仍可通过 dispatch hint 使用 run-level executor override。

### Reflect review

Reflect 应该使用 registry review listing 和 `Registry.MemoryScope`，这样 review candidates 遵守 session policy，并且 delegate/task/scheduler 这类内部 kind 可以被排除。

## Testing rules

Session 架构测试应该打在拥有规则的 seam 上。

| Rule                                                      | 测试位置                                 |
| --------------------------------------------------------- | ---------------------------------------- |
| 用户/模型提供的 ID 是 resume-only                         | `agent.Service`                          |
| trusted channel/scheduler IDs 可 exact-create 且要求 kind | `agent.Service`                          |
| ownership/kind/archive validation                         | `session.Registry`                       |
| turn assembly、snapshot prompt、timeout、busy guard       | `runtime.Runtime`                        |
| task session owner 是 executor                            | `internal/tasks` dispatcher/minter tests |
| HTTP create/message contract                              | `internal/server` 和 generated API types |

有用的 guard：

```bash
# Edge callers 不应该绕过 Service 直接用 Registry.Ensure。
rg "\.Sessions\.Ensure" internal cmd --glob '!**/*_test.go'

# Policy switches 不应该出现在 session plumbing 和 Service intent methods 之外。
rg "AllowExactIDCreate|CreateIfMissing|RequireKind" internal cmd \
  --glob '!**/*_test.go' \
  --glob '!**/session/**'

# 生产代码不应该手搓 memory.Session，除非在批准的 seam 中。
rg "memory\.Session\{" internal cmd --glob '!**/*_test.go'
```

这些 grep check 不能替代测试。它们只是防止架构漂移的绊线。

## 添加新的 agent entry point

添加新的 agent 执行入口时：

1. 用自然语言定义业务意图。
2. 判断 session ID 是 untrusted resume-only，还是 trusted system-derived。
3. 添加或复用一个 `agent.Service` 方法表达这个意图。
4. 让 `Service` 用正确的 create/resume/kind policy 调用 `session.Registry`。
5. 只把验证过的 `session.Info` 传给 `runtime.Runtime`。
6. 确保 memory 操作使用 `Registry.MemoryScope` 或 runtime validated scope。
7. 在拥有规则的 seam 上加测试。
8. 如果涉及 HTTP，先更新 OpenAPI，再运行 `mise run generate:api`。

## Anti-patterns

避免这些模式：

```go
// Edge caller 直接组合 lifecycle policy。
svc.Sessions.Ensure(ctx, session.Request{AllowExactIDCreate: true, ...})
```

```go
// 用户/模型提供的 ID 创建 session。
CreateIfMissing: true,
AllowExactIDCreate: true,
ID: req.SessionID,
```

```go
// Runtime 使用未经过 Registry 验证的 session.Info。
rt.Chat(ctx, session.Info{ID: id}, msg)
```

```go
// 生产代码从 request fields 手搓 memory scope。
mem.Append(ctx, memory.Session{ID: sessionID, UserID: userID, AgentID: agentID}, msg)
```

## 合并架构变更前的验证

涉及这个架构的改动，需要运行项目要求的检查：

```bash
mise run format
mise run build
mise run test
```

涉及 HTTP API 变更，还要运行：

```bash
mise run generate:api
```

涉及数据库 schema 变更，编辑任何表之前先按 `rules/schema-design` 的 goose 迁移流程走。
