---
title: 架构
---

> 本节面向为 Stella 贡献代码的开发者。

## 系统概述

stella 的结构是一组松耦合的包，在启动时组装在一起。系统支持多用户和多代理，消息路由按消息级别处理。核心流程：

1. 一个**通道**（CLI、Telegram、QQ、Feishu 或微信）接收用户输入。
2. 通道**解析用户**（通过外部 ID + 平台进行 upsert）和**解析代理**（DM 默认、群组绑定或回退）。
3. **ServiceManager** 通过代理 ID 查找该代理的 `agent.Service`。
4. `agent.Service` 通过 `session.Registry` 解析 session intent。
5. `runtime.Runtime` 通过缓存的 **Runner** 执行这一轮。
6. **Runner** 调用 LLM provider，并在循环中执行工具。
7. 响应通过通道流回给用户。

```
Channel (CLI / Telegram / QQ / Feishu / WeChat)
    |
    v
Resolve user  -->  Resolve agent
    |
    v
ServiceManager.GetService(agentID)  -->  agent.Service
    |                                      |
    |                                      +--> session.Registry
    |                                      |
    |                                      +--> runtime.Runtime --> Runner
    |                                                             |
    v                                                             v
Channel response stream                                      LLM Provider
```

会话键的作用域为每个代理：`{agentID}:{platform}:{userID}:{context}`，确保同一用户与不同代理对话时拥有独立的对话历史。session/runtime/memory 的设计规则见 [Agent 架构](/docs/development/agent-architecture)。

## 包布局

```
cmd/stellad/             入口点，服务器命令，服务组装
internal/
  config/              Store 接口、DBStore（PostgreSQL）、Snapshot、类型
  ai/                  Message/Content 类型、Model、Provider 接口、流式事件
  agent/               Service、ServiceManager、session registry、runtime、runner 工厂
    session/           Session 生命周期、ownership、kind/channel policy
    runtime/           Runner cache、turn 执行、event 持久化
    engine/            代理循环引擎（多轮工具执行）
    prompt/            系统提示构建器和模板
  channel/             Channel 接口、身份解析、斜杠命令、通知
    cli/               Bubble Tea TUI
    telegram/          Telegram 机器人
    qq/                QQ 机器人
    feishu/            飞书机器人
  admin/               HTTP API + 嵌入式 React SPA
  auth/                RBAC/ABAC 策略引擎、会话、沙箱
  db/                  PostgreSQL（pgx/v5）、goose 迁移、sqlc 查询
  scheduler/           River 持久化调度服务（供 Web UI、CLI 和 Agent 原生工具使用）
  skills/              技能工具（通过 skills.sh 搜索/安装/列出/移除）
pkg/
  memory/              Memory Provider 接口、类型、Summarizer、工具自动生成、测试辅助
  tools/               Tool 接口、注册表、内置工具（read、bash、write、edit、agent）
plugins/
  memory/              记忆插件注册表 + 实现
    lcm/               无损上下文管理（默认）— 摘要 DAG、压缩、搜索
    simple/            滑动窗口记忆 — 保留最近 N 条消息，无摘要
  tools/               插件工具注册表 + 插件工具（webfetch）
  hooks/               插件钩子注册表 + 插件钩子（rtk）
  channels/            通道插件（telegram、qq、feishu、weixin）
  providers/           供应商插件注册表 + LLM 适配器（anthropic、openai、openai-response）
```

## 配置

配置存储在 PostgreSQL 中，通过 `config.Store` 接口访问。没有 YAML 配置文件；所有设置（提供商、代理、通道、调度器）都通过 admin API 或数据库管理。

