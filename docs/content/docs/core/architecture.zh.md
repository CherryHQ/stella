---
title: 架构
---

## 系统概述

anna 的结构是一组松耦合的包，在 `main.go` 中组装在一起。系统支持多用户和多代理，消息路由按消息级别处理。核心流程：

1. 一个**通道**（CLI、Telegram、QQ、Feishu 或微信）接收用户输入
2. 通道**解析用户**（通过外部 ID + 平台进行 upsert）和**解析代理**（DM 默认、群组绑定或回退）
3. **PoolManager** 通过代理 ID 查找（或创建）代理的 **Pool**
4. **Pool** 管理会话并分发给 **Runner**
5. **Go runner** 通过 `internal/ai/` 调用 LLM 提供商，在循环中执行工具
6. 响应通过通道流回给用户

```
Channel (CLI / Telegram / QQ / Feishu / WeChat)
    |
    v
Resolve user (identity.go)  -->  Resolve agent (identity.go)
    |
    v
PoolManager.Get(agentID)  -->  Pool (sessions + runner lifecycle)
    |
    v
Go Runner (agent loop + tools)
    |
    v
LLM Provider (Anthropic / OpenAI / OpenAI-compatible)
```

会话键的作用域为每个代理：`{agentID}:{platform}:{userID}:{context}`，确保同一用户与不同代理对话时拥有独立的对话历史。

## 包布局

```
cmd/anna/                             入口点，CLI 命令，服务组装

internal/
  config/
    store.go                          Store 接口（基于数据库的配置 CRUD）
    dbstore.go                        DBStore 实现（基于 SQLite）
    snapshot.go                       每个代理的只读配置快照
    types.go                          Provider、Agent、Channel、User 类型

  ai/
    message.go                        Message、Content 类型
    model.go                          Model、ModelCost、Context 类型
    options.go                        RequestOptions
    events.go                         StreamEvent 类型
    provider.go                       Provider 接口、注册表、事件流
    transform.go                      消息格式转换
    providers/
      anthropic/                      Anthropic 提供商（Messages API）
      openai/                         OpenAI 提供商（Chat Completions API）
      openai-response/                OpenAI 兼容提供商（Responses API）
      register_builtins.go            自动注册所有内置提供商

  agent/
    pool_manager.go                   PoolManager（map[agentID]*Pool，延迟创建）
    pool.go                           会话池，Chat()，runner 生命周期
    pool_options.go                   PoolOption、ChatOption、With* 函数
    pool_reaper.go                    空闲/死亡 runner 回收
    pool_compaction.go                会话压缩编排
    session.go                        每个聊天会话状态，BuildSessionKey()
    workspace.go                      每个代理工作区设置（目录、身份文件）
    factory.go                        每个代理 runner 工厂（Snapshot -> GoRunner）
    engine/
      engine.go                       代理循环引擎（多轮工具执行）
      continue.go                     从现有历史恢复代理循环
      types.go                        LoopConfig、ToolSet、ToolFunc
      events.go                       循环事件类型（AgentStarted、AssistantDelta 等）
      tool_execution.go               工具调用分发与回调
    runner/
      runner.go                       Runner 接口、RPC 类型、事件辅助函数
      gorunner.go                     GoRunner：原生 LLM 提供商调用
      prompt.go                       系统提示构建器（记忆、工具、上下文）
      skill.go                        从代理工作区加载技能
      stream_proxy.go                 流代理工具
    tool/                             内置工具
      tool.go                         Tool 接口和注册表
      read.go                         读取文件内容
      bash.go                         执行 shell 命令
      write.go                        创建/覆盖文件
      edit.go                         编辑文件部分
      delegate.go                     子代理委托（并行子任务）
      truncate.go                     截断大型输出到临时文件
      webfetch.go                     获取网页内容

  channel/
    model.go                          Channel 接口、模型列表/切换类型
    command.go                        共享 HandleCommand（/new、/compact、/model、/agent、/whoami）
    identity.go                       ResolveUser、ResolveAgent、ChatContext
    agent_command.go                  AgentCommander（列出/切换代理）
    resolved.go                       ResolvedChat 类型（Pool + User + AgentID + SessionKey）
    util.go                           共享工具（SplitMessage、FormatDuration）
    notifier.go                       通知分发器（多通道）
    notify_tool.go                    代理通知工具
    cli/
      cli.go                          交互式 TUI 入口点
      chat.go                         Bubble Tea 聊天模型，Update()
      chat_view.go                    View()，resize()，markdown 渲染
      chat_input.go                   输入处理，斜杠命令自动补全
      chat_picker.go                  模型选择器按键处理
      command.go                      聊天内斜杠命令（/compact、/model 等）
      model.go                        TUI 模型切换 UI
      style.go                        终端样式
    telegram/
      telegram.go                     机器人设置，长轮询（实现 channel.Channel）
      handler.go                      消息/回调处理器
      stream.go                       流式传输（草稿 API + 编辑回退）
      render.go                       Markdown 渲染
      model.go                        分页模型选择器 UI
    qq/
      qq.go                           机器人设置，WebSocket（实现 channel.Channel）
      handler.go                      消息处理器，命令路由
      stream.go                       通过 QQ Stream API 流式传输
      render.go                       消息分块
      model.go                        基于文本的模型选择 UI
    feishu/
      feishu.go                       机器人设置，WebSocket，通知后端
      handler.go                      消息事件处理器
      stream.go                       通过消息更新流式传输（就地编辑）
      render.go                       响应拆分
      model.go                        基于文本的模型列表

  admin/
    server.go                         Admin HTTP API 服务器 + 嵌入式 SPA
    agents.go                         Agent CRUD 端点
    channels.go                       Channel 配置端点
    providers.go                      Provider 配置端点
    sessions.go                       会话列表/详情端点
    settings.go                       全局设置端点
    users.go                          用户管理端点
    scheduler.go                      调度器任务端点
    embed.go                          嵌入式前端资源
    ui/                               SPA 前端（构建资源）

  db/
    embed.go                          嵌入式迁移文件系统
    database.go                       SQLite 打开、WAL、迁移运行器
    schemas/tables/                   模式真实来源（Atlas 读取这些）
    migrations/                       Atlas 生成的 SQL 迁移文件
    queries/                          sqlc 查询定义
    sqlc/                             生成的查询代码（sqlc 输出）

  scheduler/
    service.go                        调度器服务（gocron/v2）
    heartbeat.go                      心跳轮询（决策/执行/通知）
    persistence.go                    任务 JSON 持久化（加载/保存）
    job.go                            Job 和 Schedule 类型
    tool.go                           代理调度器工具（添加/列出/移除）

  memory/
    engine.go                         内存引擎门面
    assembler.go                      上下文窗口组装
    compaction.go                     叶子 + 压缩聚合遍历
    retrieval.go                      消息搜索和检索
    summarize.go                      LLM 摘要
    types.go                          Engine 接口、CompactionResult 等
    context.go                        上下文项管理
    usermemory.go                     UserMemoryStore（每用户每代理数据库访问）
    tool/                             内存代理工具（grep/describe/expand/user_memory_update）

  skills/
    tool.go                           代理技能工具（search/install/list/remove）
    search.go                         通过 skills.sh API 搜索技能生态
    install.go                        Git clone + copy 安装流程（go-git）
    list.go                           列出已安装技能
    remove.go                         移除已安装技能

  toolspec/
    toolspec.go                       工具定义类型（零依赖叶子包）
```

