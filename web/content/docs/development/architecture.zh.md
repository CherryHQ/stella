---
title: 架构
---

> 本节面向为 Stella 贡献代码的开发者。

## 系统概述

stella 的结构是一组松耦合的包，在 `main.go` 中组装在一起。系统支持多用户和多代理，消息路由按消息级别处理。核心流程：

1. 一个**通道**（CLI、Telegram、QQ、Feishu 或微信）接收用户输入
2. 通道**解析用户**（通过外部 ID + 平台进行 upsert）和**解析代理**（DM 默认、群组绑定或回退）
3. **PoolManager** 通过代理 ID 查找（或创建）代理的 **Pool**
4. **Pool** 管理会话并分发给 **Runner**
5. **Runner** 通过 `internal/ai/` 调用 LLM 提供商，在循环中执行工具
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
cmd/stella/              入口点，CLI 命令，服务组装
internal/
  config/              Store 接口、DBStore（SQLite）、Snapshot、类型
  ai/                  Message/Content 类型、Model、Provider 接口、流式事件
  agent/               PoolManager、Pool、Session、工作区设置、runner 工厂
    engine/            代理循环引擎（多轮工具执行）
    runner/            Runner、系统提示构建器、技能加载
  channel/             Channel 接口、身份解析、斜杠命令、通知
    cli/               Bubble Tea TUI
    telegram/          Telegram 机器人
    qq/                QQ 机器人
    feishu/            飞书机器人
  admin/               HTTP API + 嵌入式 React SPA
  auth/                RBAC/ABAC 策略引擎、会话、沙箱
  db/                  SQLite、Atlas 迁移、sqlc 查询
  scheduler/           gocron 服务、心跳（通过 stella scheduler CLI 提供技能）
  skills/              技能工具（通过 skills.sh 搜索/安装/列出/移除）
pkg/
  memory/              Memory Provider 接口、类型、Summarizer、工具自动生成、测试辅助
  tools/               Tool 接口、注册表、内置工具（read、bash、write、edit、agent）
plugins/
  memory/              记忆插件注册表 + 实现
    lcm/               无损上下文管理（默认）— 摘要 DAG、压缩、搜索
    simple/            滑动窗口记忆 — 保留最近 N 条消息，无摘要
  tools/               插件工具注册表 + 插件工具（mcp、webfetch）
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

LLM 提供商采用插件模式。Stella 内置三种提供商：

| 提供商            | API                  | 使用场景                                      |
| ----------------- | -------------------- | --------------------------------------------- |
| `anthropic`       | Messages API         | Claude 模型                                   |
| `openai`          | Chat Completions API | GPT 模型                                      |
| `openai-response` | Responses API        | OpenAI 兼容服务（Perplexity、Together.ai 等） |

每个提供商都实现 `ai.ProviderAdapter` 接口以进行流式响应，并可选实现 `ai.ModelLister` 以进行模型发现。所有提供商都通过 `ImageContent` 类型支持多模态输入（文本 + 图像），转换为其原生图像格式（Anthropic 的 base64 块、OpenAI 的数据 URI image_url）。

提供商位于 `plugins/providers/`，通过 `init()` 自注册。添加新的提供商只需在 `plugins/providers/` 下创建一个包——无需其他连接代码。详见[插件系统](/docs/development/plugin-system)。

## 工具

Runner 将工具注入 LLM 调用。工具遵循定义在 `pkg/tools/` 中的通用接口。`tools.Definition` 类型是 `ai.ToolDefinition` 的类型别名，使领域包保持解耦：

```go
type Tool interface {
    Definition() tools.Definition
    Execute(ctx context.Context, args map[string]any) (string, error)
}
```

### 内置工具（始终可用）

| 工具       | 描述                             |
| ---------- | -------------------------------- |
| `read`     | 使用 UTF-8 安全截断读取文件内容  |
| `bash`     | 执行 shell 命令                  |
| `write`    | 原子性创建/覆盖文件              |
| `edit`     | 编辑文件部分，保留上下文         |
| `delegate` | 将专注子任务委派到隔离的子循环中 |

### 插件工具（通过Web UI切换）

| 工具       | 描述                                                |
| ---------- | --------------------------------------------------- |
| `mcp`      | 通过一个通用 Stella MCP 工具代理已配置的 MCP 服务器 |
| `webfetch` | 获取网页内容                                        |

核心本地工作区工具通过 Docker 沙箱后端运行。`bash` 工具通过 `Session.Exec` 执行；`read`、`write` 和 `edit` 工具使用 `Session.ResolvePath` 获取主机路径，然后直接调用 `os.*`。Runner 启动时如果 Docker 不可用则失败关闭。

### 沙盒架构

沙盒系统使用 Docker 进行进程和文件系统隔离：

- **Session**：Runner 启动时创建的每次运行 Docker 容器，关闭时销毁。
- **Workspace root**：挂载到容器中的代理工作区目录。
- **Working Directory**：容器内的逻辑工作目录，通过 `Session.WorkingDir` 解析。

所有核心工具在每个 Runner 中共享同一个容器会话：

```
┌─────────────────────────────────────────────────────────────┐
│                     Go Runner                               │
│  ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐           │
│  │  bash   │ │  read   │ │  write  │ │  edit   │           │
│  └────┬────┘ └────┬────┘ └────┬────┘ └────┬────┘           │
│       │           └───────────┘                             │
│       │ Exec              ResolvePath + os.*                │
│       ▼                                                     │
│  ┌──────────────────┐                                       │
│  │  sandbox.Session │                                       │
│  │  (Docker)        │                                       │
│  └──────────────────┘                                       │
└─────────────────────────────────────────────────────────────┘
```