- **Store**（`config.Store`）-- 用于读写提供商、代理、通道、用户和聊天-代理绑定的接口。由 `DBStore` 实现。
- **DBStore**（`config.DBStore`）-- 使用 sqlc 生成的查询的 PostgreSQL 支持实现。
- **Snapshot**（`config.Snapshot`）-- 单个代理的只读配置视图。在池创建时从 Store 组装。包含已解析的提供商凭证、模型名称、工作区路径、系统提示和 runner 设置。传递给 runner 工厂和需要每个代理配置的工具。

## 组合与生命周期

`cmd/stellad` 是唯一的手动组合根。没有 DI 框架，也没有通用 `Lifecycle` 接口——各子系统在同一处显式构造和装配，使布线可审计。启动按严格阶段进行，每一阶段必须先于下一阶段完成：

1. **启动配置** — `serverAction` 在启动边界一次性解析 `config.LoadServerConfig(os.LookupEnv)` 与 `oidc.LoadLoginConfig(os.LookupEnv, baseURL)`。其它包一律不读环境变量（由测试三线闸强制，仅对 `STELLA_HOME`/OTel/运行时透传保留小白名单）。最终 base URL 在此解析并向下传递，共享服务直接用它构造——绝不用 `localhost` 占位符再事后改写。
2. **Build（构建）** — `setup()` 一次性构造每个子系统。共享的 credentials/email/share/recally/MCP 服务只建一次（每个域通过 `*ForPool` 构造子自持查询集），因此同一实例同时支撑 agent 工具与 HTTP 端点。
3. **Bind（绑定）** — 真正的反向边用一次性的预启动绑定闭合，拒绝 nil/重复/迟到绑定：PoolManager 的 `BindVaultEnvLoader`/`BindMCPToolProvider`/`BindOAuthRegistry`（在 `StartAll` 之前）、scheduler/goal/embedding 服务上共享 River 客户端的 `BindRiverClient`，以及 `AddBuiltinTool`（去重，由 `StartAll` 密封）。普通依赖走构造注入，不走绑定。
4. **Validate / Seal（校验/密封）** — `pluginhost.Seal()` 校验全部静态注册与能力绑定后拒绝进一步静态注册；动态期望态接口（`ApplyChannel`/`RegisterManifestPlugins`）保持开放。admin 服务由不可变、已校验的 `server.Deps` 经 `server.New(ctx, deps)` 构建，缺任一必需依赖即快速失败。`server.New` 不读环境、不构造服务、无 setter。
5. **可观测性** — 全局 OTel 追踪在服务阶段之前初始化，因此任何产生 span 的组件（经 HTTP/通道入口的 agent 运行）都不会在 exporter 装好之前启动。
6. **Run（运行）** — 至此组合根才启动入口，且必须在其依赖的所有后端就绪之后。先接好静态回调（`notifier.SetAuthService`、scheduler 的 `OnJob` 处理器——均为互斥保护的一次性写入），并启动 River、scheduler、goal 调度 tick、embedding backfill；scheduler 处理器在 River 启动**之前**接好，因为 River 一启动就可能处理已持久化的作业。之后入口才上线——group 调度循环、受管通道运行时，最后是 `httpSrv.Serve`（监听器提前绑定但不 serve）。组合根持有单个 `errgroup`：`httpSrv.Serve` 与 `groupDispatcher.Run(ingressCtx)` 在其下运行。预期的关停错误归一为 `nil`（`http.ErrServerClosed`、`context.Canceled`）；任何其它组件错误取消同伴并成为根错误。组件构造子不启动 goroutine——后台循环由显式阻塞式 `Run(ctx)` 或组合根拥有的 `Start` 进入（例如 trace hook 的空闲会话回收器）。

**不可变 Server Deps。** `server.Deps` 是按域分组的值结构体（持久化、授权、运行时、共享服务、可选能力）。它携带具体域服务，而非宽泛的影子 store；一个 reflection/AST 三线闸冻结剩余的宽持久化债（DB 池、`config.Store`、auth stores）并禁止新增宽字段。可选能力容忍 nil，退化为单一集中的 503 映射。

