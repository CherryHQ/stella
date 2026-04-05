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
cmd/anna/              入口点，CLI 命令，服务组装
internal/
  config/              Store 接口、DBStore（SQLite）、Snapshot、类型
  ai/                  Message/Content 类型、Model、Provider 接口、流式事件
  agent/               PoolManager、Pool、Session、工作区设置、runner 工厂
    engine/            代理循环引擎（多轮工具执行）
    runner/            GoRunner、系统提示构建器、技能加载
  channel/             Channel 接口、身份解析、斜杠命令、通知
    cli/               Bubble Tea TUI
    telegram/          Telegram 机器人
    qq/                QQ 机器人
    feishu/            飞书机器人
  admin/               HTTP API + 嵌入式 SPA（templ + Alpine.js + daisyUI）
  auth/                RBAC/ABAC 策略引擎、会话、沙箱
  db/                  SQLite、Atlas 迁移、sqlc 查询
  scheduler/           gocron 服务、心跳、调度器工具
  memory/              内存引擎、压缩、检索、内存工具
  skills/              技能工具（通过 skills.sh 搜索/安装/列出/移除）
pkg/
  tools/               Tool 接口、注册表、内置工具（read、bash、write、edit、agent）
plugins/
  tools/               插件工具注册表 + 插件工具（webfetch）
  hooks/               插件钩子注册表 + 插件钩子（rtk）
  channels/            通道插件（telegram、qq、feishu、weixin）
  providers/           供应商插件注册表 + LLM 适配器（anthropic、openai、openai-response）
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

LLM 提供商采用插件模式。Anna 内置三种提供商：

| 提供商            | API                  | 使用场景                                      |
| ----------------- | -------------------- | --------------------------------------------- |
| `anthropic`       | Messages API         | Claude 模型                                   |
| `openai`          | Chat Completions API | GPT 模型                                      |
| `openai-response` | Responses API        | OpenAI 兼容服务（Perplexity、Together.ai 等） |

每个提供商都实现 `ai.ProviderAdapter` 接口以进行流式响应，并可选实现 `ai.ModelLister` 以进行模型发现。所有提供商都通过 `ImageContent` 类型支持多模态输入（文本 + 图像），转换为其原生图像格式（Anthropic 的 base64 块、OpenAI 的数据 URI image_url）。

提供商位于 `plugins/providers/`，通过 `init()` 自注册。添加新的提供商只需在 `plugins/providers/` 下创建一个包——无需其他连接代码。详见[插件系统](/docs/features/plugin-system)。

## 工具

Go runner 将工具注入 LLM 调用。工具遵循定义在 `pkg/tools/` 中的通用接口。`tools.Definition` 类型是 `ai.ToolDefinition` 的类型别名，使领域包保持解耦：

```go
type Tool interface {
    Definition() tools.Definition
    Execute(ctx context.Context, args map[string]any) (string, error)
}
```

### 内置工具（始终可用）

| 工具    | 描述                            |
| ------- | ------------------------------- |
| `read`  | 使用 UTF-8 安全截断读取文件内容 |
| `bash`  | 执行 shell 命令                 |
| `write` | 原子性创建/覆盖文件             |
| `edit`  | 编辑文件部分，保留上下文        |
| `agent` | 为有界子任务生成子代理循环      |

### 插件工具（通过管理面板切换）

| 工具       | 描述         |
| ---------- | ------------ |
| `webfetch` | 获取网页内容 |

插件工具位于 `plugins/tools/`，通过 `init()` 自注册。添加新的插件工具只需一个空白导入，无需修改组装代码。详见[插件系统](/docs/features/plugin-system)。

### Agent 工具

`agent` 工具使代理能够生成具有隔离上下文的子代理循环。这对于从新上下文受益的专注子任务（研究、代码审查、起草）很有用，而不会污染父对话。

- 每个子任务获得仅包含任务描述的新消息历史
- 多个任务通过 goroutine 并行运行，支持可配置并发度
- `agent` 工具从子任务中排除以防止递归
- 子任务输出截断为 ~4096 个 token，以避免膨胀父上下文
- 支持从带有 YAML 前置数据的 markdown 文件加载预设
- 每任务选项：`preset`、`context`、`model`（覆盖）、`system`（附加指令）、`tools`（白名单）、`max_turns`（默认 10）、`timeout_seconds`（默认 120）

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