### 平台要求

Docker 是唯一的后端，在所有平台（Linux、macOS、Windows）上都是必需的。Docker 守护进程必须正在运行并可访问。Stella 在会话创建时联系 Docker 守护进程，如果不可用则失败关闭。没有 `auto` 或 `Relaxed` 模式。

### 网络策略配置

每个代理的沙盒网络策略通过 admin API 或数据库配置：

| 模式        | 描述                   | 使用场景               |
| ----------- | ---------------------- | ---------------------- |
| `disabled`  | 无出站网络访问（默认） | 不可信代码的最大安全性 |
| `allow_all` | 无限制的出站访问       | 需要完整网络的可信代理 |

Stella 在会话创建时验证网络模式，如果 Docker 后端无法强制执行则失败关闭。

### 失败行为

Runner 启动在以下情况下失败关闭：

- Docker 守护进程不可用或无法访问
- 网络策略配置无效
- 网络策略有效但 Docker 后端不支持

这确保沙盒执行要么完全正常运行，要么根本不运行，防止安全降级。

### 显式例外边界

沙盒保证适用于 Stella 拥有的本地执行路径。远程 MCP 传输目前被视为独立的信任边界：

- 本地 MCP stdio 生成使用 `Session.StartProcess`，通过活跃的 Runner 会话进行调解
- 远程 MCP HTTP/SSE/StreamableHTTP 拨号目前不由 `ToolRuntime` 调解
- 该例外是显式的、可观察的，并记录为 `runtime.exception_path`，`exception_id=EX-009`

内置工具（bash、read、write、edit、delegate、mcp、notify、skills、coretools）位于 `internal/tools/`，直接集成到代理中。插件工具（如 webfetch）位于 `plugins/tools/`，通过 `init()` 自注册。添加新的插件工具只需一个空白导入，无需修改组装代码。详见[插件系统](/docs/development/plugin-system)。

### Delegate 工具

`delegate` 工具使代理能够将专注子任务委派到具有隔离上下文的子循环中。这对于从新上下文受益的任务（研究、代码审查、起草）很有用，而不会污染父对话。

- 每个委派任务获得仅包含任务描述的新消息历史
- 多个任务通过 goroutine 并行运行，支持可配置并发度
- `delegate` 工具从子任务中排除以防止递归
- 委派任务输出截断为 ~4096 个 token，以避免膨胀父上下文
- 支持从带有 YAML 前置数据的 markdown 文件加载预设
- 每任务选项：`preset`、`context`、`model`（覆盖）、`system`（附加指令）、`tools`（白名单）、`max_turns`（默认 10）、`timeout_seconds`（默认 120）

### 内置共享工具

| 工具        | 条件                  | 描述                                           |
| ----------- | --------------------- | ---------------------------------------------- |
| `memory`    | 始终                  | 自动生成的内存工具（操作根据提供商能力自适应） |
| `skills`    | 始终                  | 技能管理（从 skills.sh 搜索/安装/列出/移除）   |
| `scheduler` | 始终                  | 安排任务（添加/列出/移除作业）                 |
| `notify`    | 网关模式 + 通道已配置 | 通过分发器发送通知                             |

内存工具由 `memory.BuildTool(provider)` 自动生成，它会检查提供商的能力并生成匹配的工具操作。使用 LCM 提供商时：`status`、`search`、`describe`、`expand`、`profile_get`、`profile_update`。使用 Simple 提供商时：`status`、`profile_get`、`profile_update`。每用户笔记通过 `profile_get`/`profile_update` 管理，并在会话开始时注入系统提示。

## 会话生命周期

1. 通道解析用户和代理，产生 `ResolvedChat`
2. 调用 `ResolvedChat.Pool.Chat(ctx, sessionKey, message)` -- message 是 `string`（文本）或 `[]ContentBlock`（多模态）
3. Pool 使用作用域键 `{agentID}:{platform}:{userID}:{context}` 查找或创建会话
4. Pool 获取或为会话创建 runner，使用代理的 Snapshot 配置
5. Runner 通过通道流回事件
6. 空闲超时后，runner 被回收；会话通过 `memory.Provider` 持久化到 SQLite

有关历史管理，请参阅[会话压缩](/docs/development/session-compaction)。

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

共享命令逻辑（`/new`、`/temp`、`/compact`、`/abort`、`/whoami`）位于通道协调层，每个通道委托给它以处理核心逻辑。`/model` 和 `/agent` 保持按通道处理，因为它们需要特定于平台的 UI（Telegram 使用内联键盘，QQ、Feishu 和微信使用文本列表，CLI 使用 TUI 选择器）。聊天轮次按解析的 Stella 会话进行序列化，因此重叠的通道消息不会竞争相同的会话历史；`/abort` 取消该会话当前正在运行的轮次。

## Admin API

`internal/server/` 包提供用于管理系统的 HTTP API 和嵌入式 SPA。端点涵盖提供商、代理、通道、用户、会话、调度器作业和全局设置的 CRUD 操作。server 通过 `config.Store` 读写，为操作员提供 Web 界面来配置之前通过 YAML 文件完成的配置。

## 通知流程

```
Agent notify tool      --> Dispatcher --> Channel (Telegram/QQ/Feishu/WeChat)
Scheduler job result   --> Dispatcher --> Channel (Telegram/QQ/Feishu/WeChat)
```

分发器在设置早期创建，但后端在网关服务启动时稍后注册。PoolManager 通过 `BuiltinToolsFactory` 按代理注入通知工具，把通知保留在始终启用的内建工具集合中，而外部工具继续由插件管理。