**授权。** Agent 的 HTTP、webhook 和 channel 入口统一使用权威的 `agentaccess` 策略执行服务。Session 与 Workspace 用例使用 `sessionaccess`：它先加载持久化的 owner、agent、kind 和生命周期事实，再创建带作用域的 registry 访问，并在一个绑定策略版本的 `authz.Authority` evaluation 中决定 Agent、Session 和 Workspace 请求；旧策略引擎不再有任何 Agent、Session 或 Workspace 决策路径。Authority 只能由可信身份适配器（`internal/auth`、`internal/credential`、`internal/authz`）铸造；请求 body/path 字段永远不能铸造或覆写 actor。

执行域采用相同形态：Workflow、Scheduler、Goal 由各自的域服务（`workflow.Service`、`scheduler.Service`、`goal.Service`）执行，Skills 由 `skillaccess` 执行，均取代了原先的 `Service.As(authz.Identity)` 门面与散落的 helper。每个传输层与工具用例只调用一次 `Begin`，并在该单一版本内决定域资源；跨资源的 agent 门禁通过 `agentaccess.AuthorizeWithin` 折叠进同一 evaluation。持久化 worker（goal attempt、触发的定时 workflow）从持久可信状态重建 owner/executor Authority，并在每次动作时重新决策。`admin` 通过内建的 `admin-full-access` 策略成为策略超级用户，而非散落的 `role == admin` 检查。

**静态 vs 动态。** 启动静态能力在启动前绑定一次并随后密封。热重配（插件工具/钩子/提供商重载、agent 同步、runner 失效）是独立接口，启动后仍可用并原子应用——绝不重跑一次性绑定。

**关停顺序。** 首个 `SIGINT`/`SIGTERM` 启动优雅排空（第二个坍缩为硬停）。`drainSequence` 依次：标记 `/readyz` 不就绪并通知 SSE 流 → **停止每一个非 HTTP 入口源**（group 调度受理、通道 bot 轮询、以及 scheduler/goal/embedding 的 River 周期任务与一次性派发），各由幂等的 stop-once 闭包完成，故排空开始后不再有新工作或周期触发 → 在 `STELLA_HTTP_SHUTDOWN_TIMEOUT` 内排空在途 HTTP（超时强制关闭）→ 取消工作上下文，随后 River 在软停预算内排空其在途作业，LIFO defer 链逆序 Close 各子系统。group 调度循环跑在独立的 `ingressCtx`（errgroup 上下文的子上下文）上，故可在不取消工作上下文的情况下被叫停；出站依赖（池、notifier）在最终取消前保持存活，故排空前已接受的工作仍能完成并投递。同一批 stop-once 闭包同时支撑 `stopIngress` 与逆序 defer 清理，故崩溃/启动错误路径也能安全拆除、不重复停止。子系统崩溃取消 errgroup 并在无就绪排空的情况下拆除。

## 多用户多代理路由

每条传入消息在到达代理循环之前都要经过两步解析：

1. **用户解析**（`channel.ResolveUser`）-- 通过外部平台 ID 对发送者进行 upsert，返回带有稳定内部用户 ID 的 `config.User` 记录。
2. **代理解析**（`channel.ResolveAgent`）-- 确定哪个代理处理此消息：
   - 在 DM 中，使用用户的 `default_agent_id`。
   - 在群聊中，`chat_agents` 绑定将 `(platform, chat_id)` 映射到代理。
   - 如果两者都未设置，则使用第一个启用的代理作为回退。

已解析的用户和代理被打包到 `ResolvedChat` 结构中，该结构贯穿所有处理器和命令路径。此结构包含目标 `Service`、`User`、`AgentID` 和 `SessionKey`。

`ServiceManager`（由 `PoolManager` 实现）维护 `map[agentID]*Service` 并在首次访问时延迟创建。每个 Service 通过 runner 工厂使用其代理的 `Snapshot`（模型、凭证、工作区、系统提示）进行配置。

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