## 配置

配置存储在 SQLite 中，通过 `config.Store` 接口访问。没有 YAML 配置文件；所有设置（提供商、代理、通道、调度器）都通过 admin API 或数据库管理。

- **Store**（`config.Store`）-- 用于读写提供商、代理、通道、用户和聊天-代理绑定的接口。由 `DBStore` 实现。
- **DBStore**（`config.DBStore`）-- 使用 sqlc 生成的查询的 SQLite 支持实现。
- **Snapshot**（`config.Snapshot`）-- 单个代理的只读配置视图。在池创建时从 Store 组装。包含已解析的提供商凭证、模型名称、工作区路径、系统提示和 runner 设置。传递给 runner 工厂和需要每个代理配置的工具。

## 多用户多代理路由

每条传入消息在到达代理循环之前都要经过两步解析：

1. **用户解析**（`channel.ResolveUser`）-- 通过外部平台 ID 对发送者进行 upsert，返回带有稳定内部用户 ID 的 `config.User` 记录。
2. **代理解析**（`channel.ResolveAgent`）-- 确定哪个代理处理此消息：
   - 在 DM 中，使用用户的 `default_agent_id`。
   - 在群聊中，`chat_agents` 绑定将 `(platform, chat_id)` 映射到代理。
   - 如果两者都未设置，则使用第一个启用的代理作为回退。

已解析的用户和代理被打包到 `ResolvedChat` 结构中，该结构贯穿所有处理器和命令路径。此结构包含目标 `Pool`、`User`、`AgentID` 和 `SessionKey`。

`PoolManager` 维护 `map[agentID]*Pool` 并在首次访问时延迟创建池。每个池通过 runner 工厂使用其代理的 `Snapshot`（模型、凭证、工作区、系统提示）进行配置。

### 代理切换

`/agent` 斜杠命令（由 `AgentCommander` 处理）让用户列出启用的代理并为其 DM 或群聊切换活动代理。在 DM 中，这会更新 `default_agent_id`；在群组中，它会更新 `chat_agents` 绑定。`/model` 在当前代理内保持每会话。

## 提供商

支持三种 LLM 提供商：

| 提供商            | API                  | 使用场景                                      |
| ----------------- | -------------------- | --------------------------------------------- |
| `anthropic`       | Messages API         | Claude 模型                                   |
| `openai`          | Chat Completions API | GPT 模型                                      |
| `openai-response` | Responses API        | OpenAI 兼容服务（Perplexity、Together.ai 等） |

