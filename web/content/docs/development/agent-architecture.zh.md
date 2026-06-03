---
title: Agent 架构
---

> 本页面面向修改 Stella agent runtime、session、memory、channel、scheduler、delegate tool 或 task system 的开发者。

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

| Method                  | 用途                                         | ID trust model                                        |
| ----------------------- | -------------------------------------------- | ----------------------------------------------------- |
| `Chat`                  | 前台 chat，使用已有 session 或生成新 session | caller-supplied `SessionID` 是 resume-only            |
| `ChatForChannel`        | 非私有 channel/group chat                    | 允许 exact-create，因为 `SessionKey` 由 Stella 派生   |
| `ChatForScheduler`      | scheduler 发起的 run                         | 允许 exact-create，因为 `SessionID` 由 scheduler 派生 |
| `ResolveChannelSession` | 只解析 channel session，不执行 chat turn     | trusted channel key，要求 `KindChat`                  |
| `NewSession`            | HTTP/Web UI 创建 session                     | 只能生成 ID                                           |
| `MintTaskSession`       | task system 创建 worker session              | 在 resolved executor agent 名下生成 ID                |
| `Delegate`              | 运行/恢复 delegate child session             | caller/model supplied `SessionID` 是 resume-only      |
| `ResolveMainSession`    | 解析或创建 private main session              | 生成或提升 main session                               |

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

Runtime 也强制同一个 session 同一时间只能有一个 active turn。第二个并发 chat 会返回 `ErrSessionBusy`，避免 transcript 交错写入。

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
// 生产代码不应该手搓 memory.Session。
scope := svc.Sessions.MemoryScope(validatedInfo)
```

低层 memory 测试是例外。生产代码应该从验证过的 `session.Info` 获取 `memory.Session`，这样 ownership 和 kind policy 不会被绕过。

## Session kinds 和 channels

Session kind 描述 session 为什么存在。Channel 描述 session 从哪里来。

| Kind        | Owner                 | 用户可见？ | 创建路径                               |
| ----------- | --------------------- | ---------- | -------------------------------------- |
| `main`      | private user chat     | 是         | `ResolveMainSession`                   |
| `chat`      | 普通前台/channel chat | 是         | `Chat`、`ChatForChannel`、`NewSession` |
| `delegate`  | child agent work      | 通常隐藏   | `Delegate`                             |
| `task`      | async task worker run | 默认隐藏   | `MintTaskSession`                      |
| `scheduler` | scheduled job run     | 隐藏或过滤 | `ChatForScheduler`                     |

Typed resume 必须验证 kind。即使 ID 一样，scheduler run 也不能恢复 delegate session。Channel session 虽然 key 是 trusted，也必须要求 `KindChat`。

## ID trust model

不是所有 session ID 都一样。

### Untrusted IDs：resume-only

这些 ID 来自用户、HTTP path、model tool call，或者任何 agent 能影响的地方。

- `POST /sessions/{sessionId}/messages`
- delegate tool `session_id`
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
3. 把 `SnapshotVersion` 和 `SnapshotUpdatedAt` 传入 prompt builder。
4. 用这个 base system 执行 before-run hooks。
5. 把最终 system prompt 通过 per-run context override 传给 runner。

为什么有 `systemOverride` 时跳过 snapshot：delegate turn 会传入由 parent runner base system 加 preset 组装出的显式 system prompt。再重建一次 snapshot prompt 容易重复或冲突。

### Timeout semantics

Chat timeout 是可恢复停止，不是硬失败。Runtime 会持久化并 stream 一个友好的 continuation notice，不会把 `ErrChatTimeout` 转发给调用方。非 timeout 错误仍然以 `Event{Err: ...}` 形式 stream。

这对 delegate 和 scheduler 很重要，因为它们通常把任何 stream error 当成 run failed。

### Concurrency

Runtime 对每个 session 最多允许一个 active turn。第二个 same-session turn 会被 `ErrSessionBusy` 拒绝。Runtime 不排队。排队需要产品层定义 cancellation、ordering 和 backpressure；拒绝更简单也更安全。

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

```text
channel resolves user + agent
    -> Service.ResolveMainSession
    -> Service.Chat(existing main session)
```

Private user channels 收敛到 main session。

### Group 或 shared channel message

```text
channel derives SessionKey
    -> Service.ChatForChannel(SessionKey, KindChat, Channel)
    -> Registry exact-create，因为 key 由 Stella 派生
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

### Delegate tool

```text
parent runner executes delegate tool
    -> tool sends task + optional session_id + inherited ProjectID
    -> Service.RunDelegateSession
    -> Service.Delegate
    -> Registry 创建生成的 delegate session 或恢复已有 delegate session
    -> Runtime.Chat
```

规则：

- supplied delegate `session_id` 是 resume-only
- omitted `session_id` 创建生成的 delegate session
- child sessions 继承 user、agent、project scope
- 同一次 tool call 里的重复非空 `session_id` 在启动 goroutine 前拒绝
- child runner 排除 delegate tool，避免递归

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
// Runtime 使用未经过 Registry 验证的 session info。
rt.Chat(ctx, memory.SessionInfo{ID: id}, msg)
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

涉及数据库 schema 变更，编辑任何表之前先按 `rules/schema-design` 的 Atlas 流程走。