| 工具       | 描述         |
| ---------- | ------------ |
| `webfetch` | 获取网页内容 |

核心本地工作区工具通过 Docker 沙箱后端运行。`bash` 工具通过 `Session.Exec` 执行；`read`、`write` 和 `edit` 工具使用 `Session.ResolvePath` 获取主机路径，然后直接调用 `os.*`。Runner 启动时如果 Docker 不可用则失败关闭。

### 沙箱

沙箱系统为 agent 工具执行提供进程、文件系统和网络隔离。所有核心工具在每个 runner 中共享同一个 `sandbox.Session`：`bash` 使用 `Session.Exec`；`read`/`write`/`edit` 使用 `Session.ResolvePath` + `os.*`。沙箱后端不可用时 runner 启动失败关闭。详见[沙箱后端抽象](/docs/development/sandbox)了解完整的 Session 接口、执行中介、拒绝失败行为和例外边界。

沙箱工具（bash、read、write、edit）位于 `internal/agent/sandbox/`；其他内置工具位于它们投射的能力包中（例如 delegate 位于 `internal/agent/delegate`）。插件工具（如 webfetch）位于 `plugins/tools/`，通过 `init()` 自注册。添加新的插件工具只需一个空白导入，无需修改组装代码。详见[插件系统](/docs/development/plugin-system)。

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

内存工具由 `memory.BuildTool(provider)` 自动生成，它会检查 provider 能力并生成匹配动作。普通聊天 runner 会用 `WithSessionReadOnlyWrites()` 收窄它：使用 LCM provider 时暴露 `status`、`search`、`describe`、`expand`、`get_message`、`profile_get`、`soul_get`、`profile_history` 和 `constraint_list`；Simple provider 暴露对应的只读子集。持久 profile/soul/constraint 写入由 Reflect 或 UI/API/CLI 等 manual 路径完成，并注入新会话的系统提示。

## 会话生命周期

1. 通道解析用户和代理，产生 `ResolvedChat`
2. 调用 `ResolvedChat.Chat(ctx, message)` -- message 是 `string`（文本）或 `[]ContentBlock`（多模态）
3. `Service.Chat` 通过 `session.Registry` 使用作用域键解析或创建会话
4. `runtime.Runtime` 获取或为会话创建 runner，使用代理的 Snapshot 配置
5. Runner 通过通道流回事件
6. 空闲超时后，runner 被回收；会话通过 `memory.Provider` 持久化到 PostgreSQL

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

共享命令逻辑（`/new`、`/compact`、`/abort`、`/whoami`）位于通道协调层，每个通道委托给它以处理核心逻辑。`/model` 和 `/agent` 保持按通道处理，因为它们需要特定于平台的 UI（Telegram 使用内联键盘，QQ、Feishu 和微信使用文本列表，CLI 使用 TUI 选择器）。聊天轮次按解析的 Stella 会话进行序列化，因此重叠的通道消息不会竞争相同的会话历史；`/abort` 取消该会话当前正在运行的轮次。

## Admin API

`internal/server/` 包提供用于管理系统的 HTTP API 和嵌入式 SPA。端点涵盖提供商、代理、通道、用户、会话、调度器作业和全局设置的 CRUD 操作。server 通过 `config.Store` 读写，为操作员提供 Web 界面来配置之前通过 YAML 文件完成的配置。

## 通知流程

```
Agent notify tool      --> Dispatcher --> Channel (Telegram/QQ/Feishu/WeChat)
Scheduler job result   --> Dispatcher --> Channel (Telegram/QQ/Feishu/WeChat)
```

分发器在设置早期创建，但后端在网关服务启动时稍后注册。ServiceManager 通过 `BuiltinToolsFactory` 按代理注入通知工具，把通知保留在始终启用的内建工具集合中，而外部工具继续由插件管理。