每个提供商都实现 `ai.ProviderAdapter` 接口以进行流式响应，并可选实现 `ai.ModelLister` 以进行模型发现。所有提供商都通过 `ImageContent` 类型支持多模态输入（文本 + 图像），转换为其原生图像格式（Anthropic 的 base64 块、OpenAI 的数据 URI image_url）。

## 工具

Go runner 将工具注入 LLM 调用。工具遵循通用接口（在 `internal/agent/tool/` 中定义）。工具元数据使用来自零依赖 `internal/toolspec/` 叶子包的 `toolspec.Definition` 类型，使领域包与 `internal/ai/` 解耦：

```go
type Tool interface {
    Definition() toolspec.Definition
    Execute(ctx context.Context, args map[string]any) (string, error)
}
```

### 内置工具（始终可用）

| 工具       | 描述                            |
| ---------- | ------------------------------- |
| `read`     | 使用 UTF-8 安全截断读取文件内容 |
| `bash`     | 执行 shell 命令                 |
| `write`    | 原子性创建/覆盖文件             |
| `edit`     | 编辑文件部分，保留上下文        |
| `truncate` | 将大型输出截断到临时文件        |
| `delegate` | 为有界子任务生成子代理循环      |
| `webfetch` | 获取网页内容                    |

### 委托

`delegate` 工具使代理能够生成具有隔离上下文的子代理循环。这对于从新上下文受益的专注子任务（研究、代码审查、起草）很有用，而不会污染父对话。

- 每个子任务获得仅包含任务描述的新消息历史
- 多个任务通过 goroutine 并行运行
- `delegate` 工具从子任务中排除以防止递归
- 子任务输出截断为 ~4096 个 token，以避免膨胀父上下文
- 每任务选项：`model`（覆盖）、`system`（附加指令）、`tools`（白名单）、`max_turns`（默认 10）、`timeout_seconds`（默认 120）

### 额外工具（有条件注入）

| 工具        | 条件                      | 描述                                                    |
| ----------- | ------------------------- | ------------------------------------------------------- |
| `memory`    | 始终                      | 统一内存工具（grep/describe/expand/user_memory_update） |
| `skills`    | 始终                      | 技能管理（从 skills.sh 搜索/安装/列出/移除）            |
| `scheduler` | `scheduler.enabled: true` | 安排任务（添加/列出/移除作业）                          |
| `notify`    | 网关模式 + 通道已配置     | 通过分发器发送通知                                      |

`memory` 工具中的 `user_memory_update` 操作是一个只写操作，它替换数据库中整个每用户每代理的内存内容。这些笔记始终加载到系统提示中（在"用户记忆"部分），因此代理在会话之间具有关于用户偏好和重要细节的持久上下文。这用基于数据库的 `UserMemoryStore` 取代了之前基于文件的 SOUL.md/USER.md 方法。

## 会话生命周期

1. 通道解析用户和代理，产生 `ResolvedChat`
2. 调用 `ResolvedChat.Pool.Chat(ctx, sessionKey, message)` -- message 是 `string`（文本）或 `[]ContentBlock`（多模态）
3. Pool 使用作用域键 `{agentID}:{platform}:{userID}:{context}` 查找或创建会话
4. Pool 获取或为会话创建 runner，使用代理的 Snapshot 配置
5. Runner 通过通道流回事件
6. 空闲超时后，runner 被回收；会话通过 `memory.Engine` 持久化到 SQLite

有关历史管理，请参阅 [session-compaction.md](/docs/core/session-compaction)。

## 通道接口

所有消息平台都实现 `channel.Channel` 接口：

```go
type Channel interface {
    Name() string
    Start(ctx context.Context) error
    Stop()
    Notify(ctx context.Context, n Notification) error
}
```

共享命令逻辑（`/new`、`/compact`、`/model`、`/agent`、`/whoami`）位于 `channel.HandleCommand` 中，每个通道委托给它以处理核心逻辑。`/model` 和 `/agent` 按通道处理，因为它们需要特定于平台的 UI（Telegram 使用内联键盘，QQ、Feishu 和微信使用文本列表，CLI 使用 TUI 选择器）。

## Admin API

`internal/admin/` 包提供用于管理系统的 HTTP API 和嵌入式 SPA。端点涵盖提供商、代理、通道、用户、会话、调度器作业和全局设置的 CRUD 操作。admin 服务器通过 `config.Store` 读写，为操作员提供 Web 界面来配置之前通过 YAML 文件完成的配置。

## 通知流程

```
Agent notify tool      --> Dispatcher --> Channel (Telegram/QQ/Feishu/WeChat)
Scheduler job result   --> Dispatcher --> Channel (Telegram/QQ/Feishu/WeChat)
```

分发器在设置早期创建，但后端在网关服务启动时稍后注册。PoolManager 用于通过 `ExtraToolsFactory` 按代理注入通知工具。有关详细信息，请参阅 [notification-system.md](/docs/features/notification-system)。
