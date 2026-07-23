# Stella 全量能力与 Release 测试方案

## 文档状态

本文定义 Stella 在发布新版本前进行全量能力验证的总体方案。

以下方向已经确认：

- 工作分三步推进：先列出所有能力，再盘点已有测试方法，最后设计如何完整测试并尽量自动化。
- 能力清单分为核心能力与外围入口/扩展集成，避免把 Web、API、CLI 等入口重复计算成功能。
- 本文设计的是独立于日常开发 CI/CD 的 Release Full Test，只在发布新版本时执行。
- 每次执行都按同一份 `release_policy` 处理全部已登记场景，不根据版本大小、改动范围或时间计划筛选场景；非阻塞和手工结果必须报告，不能伪装成自动通过。
- Release Full Test 以自动化、可重复、可诊断为目标，但不要求每项能力都通过浏览器测试。
- 核心能力使用不依赖真实第三方凭据的自动化测试；外部集成同时使用 Contract Test 和 Live Smoke。
- 机器可读能力清单在首次落地时建立，之后作为仓库内的长期版本化文件随产品增量维护；每次发布重新提取当前公开表面做一致性校验，不自动重写或重建清单。
- 已有测试证据在首次落地时集中盘点并写入能力清单，之后随测试变更增量维护；每次发布只校验引用有效性并执行当前登记场景，不从零重新盘点。
- Scenario 使用 `layer` 表达最低充分测试边界、`status` 表达当前证据状态、`release_policy` 表达发布处置，避免把测试层级、实现进度和门禁策略混在一个枚举中。
- Tag Release workflow 必须先通过独立 Validate Job，GoReleaser 和 Docker 发布 Job 才能启动；该门禁只属于 Release，不接入日常 PR/Main CI。

本文以 GitHub `main` 的提交
[`60435d5d84a093ef5647b720ce23193464fdc943`](https://github.com/CherryHQ/stella/commit/60435d5d84a093ef5647b720ce23193464fdc943)
为盘点基线。首次建立机器可读能力清单前，必须基于当时最新 `main` 重新提取能力表面，吸收基线之后新增、删除或改名的能力；清单建立后不在每次发布时重新生成，而由表面校验暴露未同步的变更。

本文主体保留已确认的总体方案和最终目标；当前实施进展单独记录在“第一部分实施基线”，具体拆分见 [implementation.md](./implementation.md)。

### 第一部分实施基线

第一部分已在 `upstream/main` 的
[`8e454a0112d51e0cde7e81ffd3ba2df0fb69672d`](https://github.com/CherryHQ/stella/commit/8e454a0112d51e0cde7e81ffd3ba2df0fb69672d)
上完成首次机器可读盘点：

- `test/capabilities.yaml` 登记 19 个核心能力、19 个扩展能力和 78 个初始验收场景。
- 当前实现提取并核对 240 个 OpenAPI Operation、58 个唯一 Web URL、17 个 CLI Command、13 个内置 Plugin 和 10 个 System Skill；其中 3 个 Web URL 是有理由的非产品路由豁免。
- 盘点 14 类已有测试资产，并从实施工具自身之外重新确认 473 个 Go Test 文件、0 个前端测试文件、10 个 System Test 文件，以及 `httptest`、`dbtest`、`memorytest` 的实际使用数。
- 当前 25 个场景为 `covered`、18 个为 `partial`、32 个为 `missing`、3 个为 `manual-only`，共形成 53 个第二部分待补缺口；已有场景登记 107 条直接测试证据。
- 78 个场景中，65 个为 `blocking`、11 个为 `nonblocking`、2 个为 `manual`；当前缺口分为 40 个 Blocking Gap、11 个 Nonblocking Backlog 和 2 个 Manual Requirement，并登记 13 个仍需在真实外部边界验证的 Live Scenario。
- `mise run test:capabilities` 使用独立 `capability` build tag 校验清单、真实公开表面和测试引用，并生成 `dist/test-results/capabilities/inventory.{json,md}`；日常 `go test ./...` 不执行该盘点套件。

这些状态是第一部分的覆盖基线，不是 Release Full Test 已通过的结论。Live Target 注册、缺口测试、统一 Runner 和最终 `release_allowed` 门禁属于第二部分。

### 第二部分已确认规则

- 第二部分第 0 步先在 Tag Release workflow 增加确定性 Validate Job，至少执行能力清单校验、普通测试和现有 System Test；发布 Job 使用 `needs` 等待门禁通过。
- Browser E2E 首批覆盖 C02 登录、C07 Chat SSE、C05 Agent 管理、C06 Provider Secret、C17 公开 Share 和 X02 Channel 管理六条浏览器独有或高风险旅程。其余 Browser 候选必须先判断低层测试能否充分证明行为，不能长期作为未解释的发布缺口。
- X07 Webhook、X09 MCP、X15 Docker、X18 发布物/kind/外部 PostgreSQL、X19 OTLP 的目标可由测试自托管或装入测试环境，因此改为确定性阻塞测试，不再作为 Live Smoke。
- 自动化 Live 的 `Product Failure` 阻止发布；`External Blocked`、`Not Run` 和重试后通过的 `Flaky` 不能算 Pass，必须生成诊断并经过显式人工 waiver 才能继续。
- X06 微信真实收发和 X13 真实 IdP 登录当前保持 `manual-only`，原因是缺少合规稳定的自动化身份；这是当前状态而非永久放弃自动化，将来具备受控目标后应升级。
- 第一部分已实现 `release_policy` Schema、Scenario 重分类、X06/X13 手工 Runbook 和分类报告；Tag workflow、业务测试、Browser/Live Runner、真实 Target 和最终聚合门禁仍属于第二部分，不能因清单规则已落地而视为 Release Full Test 已完成。

## 问题与目标

Stella 已有大量 Go 单元测试和进程内集成测试，也已经具备启动真实 `stellad`、嵌入式 PostgreSQL、HTTP、SSE 和 fake Anthropic 的 System Test。当前缺少的是覆盖全产品的统一视图：无法直接回答有哪些能力、每项能力由什么测试证明、哪些发布前仍未验证。

现状带来几个风险：

- 新功能可能有包级测试，但没有从用户入口验证完整链路。
- 测试数量和代码覆盖率较高，不代表每项产品能力都有验收证据。
- API、Web 路由、CLI、插件和外部集成分别演进，缺少统一的防漂移机制。
- `release:validate` 是本地约定；Tag 触发的发布工作流没有强制重复执行完整门禁。
- 浏览器测试文档目前是 `tap browser` 操作手册，不是仓库内可版本控制、可报告、可由 CI 执行的测试套件。

目标是建立一套可持续的能力验收体系：

1. 为每项稳定产品能力分配唯一 ID，并记录它的入口、来源和外部依赖。
2. 为每项能力关联已有测试，明确当前覆盖层级和缺口。
3. 为每项能力定义目标场景和最低充分测试层级。
4. 让确定性测试和真实外部服务 Live Smoke 通过一个 Release 命令完整运行。
5. 让失败结果包含足够的日志、Trace、请求记录和测试数据标识，无需先复现才能定位。
6. 让能力清单与 OpenAPI、Web 路由、CLI 和插件注册表之间的漂移能够自动暴露。

这里的“全量测试”定义为：

> 每项已登记能力都定义完整的适用验收场景集合，所有场景在能够证明该行为的最低充分层级自动执行；关键用户旅程再增加跨层 E2E。

它不等于把 240 个 API 操作各写一条浏览器测试，也不等于只追求代码行覆盖率。

## 范围与非目标

### 当前范围

- `stellad` 启动、数据库、迁移、健康检查和退出。
- 本地认证、用户、权限、PAT 和 OAuth 客户端。
- Agent、Session、Chat、Workspace、Project、Goal、Workflow 和 Scheduler。
- Memory、Reflect、Skill、Group、Inbox、通知、Share 和 Vault。
- Web UI、HTTP API 和 `stellad` CLI 三类第一方入口。
- Channels、Email、MCP、插件、Recally、模型 Provider、Embedding、Sandbox 和远程 Skill 来源。
- 二进制、Service、Docker、Helm 等发布和部署表面。
- 现有 Go 测试、System Test、API 手工联调、浏览器手工自动化和发布校验任务。

### 当前非目标

- 第一部分不补业务测试、fixture、fake、Browser/Live Runner 或日常 CI workflow；本阶段只建立长期清单、证据映射、表面校验和报告。
- 不设计或替代日常开发使用的 PR/Main CI/CD 流程，也不要求开发阶段运行本套全功能测试。
- 不为不同版本、改动范围或时间计划设计多档测试策略；每次启动本套件都执行完整集合。
- 不要求每项功能都经过浏览器层；无 UI 的行为在更合适的层级验证。
- 不把视觉回归扩展到所有页面；截图仅用于布局和主题确实属于验收条件的场景。
- 不用代码覆盖率替代能力覆盖率。
- 不逐字断言 Prompt、Skill 文案或模型自然语言输出；只验证稳定的协议字段、工具调用和可观察结果。
- 不在方案阶段确定最终测试执行时长预算；先取得实测数据，再设置门禁预算。

## 约束与已知事实

### 产品表面

基线提交中可以识别到：

- 26 个 OpenAPI 功能域、240 个 HTTP 操作，来源为
  [`api/spec/domain`](https://github.com/CherryHQ/stella/tree/60435d5d84a093ef5647b720ce23193464fdc943/api/spec/domain)。
- 58 个 TanStack Router 路由声明，来源为
  [`web/src/routes`](https://github.com/CherryHQ/stella/tree/60435d5d84a093ef5647b720ce23193464fdc943/web/src/routes)。
- `stellad` 的 Server、Version、Upgrade、PostgreSQL、Vault、mise 和 Service 命令，来源为
  [`cmd/stellad`](https://github.com/CherryHQ/stella/tree/60435d5d84a093ef5647b720ce23193464fdc943/cmd/stellad)。
- Feishu、QQ、Telegram、Webhook 和 Weixin 五种 Channel 插件。
- Anthropic、OpenAI 和 OpenAI Responses 三种模型 Provider 插件。
- Local、Docker 和 None 三种 Sandbox 后端。
- Email、HTML Artifact、Lark CLI、Python Script、Recally、Scheduler、Skill Creator、Stella、Tap Web 和 Xberg 等内置 System Skill。

OpenAPI 操作、Web 路由和 CLI 命令是能力盘点的输入，不直接等同于能力数。一个“创建 Agent”能力可以同时具有 Web、API 和 CLI 入口，但只登记一次。

### 现有测试基础

基线提交包含 473 个 Go `_test.go` 文件。数量说明仓库已经有广泛的包级测试，但不能证明完整用户旅程。

当前正式 System Test 位于
[`test/system`](https://github.com/CherryHQ/stella/tree/60435d5d84a093ef5647b720ce23193464fdc943/test/system)，通过 `system` build tag 隔离，执行：

```bash
mise run system-test
```

它启动真实 `stellad` 子进程，使用真实 TCP、HTTP、SSE、嵌入式 PostgreSQL 和脚本化 fake Anthropic。当前有六条有序旅程：

1. `readiness`
2. `startup_and_auth`
3. `chat_sse`
4. `chat_provider_error`
5. `goal_lifecycle`
6. `graceful_drain`

方案盘点基线 `60435d5d84a093ef5647b720ce23193464fdc943` 中的 `mise run release:validate` 依次运行：

```text
format
-> build
-> test
-> system-test
-> release:check
-> release:snapshot
```

System Test 被定位为本地 Release Gate，普通 CI 没有执行它。Tag 触发的 Release workflow 直接进入构建和发布，因此本地漏跑门禁时，远端没有第二道强制校验。

### 外部依赖

- 真实模型、Channel、邮件、OAuth/OIDC、MCP、Embedding、Feed 和对象存储受到网络、凭据、费用、限流和第三方状态影响。
- Release Full Test 的确定性部分必须离线、可重复且不需要个人凭据；Live Smoke 部分使用受控的测试专用凭据和真实服务。
- Contract Test 与 Live Smoke 都属于完整发布验收，但必须分开报告，便于区分产品缺陷和外部环境问题。
- 已登记的真实外部目标缺少凭据、环境不可用或没有实际执行时不能判定为 Pass；阻塞场景必须取得显式、可审计的人工 waiver 才能继续发布。
- 异步 Worker、SSE 和调度任务必须使用有截止时间的轮询或事件等待，不能依赖固定 `sleep` 判断成功。

## 能力清单规则

### 术语

| 术语              | 定义                                                                 |
| ----------------- | -------------------------------------------------------------------- |
| 能力              | 用户、管理员或外部系统能够完成的一项稳定任务                         |
| 入口              | Web、API、CLI、Channel、后台 Worker 等触发能力的方式                 |
| 场景              | 对一项能力进行验收的 Given/When/Then 行为                            |
| 测试方法          | 单元、集成、System、Browser、Contract 或 Live Smoke 等证明场景的层级 |
| Release Full Test | 发布新版本时运行的完整能力验收集合，不属于日常开发 CI/CD             |

### Scenario 字段语义

| 字段             | 回答的问题                           | 允许值或示例                                                   |
| ---------------- | ------------------------------------ | -------------------------------------------------------------- |
| `layer`          | 这个行为应在哪个最低充分边界得到证明 | `unit`、`integration`、`system`、`browser`、`contract`、`live` |
| `status`         | 当前已经取得什么证据                 | `covered`、`partial`、`missing`、`manual-only`                 |
| `release_policy` | 发布时这个场景如何影响最终结论       | `blocking`、`nonblocking`、`manual`                            |

`status` 会随测试实现变化，`release_policy` 是发布标准。复合说法拆成独立字段表达：

- Browser 暂缓项使用 `layer: browser`、当前实际 `status` 和 `release_policy: nonblocking`。
- 真实外部服务的非阻塞 Smoke 使用 `layer: live` 和 `release_policy: nonblocking`。
- 当前只能人工验证的行为使用实际边界，例如 `layer: live`，同时设置 `status: manual-only` 和 `release_policy: manual`。
- 尚未实现但最终必须进入发布门禁的行为使用 `status: missing` 和 `release_policy: blocking`；在最终聚合门禁启用前必须补齐。

### 归并规则

- 同一业务行为只登记一次，不按入口重复计数。
- CRUD 操作在业务含义一致时归为一个能力组，但测试场景仍覆盖创建、读取、更新、删除和权限边界。
- Provider 配置管理属于核心控制面；访问真实 Provider 属于扩展集成。
- Plugin 管理框架属于扩展控制面；每个具体 Plugin 的协议行为单独登记。
- 本地 Skill 管理属于核心能力；远程市场搜索、安装和升级属于扩展集成。
- Web 页面只作为能力入口；纯布局和导航可另建少量 UI 场景，不把每个路由都算成独立能力。
- 所有 OpenAPI `operationId`、Web 路由、CLI Command 和内置 Plugin 必须能追溯到至少一个能力 ID，或者显式标注为内部/非产品表面并说明理由。

## 第一步：完整能力清单

### 核心能力

| ID  | 功能域                | 能力范围                                                                                                                                               | 主要入口                          |
| --- | --------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------ | --------------------------------- |
| C01 | 运行时与存储          | 启动服务、嵌入式/外部 PostgreSQL、自动迁移、资源同步、状态与版本、Readiness、优雅退出                                                                  | CLI、HTTP、Process                |
| C02 | 本地认证              | 首位用户注册、登录、退出、当前用户、认证方式发现、登录 Session 列表与撤销、修改密码                                                                    | Web、API                          |
| C03 | 用户与权限            | 用户列表/详情、启停、角色、默认 Agent、Agent 分配、登录/Channel Identity、通知身份、管理员查看和维护用户 Memory                                        | Web、API                          |
| C04 | PAT 与 OAuth 客户端   | Token Scope、PAT 创建/查看/撤销、OAuth2 Client 注册/禁用/密钥轮换、授权应用查看/撤销                                                                   | Web、API                          |
| C05 | Agent 管理            | Agent 增删改查、用户授权、工具可见性、Tool Override、Agent 上下文 Skill                                                                                | Web、API                          |
| C06 | Provider/Model 控制面 | Provider Type、Provider 配置增删改查、模型列表和模型缓存                                                                                               | Web、API                          |
| C07 | Session 与 Chat       | Session 增删改查、消息历史、SSE 消息发送、实时事件订阅、System Prompt、Context Item、Compaction Summary                                                | Web、API、Channel                 |
| C08 | Session Workspace     | 文件列表、读取、写入、新建、移动、删除、上传和空间用量                                                                                                 | Web、API、Agent Tool              |
| C09 | Project               | Project 增删改查，以及 Project Scope 下的 Session、Skill、Memory 和 Task                                                                               | Web、API                          |
| C10 | Goal/Task             | 创建、编辑、归档/恢复、激活、取消、放弃、重试、子 Goal、依赖边、Waive、Readiness、Attempt、Timeline、Plan 审批、Acceptance Event、人工 Verdict、Health | Web、API、Worker、Agent Tool      |
| C11 | Workflow              | 从已验收 Goal 保存 Workflow、列表、详情、删除、实例化和 Run 记录                                                                                       | Web、API、Agent Tool              |
| C12 | Scheduler             | Job 增删改查、立即运行、Run 记录、Job Template 和持久化后台调度                                                                                        | Web、API、Worker、Agent Tool      |
| C13 | Memory 与 Reflect     | User-Agent Memory、Soul、Constraint、Knowledge 创建/替换/废弃/恢复、Changelog、Session Compaction、后台 Reflect/Reconciliation                         | Web、API、Worker、Agent Tool      |
| C14 | 本地 Skill            | 多 Scope Skill 增删改查、文件读写删除、ZIP 上传、Agent/Project Skill 可见性                                                                            | Web、API、Agent Runtime           |
| C15 | Group 协作            | Group 增删改查、Agent Member、Group Message History、SSE Group Chat 和多 Agent 调度                                                                    | Web、API、Worker                  |
| C16 | Inbox 与通知          | Attention Inbox、Agent Notify Tool、用户通知 Identity                                                                                                  | Web、API、Agent Tool、Channel     |
| C17 | Artifact Share        | Share Link 创建、列表、撤销、无认证公开访问，以及 Agent Share Tool                                                                                     | Web、API、Public Link、Agent Tool |
| C18 | Vault 与 Secret       | 多 Scope Secret 增删改查、加密存储、Agent Vault Tool                                                                                                   | Web、API、Agent Tool              |
| C19 | 内置资源与工具        | Builtin Resource 发现，以及 Memory、Notify、Goal、Scheduler、Workflow、Share、Vault 等内置工具的装配和可用性                                           | API、Agent Runtime                |

### 外围入口与扩展集成

| ID  | 功能域                | 能力范围                                                                                                                             | 主要入口/依赖                   |
| --- | --------------------- | ------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------- |
| X01 | `stellad` CLI         | `server`、`version`、`upgrade`、PostgreSQL Runtime 下载、Vault Keygen、mise Builtin Reconcile、Service 安装/卸载/启停/重启/状态/日志 | CLI、OS Service、GitHub Release |
| X02 | Channel 公共能力      | Channel 增删改查、公开 Channel 列表、Inbound/Outbound Message、Identity Routing、Streaming Reply、Runtime Lifecycle                  | API、Plugin、外部平台           |
| X03 | Feishu                | PersonalAgent 注册、群聊/Thread、Reference、Reaction、Attachment 和 Streaming                                                        | Feishu API/WebSocket            |
| X04 | QQ                    | Bot 配置、Inbound/Outbound Message、Render 和 Streaming                                                                              | QQ Bot API                      |
| X05 | Telegram              | Bot 配置、Agent Routing、Inbound/Outbound Message、Render 和 Streaming                                                               | Telegram Bot API                |
| X06 | Weixin                | QR 登录、iLink Bot 注册、Media、Message Guard、Inbound/Outbound Message 和 Streaming                                                 | Weixin/iLink                    |
| X07 | Webhook Channel       | Webhook 配置和消息交付                                                                                                               | HTTP Endpoint                   |
| X08 | Email                 | Account、Folder、Message List、Read、Mark Read/Unread、Send，以及 Agent Email Tool                                                   | IMAP/SMTP 或 Provider API       |
| X09 | MCP                   | Scoped MCP Server 注册/更新/删除、Tool Discovery、Namespacing 和 Invocation                                                          | MCP Server                      |
| X10 | Plugin/Extension      | Plugin 列表、启停、Config、Schema、Status、Manifest Plugin 保存/同步、mise Registry、RTK Hook、WebFetch Tool                         | Web、API、Plugin Runtime        |
| X11 | Recally               | Feed 增删改查和 Poll、Entry 状态、Article 保存/搜索/编辑、Daily/Stored Digest、Agent Tool 和 Scheduler Template                      | Web、API、HTTP Feed、Worker     |
| X12 | 模型 Provider         | Anthropic、OpenAI、OpenAI Responses 的 Model Discovery、Streaming、Tool Call 和 Error Mapping                                        | 外部模型 API                    |
| X13 | OAuth/OIDC Connection | Provider 管理、Device Flow、Poll、Callback、Connect/Disconnect Identity、Link Code 和 OIDC Login                                     | 外部 Identity Provider          |
| X14 | Embedding 与语义检索  | Embedding Setting、外部 Embedding 请求、Backfill、Index 和 Semantic Query                                                            | 外部 Embedding API、Worker      |
| X15 | Sandbox               | Local、Docker、None 后端，Process/Container Lifecycle、Permission、Tool Cache 和 Preflight                                           | OS、Docker                      |
| X16 | 远程 Skill 来源       | ClawHub Search/Detail、MCPHub Search、Remote Install/Upgrade                                                                         | 外部 Marketplace/HTTP           |
| X17 | 内置 System Skill     | Email、HTML Artifact、Lark CLI、Python Script、Recally、Scheduler、Skill Creator、Stella、Tap Web、Xberg 的发现和注入                | Agent Runtime、可选外部 CLI     |
| X18 | 发布与部署            | Binary Archive、Upgrade、Service Install、Docker Image、Helm Chart、External PostgreSQL 和部署后 Smoke                               | GoReleaser、Docker、Kubernetes  |
| X19 | Observability         | Structured Log、Trace Hook、OpenTelemetry Export 和失败诊断                                                                          | Log、OTLP Backend               |

### 第一步的交付标准

最终能力清单不能只存在于本文。第一部分已增加机器可读的 `test/capabilities.yaml`，作为能力覆盖的唯一索引，并在不重建清单的前提下为每个 Scenario 增加 `release_policy`。

目标结构示例如下：

```yaml
capabilities:
  # 能力 ID 一旦被 Release Full Test 引用，不因目录重构随意改名。
  - id: C07
    class: core
    name: sessions-and-chat
    surfaces:
      # 所有公开 operationId 必须被能力清单覆盖。
      openapi: [createSession, sendSessionMessage, streamSessionEvents]
      web_routes: [/sessions, /agents/$agentId/sessions/$sessionId]
    scenarios:
      - id: C07-S02
        name: real-sse-chat-and-provider-error
        layer: system
        status: covered
        release_policy: blocking
        evidence:
          - kind: system_test
            path: test/system/system_test.go
            test: TestSystem
            subtest: chat_sse
            direct: true
```

清单校验至少保证：

- Capability ID 和 Scenario ID 唯一。
- 240 个 OpenAPI `operationId` 均被映射或显式豁免。
- 58 个 Web Route、全部 CLI Command 和内置 Plugin 均被映射或显式豁免。
- 每个 Scenario 必须声明 `layer`、`status` 和 `release_policy`；`covered` 必须引用真实存在的直接证据，`manual-only` 必须引用 Runbook。
- 能够合规自动化的真实外部集成必须映射测试专用 Live Target；当前不能自动化的场景必须登记 Manual Requirement、原因和升级条件，不能靠“不配置”绕过。
- 删除或改名公开表面时，能力清单同步更新；遗漏会导致能力清单校验失败。
- 生成的人类可读报告来自该清单，不再维护第二份手工覆盖表。

## 第二步：已有测试方法

| ID  | 方法                      | 入口                                         | 能证明什么                                                                | 自动化现状             | 主要限制                                                  |
| --- | ------------------------- | -------------------------------------------- | ------------------------------------------------------------------------- | ---------------------- | --------------------------------------------------------- |
| T01 | Format/Generate/Build     | `mise run format`、`generate:check`、`build` | 格式、生成物无漂移、编译与嵌入资源                                        | CI/Release 已有        | 不证明运行时行为                                          |
| T02 | Go Unit Test              | `go test`、`mise run test`                   | 纯逻辑、状态机、转换、错误映射、权限规则                                  | 广泛存在               | 不能证明真实进程、网络和浏览器                            |
| T03 | In-process Integration    | `mise run test`                              | Go Service/Handler 与真实 PostgreSQL 的组合行为                           | 已自动化               | 不经过真实 `stellad` 子进程和 TCP                         |
| T04 | Race/Coverage             | `mise run test:coverage:race`                | 数据竞争和代码行覆盖                                                      | 普通 CI 已运行         | 执行慢；覆盖率不是能力覆盖率                              |
| T05 | System Test               | `mise run system-test`                       | 真实 Binary、Migration、HTTP Auth、SSE、跨请求流程、异步 Worker、Shutdown | 已有六条 Journey       | 无浏览器；只覆盖少量能力；当前不进 CI                     |
| T06 | Manual API Integration    | `api-test.md` 的 `curl -> API -> DB`         | 运行服务上的探索性 API 和数据库不变量                                     | 手动                   | 无统一 fixture、报告和回归门禁                            |
| T07 | `tap` Browser Runbook     | `web-ui-test.md` 的 `tap browser` 命令       | 页面文本、交互、URL、网络和截图                                           | 手动/Agent 驱动        | 不是仓库内测试 Runner；无 Suite、Report、Trace 和 CI Task |
| T08 | Plugin/Fake/Contract Test | 各 `plugins/*` 和 `internal/*` 包测试        | 第三方协议转换、消息渲染、Runtime、Sandbox Contract                       | 已有较多测试           | 通常不证明真实第三方账户与完整进程链路                    |
| T09 | Release/Package Check     | `release:check`、`release:snapshot`          | GoReleaser 配置和 Host Archive 中存在 Binary                              | 本地 Release Gate 已有 | 不证明解压后启动、升级、Docker/Helm 部署成功              |
| T10 | Live Smoke                | 人工连接真实 Channel/Provider/Email 等       | 真实凭据、网络和第三方兼容性                                              | 无统一套件             | 不稳定、昂贵、需要 Secret，不能作为唯一门禁               |

### 已有测试框架、辅助库与运行工具

下面按基线提交中的实际引用和任务配置整理。“仓库中可用”不等于“已经形成全功能自动化测试”：测试框架、测试辅助设施、任务编排、质量门禁和手工工具分别标注，避免把构建或代码检查误算成功能验收。

| 类别                | 已有框架或工具                                                                                       | 基线中的使用证据                                                                                                          | 当前用途与结论                                                                                                                                      |
| ------------------- | ---------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------- |
| Go 测试框架         | Go 标准库 `testing`                                                                                  | 473 个 Go `_test.go` 文件，广泛使用 Test、Subtest 和 `TestMain`                                                           | 当前最主要的自动化测试框架，承载 Unit、进程内 Integration 和部分 Contract Test                                                                      |
| HTTP 测试辅助       | `net/http/httptest`                                                                                  | 35 个测试文件直接使用                                                                                                     | 通过 Recorder 或本地 HTTP Server 验证 Handler、OAuth/OIDC、Channel、Feed、WebFetch 和 Provider 协议；属于进程内 HTTP 测试，不等于真实 `stellad` E2E |
| 数据库集成 Harness  | `internal/db/dbtest`、embedded PostgreSQL、pgx                                                       | 57 个测试文件引用 `dbtest`                                                                                                | 每个测试二进制启动一次嵌入式 PostgreSQL，迁移模板库，再为测试克隆隔离数据库；可以验证真实事务、提交和并发，是现有的重要 Integration 基础设施        |
| Memory 测试 Harness | `internal/memory/memorytest`                                                                         | 22 个测试文件引用                                                                                                         | 提供内存 Fake 和可复用 Provider Conformance Suite，用于验证不同 Memory 实现的一致行为                                                               |
| 专项测试辅助        | 包内 Fake/Stub/Recorder、OpenTelemetry `tracetest`、`testing/fstest`、`os/exec`                      | 分散在各 Go 包测试中                                                                                                      | 已能覆盖协议记录、Trace、虚拟文件系统和子进程等专项行为，但目前没有统一的 Mock 框架或跨域 Fixture 层                                                |
| System Test Harness | `test/system`、`system` build tag、脚本化 fake Anthropic                                             | `mise run system-test`；10 个 System Test 文件、6 条有序 Journey                                                          | 自研的真实进程 Harness，覆盖真实 Binary、TCP、HTTP/SSE、PostgreSQL、认证、Worker 和退出；这是现有最接近后端 E2E 的框架，应继续扩展而不是另起一套    |
| Docker Contract     | Docker Sandbox 的 Go Contract Test 与真实 Docker daemon                                              | `plugins/sandbox/docker/contract_test.go`                                                                                 | 使用项目 Docker Client 直接验证容器创建、执行、完成和清理；环境没有 Docker 时会 Skip，尚未形成跨平台发布结果矩阵                                    |
| 任务编排            | `mise` 与 `mise.toml`                                                                                | 已有 `test`、`test:coverage`、`test:race`、`system-test`、`release:check`、`release:snapshot`、`release:validate` 等 Task | 是现有测试与发布门禁的统一命令入口，适合继续承载 Release Full Test；它本身不是测试框架                                                              |
| 覆盖与并发检查      | `go test` Coverage、Race Detector                                                                    | 普通 CI 运行 `mise run test:coverage:race` 并保存覆盖报告                                                                 | 能发现数据竞争并提供代码行覆盖率，不能代替 Capability/Scenario 覆盖                                                                                 |
| 构建与质量工具      | `generate:check`、prek、dprint、golangci-lint、goose validate、GoReleaser check/snapshot、Helm check | `mise.toml`、Pre-commit 配置和相关脚本                                                                                    | 验证生成物、格式、静态质量、Migration、打包和 Chart；这些是必要发布门禁，但除专门运行时检查外不应标成功能测试通过                                   |
| 自动化执行环境      | GitHub Actions                                                                                       | `lint-and-test.yml` 运行生成检查、格式与 Go Race/Coverage；Tag Release workflow 负责构建发布                              | 已有普通 CI/CD 执行环境，但当前不运行 System Test，也没有在发布前强制执行完整 `release:validate`                                                    |
| 浏览器探索工具      | `tap browser`                                                                                        | `web-ui-test.md` 中的命令 Runbook                                                                                         | 可供人或 Agent 操作页面、检查 URL/网络并截图；没有仓库内版本化 Suite、独立 Runner、Trace 和统一报告，因此不能称为现有 Browser E2E 框架              |
| API 探索工具        | `curl`、数据库查询和 `api-test.md` Runbook                                                           | 开发规则中的手工联调流程                                                                                                  | 可验证运行服务和数据库不变量，适合探索与复现；没有统一 Fixture、结果格式和回归 Runner                                                               |
| Reflect 模型评估    | `reflecteval` build tag 和 opt-in 环境开关                                                           | `candidate_eval_manual_test.go`、`reconciliation_eval_manual_test.go`                                                     | 能使用真实模型执行候选与 reconciliation 评估并输出诊断报告；依赖 Provider，当前是手工 opt-in Harness                                                |

### 基线中尚未形成的测试能力

- Web 工程没有测试脚本、前端 `*.test.*`/`*.spec.*` 测试文件，也没有 Playwright、Cypress 或 Vitest 配置；因此不能把 `tap browser` 或 Web 构建/typecheck 记成已有 Browser E2E Suite。
- Go 测试没有直接引用 Testify、Ginkgo/Gomega 或 Testcontainers。`testify` 只作为间接依赖出现，Docker Contract 直接使用项目 Docker Client；不能把这些库列为项目已采用的测试框架。
- 方案基线没有统一的 Capability-to-Test 映射、跨 Runner 结果协议、Browser Trace/Report 或 Live Smoke Runner；第一部分现已建立 Capability-to-Test 映射和清单报告，后三项仍属于第二部分缺口。
- 现有 GitHub Release workflow 没有调用完整发布门禁；“本地存在 `release:validate`”与“发布流程已强制执行”必须分别记录。

### 当前高层覆盖

System Test 当前主要证明：

- C01 的启动、迁移、Readiness 和 Graceful Drain。
- C02 的注册、Session Authentication。
- C04 的 PAT 创建、认证和撤销。
- C07 的 Chat SSE 与 Provider Error 终止语义。
- C10 的一条 Goal 自动执行和验收主路径。

以下能力虽然通常有 Go 测试，但当前没有完整 System 或 Browser 验收：

- Agent 管理、用户权限管理和 Provider 管理页面。
- Workspace、Project、Workflow、Scheduler、Memory、Skill。
- Group、Inbox、Notification、Share、Vault。
- 全部真实 Web UI 用户旅程。
- CLI 管理流程、Channel、Email、MCP、Recally、OAuth/OIDC、Embedding。
- Plugin 配置和 Reload、Sandbox 用户链路。
- Binary Upgrade、Service Install、Docker 和 Helm 部署后 Smoke。

### 第二步的交付标准

- 首次实施时完成一次集中盘点，将框架、工具、直接测试证据和覆盖状态写入长期维护的清单；后续只随功能或测试变更增量更新，不要求每次 Release 从零扫描并人工重建。
- 每个 Capability 至少记录所有直接相关的现有测试引用。
- 区分“直接验收”与“间接经过”；fixture 创建或顺带调用不能标成完整覆盖。
- 对每个 Scenario 标记 `covered`、`partial`、`missing` 或 `manual-only`。
- 记录测试是否已经自动化、所需依赖、预计耗时以及能否由 Release Full Test 调用。
- 现有测试没有稳定地证明某项能力时，宁可标记缺口，不根据文件名推断已覆盖。

## 候选方案与权衡

### 方案一：所有能力都写 Browser E2E

优点：

- 接近真实用户操作。
- 能同时覆盖 Web、HTTP 和数据库的主路径。

缺点：

- 无法自然覆盖 CLI、Process Lifecycle、Service、Migration 和无 UI 的后台行为。
- 大量 CRUD 和错误分支放在浏览器层会显著增加执行时间和 Flake。
- 失败时难以判断是 UI、API、Worker、数据库还是第三方协议问题。
- Channel、MCP、Email 和 Provider 仍需要独立 fake/contract 基础设施。

结论：拒绝作为总方案。浏览器只覆盖高价值用户旅程和 UI 特有行为。

### 方案二：只扩展 Go System Test

优点：

- 可以复用现有真实 Binary、PostgreSQL、HTTP、SSE 和 fake Provider Harness。
- 相比浏览器更稳定、诊断更接近后端原因。
- 适合跨请求、异步 Worker 和 Process Seam。

缺点：

- 不能证明路由、表单、权限可见性、浏览器状态和前端错误反馈。
- 若把所有 Handler CRUD 都放进 System Test，会重复已有进程内集成测试。
- 真实外部协议仍需 contract/live smoke。

结论：作为后端高层测试骨架继续使用，但不能独立完成全量验证。

### 方案三：机器可读能力矩阵 + 分层测试 + 单一 Release Full Test

做法：

- 用稳定 Capability/Scenario ID 连接产品表面和测试。
- 在最低充分层级验证每项能力。
- 只为关键用户链路增加 System 和 Browser E2E。
- 使用 fake/contract 验证扩展协议，并使用 live smoke 验证真实服务。
- 用自动校验确保公开表面没有脱离能力矩阵。
- 用单一 Release Task 汇总全部确定性测试和真实外部服务验证。

优点：

- 覆盖核心、CLI、浏览器、后台 Worker 和外部协议。
- 运行成本、稳定性和诊断性更可控。
- 新能力若没有登记测试，会在发布验收时暴露。
- 可逐步迁移，不要求一次重写已有 473 个测试文件。

缺点：

- 需要维护能力清单和引用校验。
- 初次把现有测试映射到 Capability 的工作量较大。
- 必须约束团队不要为了“覆盖数字”把间接测试标成直接验收。

结论：采用方案三。

## 最终方案

### 总体流程

```text
产品公开表面
  OpenAPI / Web Routes / CLI / Plugins / Builtin Skills
        |
        v
机器可读 Capability + Scenario 清单
        |
        +--> 已有测试映射与缺口报告
        |
        +--> 最低充分测试层级
                Unit / Integration / System / Browser / Contract / Live
        |
        v
Release Full Test
  确定性测试 + Live Smoke
        |
        v
单一发布结论 + 分层诊断报告
```

### 测试层级

| 层级           | 用途                                                 | 典型能力                                         | 在 Release Full Test 中的要求      |
| -------------- | ---------------------------------------------------- | ------------------------------------------------ | ---------------------------------- |
| L0 Static      | Format、Generate、Build、Schema、Manifest 漂移       | 全仓库                                           | 全部执行                           |
| L1 Unit        | 纯函数、状态机、权限、转换、错误映射                 | Goal Fold、Provider Convert、Render              | 全部执行                           |
| L2 Integration | Service/Handler + PostgreSQL，不启动子进程           | CRUD、DB 不变量、Transaction、AuthZ              | 全部执行                           |
| L3 System      | 真实 `stellad` + TCP + DB + Worker + fake dependency | Startup、Auth、SSE、Goal、Scheduler、CLI Process | 执行所有已登记 System Journey      |
| L4 Browser     | 真实浏览器 + Web + API + DB                          | Login、Agent Setup、Chat、Goal、Settings         | 执行所有已登记关键用户旅程         |
| L5 Contract    | fake server 或协议 fixture 验证第三方边界            | Channel、Provider、Email、MCP、Embedding         | 执行所有已登记外部协议场景         |
| L6 Live Smoke  | 真实第三方服务和凭据                                 | Telegram、Feishu、OpenAI、OIDC 等                | 执行所有已配置且登记的真实服务场景 |

### 浏览器测试方案

现有 `tap browser` 文档继续用于探索、排障和临时验证，但不作为 Release Full Test 的测试 Runner。

推荐为 Web 增加 Playwright Test：

- 测试规范、fixture 和断言可以随仓库版本控制。
- `webServer` 可以在测试前启动服务并等待 URL 可用。
- 支持 Web-first Assertion、项目配置、并发、重试、Report 和 Trace。
- Release Full Test 失败时保留 Trace；截图只用于视觉验收或失败诊断。

这些能力由 Playwright 官方的
[Test Configuration](https://playwright.dev/docs/test-configuration) 和
[Best Practices](https://playwright.dev/docs/best-practices) 文档支持。

Release Full Test 第一阶段只运行 Chromium，避免在能力覆盖尚未完成前把成本扩大到三浏览器。若 Stella 后续明确承诺支持其他浏览器，再把对应浏览器加入完整发布验收。

浏览器 fixture 必须：

- 启动测试专用 `stellad`，不能复用开发者正在运行的实例。
- 使用临时 `STELLA_HOME`、隔离数据库和唯一 Run ID。
- 使用 fake Provider 和可控时钟/任务触发方式。
- 通过 API 或稳定 fixture 完成前置数据，不反复用 UI 搭建不属于当前场景的状态。
- 使用 Locator、Response、SSE 或状态轮询等待，不使用固定 `sleep` 判断完成。
- 失败时保留 Browser Trace、Server Log、Fake Request Log 和关键资源 ID。

Browser Scenario 只在以下至少一项成立时进入阻塞门禁：

- Cookie、Redirect、无认证访问、Secret 掩码等浏览器安全边界本身就是验收行为。
- SSE 渲染、复杂客户端状态或 Schema 驱动动态表单无法由 Integration/System 直接证明。
- 场景跨越 Web、API、DB 或外部 fake，是高频、关键且容易发生装配回归的用户旅程。
- 该页面已有实际回归记录，低层测试不能防止同类问题再次发生。

首批阻塞 Browser Journey 为：

| Scenario | Journey                     | 浏览器层独有价值               |
| -------- | --------------------------- | ------------------------------ |
| C02-S02  | 登录、退出和改密生命周期    | Cookie、Redirect 和登录态      |
| C07-S03  | Web Chat SSE 收发           | Streaming 渲染和客户端状态     |
| C05-S02  | Agent 创建、编辑和分配      | 复杂表单与关系装配             |
| C06-S02  | Provider 配置和 Secret 显示 | Secret 输入、掩码与动态配置    |
| C17-S02  | 公开 Share Link 无认证访问  | 无认证浏览器上下文             |
| X02-S02  | Channel 配置管理            | Plugin Schema 驱动的动态管理面 |

其余 Browser 候选逐项处理：

1. 若业务行为已由 Integration/System 充分证明且 UI 只是薄 CRUD，删除独立 Browser 必测场景或将行为归并到最低充分层级。
2. 若存在浏览器独有价值但暂不影响当前发布结论，保留为 `release_policy: nonblocking`，在报告中列为 Nonblocking Backlog，不计入 Blocking Gap。
3. 若涉及安全、动态表单或复杂客户端状态，则升级为 `release_policy: blocking`。C18 Vault Secret UI 和 X10 Plugin 动态配置页在实现 Browser Runner 时必须再次评估，不能机械暂缓。

### System Test 扩展原则

只为低层测试无法触达的 Seam 增加 Journey：

- Binary Startup、Migration、Readiness 和 Shutdown。
- HTTP Authentication、Cookie、Bearer、SSE 和跨请求流程。
- River Worker、Scheduler、Goal Dispatcher 等异步行为。
- CLI 子进程、Signal、Upgrade Artifact 和 Process Boundary。

单 Handler CRUD、纯权限规则和数据库不变量仍放在 L1/L2，避免 System Test 膨胀成缓慢的 API 穷举。

现有 System Suite 共享一个 Server 和 Database。新增场景必须使用 Run ID 隔离数据、显式清理，并避免依赖前序场景产生的业务数据。会终止 Server 的场景继续最后运行。

### 扩展集成测试原则

每种扩展至少具备：

1. 协议解析和序列化单元测试。
2. 由本地 fake server 驱动的 Contract Test。
3. Plugin Runtime 生命周期测试。
4. 错误、超时、限流、重连和幂等性场景。
5. 能够合规、稳定自动化的真实第三方集成按适用性登记至少一条 Live Smoke；暂时缺少合规自动化身份的集成登记 Manual Requirement、原因和升级条件。

Contract Test 必须能证明 Stella 发出的请求、处理的回调和最终业务结果，而不只断言函数被调用。

Contract Test 验证 Stella 是否符合当前登记的协议契约；Live Smoke 验证真实账户、网络、权限和第三方当前实现是否仍然可用。两者提供不同证据，不能互相替代。

测试目标是否属于 Live 由控制权决定，而不是由它是否运行真实软件决定。在测试 Job 中自动启动、固定版本、隔离数据、不需要第三方凭据并能自动清理的 PostgreSQL、Docker、kind、MCP Echo Server、Webhook Receiver 或 OTLP Collector 都属于确定性测试。

以下五项改为确定性阻塞测试：

| Scenario | 新 Layer   | 受控目标                        | 必须证明的行为                                          |
| -------- | ---------- | ------------------------------- | ------------------------------------------------------- |
| X07-S02  | `contract` | 本地 Webhook Receiver           | 真实交付链路、Payload、重试、永久失败和清理             |
| X09-S02  | `contract` | 自托管 MCP Echo Server          | Initialize、Discovery、Invocation、Timeout、断连和重连  |
| X15-S02  | `contract` | CI Docker Daemon                | 三后端结果矩阵、容器执行、权限、超时和资源清理          |
| X18-S02  | `package`  | Release Artifact、kind、外部 PG | Archive 启动、Docker/Helm Readiness、迁移和部署清理     |
| X19-S02  | `contract` | 自托管 OpenTelemetry Collector  | OTLP 发送、失败恢复、Shutdown Flush、查询和敏感信息脱敏 |

这些场景重分类后仍保持当前真实 `status`；测试尚未实现时仍为 `missing`，实现并取得直接证据后才能改为 `covered`。若 X07 或 X09 的两个 Scenario 在实现时证明断言完全重复，可以归并；不能为了维持固定的场景总数重复测试。

Live Smoke 使用专用低权限测试账户和单独 Secret，输出脱敏结果。能够合规自动化的内置外部集成都必须登记至少一个测试专用目标；X06 微信和 X13 真实 IdP 等当前缺少合规稳定身份的场景登记为 Manual Requirement，不得伪造 Target。Release Full Test 每次执行全部已登记 Live Smoke，不根据版本大小、改动范围或运行时间选择子集。

Live Smoke 结果与发布处置如下：

| 结果               | 含义                                    | 发布处置                                                  |
| ------------------ | --------------------------------------- | --------------------------------------------------------- |
| `Pass`             | 本次 Commit 在真实目标上完成已登记行为  | 通过                                                      |
| `Product Failure`  | Stella 的鉴权、协议、配置或运行行为错误 | 阻止发布                                                  |
| `External Blocked` | 第三方故障、限流、网络或测试账户异常    | 不算 Pass；需要显式人工 waiver 才能继续                   |
| `Not Run`          | 缺少 Secret、Target、平台或实际执行结果 | 不算 Pass；需要记录原因和显式人工 waiver                  |
| `Flaky`            | 至少一次失败后重试通过                  | 不能静默显示为 Pass；保留 Attempt 并进入人工复核或 waiver |

`release_policy: nonblocking` 只用于边际价值低、即使产品行为失败也不应阻止发布的明确场景，不能把所有内置 Provider、Channel、Email 和身份集成一刀切为非阻塞。

### Live Smoke 最小测试清单

每个真实集成先实现一条短小、可清理、带唯一 Run ID 的成功路径。异常、超时、限流和重试等分支继续由 Contract Test 覆盖，不在真实服务上穷举。

| 类型     | 每个已配置目标的最小 Live Smoke                                                                  | 目标声明支持时同时验证的能力               | 清理与主要证据                                     |
| -------- | ------------------------------------------------------------------------------------------------ | ------------------------------------------ | -------------------------------------------------- |
| Provider | 使用真实凭据完成鉴权；选择声明支持的测试模型；发送含 Run ID 的最小请求；收到有效结束结果         | Streaming、Tool Call、Model Discovery      | Provider Request ID、模型、耗时和脱敏响应摘要      |
| Channel  | 从专用外部账号发送含 Run ID 的消息；Stella 接收并识别来源；机器人回复同一 Run ID；外部侧确认收到 | Attachment、Thread、Edit、Identity Mapping | 外部 Message ID、Stella 侧事件 ID 和双向时间戳     |
| Email    | 专用邮箱收到一封含 Run ID 的测试邮件并被 Stella 读取；Stella 再发送一封测试邮件并在收件箱确认    | Mark、Reply、HTML/Attachment               | Message-ID、邮箱别名、投递时间和清理结果           |
| MCP      | 连接专用稳定 MCP Server；完成 Initialize 和 Tool Discovery；调用确定性 Echo Tool 并校验 Run ID   | Resource、Prompt、认证、长连接             | Server 版本、协商协议版本、Tool 名称和脱敏调用结果 |

并非所有目标都支持表中所有扩展能力。Capability 清单必须声明目标支持的能力，Release Full Test 每次验证全部已声明场景，不能把“不支持”误报成测试通过。

### Live Smoke 执行与报告

`mise run live-smoke` 对应的 Runner 负责执行全部已登记的真实外部目标。实现可以保留指定 Capability/目标的调试入口，但 Release Full Test 不使用筛选参数。

每次运行必须：

- 绑定待发布 Commit、版本、环境、Capability ID、目标别名和唯一 Run ID。
- 先做 Secret、账户权限和网络 Preflight；Preflight 失败仍生成 `External Blocked` 或 `Not Run` 报告。
- 使用测试专用账户、群、邮箱、模型配额和 MCP Server，不读取开发者个人凭据。
- 对轮询和网络调用设置 Deadline，禁止无限等待或用固定长 `sleep` 代替状态判断。
- 保存脱敏后的请求标识、响应摘要、耗时、重试次数和清理结果；不得保存 Token、邮件正文或用户隐私数据。
- 对每次重试保留独立 Attempt；重试后通过必须显示为 Flaky，不得静默显示成首次通过。
- 清理测试消息、邮件和其他可删除资源；无法自动删除时使用明确前缀和保留期限，避免长期污染真实账户。

完整 Live Smoke Report 必须绑定待发布 Commit。旧 Commit、不同环境或历史运行结果不能替代本次发布证据。

### 跨能力场景模板

每项能力按适用性从以下模板选择场景，不能只保留 Happy Path：

- 创建或首次使用成功。
- 读取和再次进入时状态持久化。
- 更新、删除、撤销或恢复。
- 未认证、无权限和跨租户访问失败。
- 非法输入、资源不存在和冲突。
- 异步完成、超时、取消和重试。
- Server 重启后的持久化与恢复。
- 并发或重复请求的幂等性。
- Secret、Token、日志和错误信息不泄漏敏感数据。
- 创建数据的清理以及失败中途的资源回收。

### 核心能力的目标高层测试

| 能力    | 必须新增或保留的高层场景                                                         | 主要层级                         |
| ------- | -------------------------------------------------------------------------------- | -------------------------------- |
| C01     | 首次启动迁移、重复启动、Readiness、Graceful Drain、外部 DB 错误                  | System                           |
| C02-C04 | Signup/Login/Logout、Session Revoke、Password、PAT、OAuth Client 权限            | Integration + System + Browser   |
| C05-C06 | Agent 和 Provider 配置、User Assignment、Tool Override、fake Model Discovery     | Integration + Browser + Contract |
| C07     | Chat SSE、Attach、Provider Error、Cancel、History、Compaction 后继续对话         | System + Browser                 |
| C08-C09 | Workspace 文件全生命周期、Project Scope 隔离和 Project Session                   | Integration + Browser            |
| C10     | Draft/Activate、Plan Approve/Reject、Dependency、Cancel、Retry、Verdict、Archive | Integration + System + Browser   |
| C11-C12 | Workflow 保存/实例化、Scheduler Create/Run/Restart Persistence                   | Integration + System + Browser   |
| C13     | Profile/Soul/Constraint/Knowledge、Reflect Replace/Deprecate、Memory Isolation   | Integration + System + Browser   |
| C14     | Skill CRUD、File、Upload、Scope Visibility、Agent 使用                           | Integration + System + Browser   |
| C15     | Group CRUD、Membership、Group SSE、Multi-Agent Routing、History                  | Integration + System + Browser   |
| C16-C18 | Notify/Inbox、Public Share/Revoke、Vault Secret Lifecycle 和脱敏                 | Integration + System + Browser   |
| C19     | Builtin Resource/Tool 可见性、禁用 Tool 不可调用                                 | Integration + System             |

### 扩展能力的目标测试

| 能力    | 确定性验证                                                                                            | 真实服务验证                        |
| ------- | ----------------------------------------------------------------------------------------------------- | ----------------------------------- |
| X01     | CLI 子进程、临时 Home、Version/Upgrade Fixture、Service Adapter Contract                              | 支持平台上的真实 Service Install    |
| X02-X07 | 统一 Channel Contract、各平台 Payload Fixture、Streaming、Retry、Identity                             | 每个平台一条收发消息 Smoke          |
| X08     | fake Mail Server 的 List/Read/Mark/Send 和 Agent Tool                                                 | 专用邮箱账户                        |
| X09     | fake MCP Server 的 Discover/Invoke/Error/Timeout                                                      | 专用 MCP Server                     |
| X10     | Plugin Toggle/Config/Reload/Status、Manifest Sync、Tool/Hook 装配                                     | 需要外部 CLI 的 Plugin Smoke        |
| X11     | fake RSS、Feed Poll、Article、Digest、Scheduler                                                       | 公开稳定 Feed 的只读 Smoke          |
| X12     | fake Anthropic/OpenAI 协议、Streaming、Tool Call、Error/Rate Limit                                    | 专用低配额模型账户                  |
| X13     | fake OIDC/OAuth Provider 的 Device/Callback/Revoke                                                    | 专用 Identity Provider Tenant       |
| X14     | fake Embedding API、Backfill、Semantic Query、Restart                                                 | 专用 Embedding 账户                 |
| X15     | Local/Docker/None Contract、CI Docker Host、Process/Container Cleanup、Permission                     | 无必须第三方 Live                   |
| X16-X17 | Marketplace Fixture、Install/Upgrade、Builtin Skill Discovery/Injection                               | 稳定 Marketplace/CLI Smoke          |
| X18-X19 | Archive Extract/Run、Docker Start/Ready、kind/Helm Install、外部 PostgreSQL、Self-hosted OTLP Capture | 仅在明确支持特定托管后端时增加 Live |

### 自动化入口

Tag Release workflow 先增加独立 Validate Job。短期门禁至少执行：

```text
checkout exact tag commit
-> generate:check
-> build
-> test:capabilities
-> test
-> system-test
```

GoReleaser 和 Docker 发布 Job 都使用 `needs: validate`，因此 Validate 失败时不得生成或推送发布物。普通 PR/Main CI 保持现状。

该门禁发生在 Tag 已推送之后、Artifact 发布之前；失败会留下未发布的 Tag。若未来要求 Tag 本身也只能在验证后创建，应增加基于 Commit 的手工 Promotion Workflow，由验证成功的流程创建 Tag。

保持现有任务，最少新增三个入口：

```text
mise run test:capabilities  # 校验 Capability/Scenario 清单和测试引用
mise run browser-test      # 运行确定性的 Chromium 核心旅程
mise run live-smoke        # 运行全部已配置且登记的真实集成
```

L1/L2/L5 尽量继续由现有 `mise run test` 承载，不为相同 Go 测试再建立一套平行命令。

目标 `release:validate` 顺序为：

```text
format
-> generate:check
-> build
-> test:capabilities
-> test
-> system-test
-> browser-test
-> release:check
-> release:snapshot
-> live-smoke
```

`release:validate` 是本方案唯一面向发布者的完整入口。`test:capabilities`、`browser-test` 和 `live-smoke` 是内部组成任务，不要求日常开发单独运行。

发布流程在发布 Artifact 前对待发布 Commit 执行一次完整 `release:validate`。本方案不把该任务接入日常 PR/Main CI/CD。Live Smoke 与确定性测试生成同一套 Capability Report，但分别展示结果；最终结论由 `release_policy`、本次结果和显式 waiver 共同决定。

第二部分初期的 `test:capabilities` 只校验 Schema、Surface 和 Evidence 引用，不因现有 Blocking Gap 立即中断所有 Release。待阻塞场景补齐并能由统一 Runner 执行后，再启用“Blocking Gap 非零则 `release_allowed=false`”的最终聚合规则。

X18 Package 测试最终必须验证即将发布的同一份候选 Artifact。若 Validate 测试 Snapshot、GoReleaser 随后重新构建另一份产物，报告必须明确这种差异；优先复用已验证 Artifact 或通过可复现构建证明二者一致。

### 分阶段落地

#### 阶段一：首次建立并基线化长期能力清单

1. 从 OpenAPI 提取 `operationId`。
2. 从 Web Router 提取 Route。
3. 从 `urfave/cli` Command Tree 提取 CLI Command。
4. 从 Plugin Registry、Provider、Channel、Sandbox 和 System Skill Registry 提取扩展项。
5. 人工归并为稳定 Capability，并为所有公开表面建立映射。
6. 将清单作为版本化文件长期维护，并增加只读能力表面校验，防止后续漂移；发布运行不得用自动生成结果覆盖人工归并后的稳定 Capability/Scenario 定义。

完成标准：公开表面全部映射，豁免项有理由，能力清单成为仓库内长期事实源，校验可以检测新增未登记表面但不会重建或改写清单。

#### 阶段二：首次盘点已有测试并建立长期证据映射

1. 盘点 Go 测试框架与辅助库、System Journey、自研 Harness、mise Task、workflow、发布/质量工具和手工 Runbook。
2. 将直接证明能力的测试映射到 Scenario。
3. 标记 Partial、Missing 和 Manual-only。
4. 生成按 Capability 和 Layer 汇总的覆盖报告。
5. 约定后续功能和测试变更同步更新同一份映射；每次 Release 校验引用并执行场景，不重新做人工资产盘点。

完成标准：每项能力的现有证据和缺口可直接查询，已有框架/工具的用途与边界明确，不能以文件数量、行覆盖率、构建通过或工具存在代替功能验收。

#### 阶段三：补齐并自动化

1. 第 0 步先把现有 `test:capabilities`、普通测试和 System Test 作为 Validate Job 接入 Tag Release，并让发布 Job 等待门禁。
2. 第一部分已在 Manifest 增加 `release_policy`、完成五项自托管场景重分类，并先按当前证据分别报告 Blocking Gap、Nonblocking Backlog、Manual Requirement 和 Live Scenario；第二部分 Runner 再写入本次 Live Result。
3. 先补确定性的核心、Integration、System、Contract 和 Package 阻塞缺口。
4. 建立 Browser Runner，首批实现六条关键 Web Journey；其余 Browser 候选按最低充分层级规则归并、暂缓或升级。
5. 建立运行全部已配置真实目标的 Live Smoke，以及 `Product Failure`、`External Blocked`、`Not Run`、`Flaky` 和 waiver 记录。
6. 将所有层级接入单一 `release:validate`，阻塞场景补齐后启用统一 `release_allowed` 聚合门禁。
7. 采集执行时间和 Flake 数据后再做并行、分片和预算优化。

完成标准：所有 Blocking Scenario 都有可执行直接证据；全部已登记自动化 Live Smoke 能在同一次 Release Full Test 中执行；Manual Scenario 有明确结果或 waiver；一个命令能够给出 Pass、Block 或 Needs Waiver 的明确结论。

## 关键行为与接口

### Capability ID

- `Cxx` 表示核心能力，`Xxx` 表示外围或扩展能力。
- Scenario 使用 `<Capability>-S<nn>`。
- ID 表达稳定产品语义，不绑定 Go Package、URL 或页面文件名。
- 能力被删除时保留 Tombstone 或迁移记录，避免历史报告失去含义。

### 测试引用

- 测试名称或元数据必须包含 Scenario ID，或者由能力清单明确引用具体测试路径和名称。
- 一个测试可以证明多个 Scenario，但必须分别给出可观察断言。
- 一个 Scenario 可以由多个层级共同证明，例如 Login 同时有 Integration、System 和 Browser 证据。
- 间接经过某能力不算直接覆盖，例如 Goal fixture 创建了 Agent，不等于 Agent CRUD 已验收。

### 发布策略

- `layer` 决定最低充分测试边界，`status` 记录当前证据，`release_policy` 决定发布处置，三个字段不得互相替代。
- `release_policy: blocking` 的场景最终必须有当前 Commit 的直接执行结果；`status: missing` 只允许存在于门禁尚未完成的实施阶段。
- `release_policy: nonblocking` 的结果仍必须执行或明确报告，但不计入 Blocking Gap。
- `release_policy: manual` 表示当前没有合规稳定的自动化目标；每次 Release 必须记录人工结果或 waiver，不能静默当作 Pass。
- Waiver 必须绑定 Commit、Scenario、原因、批准人和时间；历史 waiver 不得自动沿用到新 Commit。

### 数据和隔离

- 每次运行生成唯一 Run ID，所有 User、Agent、Session、Goal、Feed 和外部 fake 请求均可追踪。
- 测试使用临时 Home、隔离 Database 和显式环境变量 Allowlist。
- 默认不读取开发者的 `STELLA_*`、`OTEL_*`、Token 或浏览器状态。
- Cleanup 在资源获得后立即注册，失败中途也必须回收。
- 并行化只用于数据和资源真正隔离的场景；共享 Server/DB 的有序 Journey 不伪装成并行。

### 结果与诊断

Release 报告至少包含：

- 基线 Commit、平台、测试层级和执行时间。
- 每个 Scenario 的 `layer`、`status`、`release_policy` 和本次执行结果。
- 分开的 Blocking Gap、Nonblocking Backlog、Manual Requirement 和 Live Result 统计。
- 通过、失败、跳过和未实现的 Scenario ID。
- 每个失败的 Expected/Actual。
- Server Log、Browser Trace、Fake Request Log 和关键资源 ID。
- Skip 原因；不支持平台和缺少 Secret 必须与测试通过区分。
- Flake 重试信息；重试后通过不能静默伪装成一次通过。
- Waiver 对应的 Commit、Scenario、原因、批准人和时间。

## 验收与测试策略

本方案进入实现前，文档验收条件为：

- 能力定义明确区分能力、入口和测试场景。
- 核心和扩展能力均有完整初始清单。
- 已有测试方法、当前高层覆盖和主要缺口有事实依据。
- 候选方案说明了为什么不采用全 Browser 或全 System。
- 推荐方案定义了能力清单、分层、Browser Runner、外部 fake、Live Smoke 和单一 Release Full Test。
- 三个落地阶段都有可验证完成标准。
- 没有把真实第三方稳定性错误归类为产品测试通过。
- 没有要求在方案确认前修改代码或 CI。

实施完成后的总体验收条件为：

- 100% 公开 OpenAPI Operation、Web Route、CLI Command 和内置 Plugin 被 Capability 映射或显式豁免。
- 100% 核心 Capability 的适用验收场景均已自动化并进入 Release Full Test。
- 100% 扩展 Capability 具备自动化 Contract Scenario；能够合规自动化的真实外部集成具备至少一个专用 Live Target，暂时不能自动化的场景有明确 Manual Requirement、原因和升级条件。
- 所有 Blocking Scenario 都由发布前的 `release:validate` 执行并取得当前 Commit 的直接结果；Nonblocking 和 Manual Scenario 分别执行或记录。
- Browser E2E 覆盖关键用户旅程，而不是机械覆盖每个 API 操作。
- Release 失败可以从保存的 Artifact 定位，不要求先本地复现。
- Live Smoke 与确定性测试分开统计；`Product Failure` 阻止发布，`External Blocked`、`Not Run` 和 `Flaky` 只有经过显式 waiver 才能继续。
- 所有已配置且登记的真实外部目标都绑定当前 Commit，不能用历史结果或历史 waiver 替代。

## 风险

### 能力清单漂移

只靠人工维护会很快过时。必须自动提取 OpenAPI、Route、CLI 和 Plugin 表面，并让未映射新增项失败。

### 把间接测试误标成覆盖

测试经过某代码路径不代表验收了该能力。能力报告必须要求稳定的可观察断言，并在 Review 中检查 `test_refs`。

### 异步和浏览器 Flake

固定等待、共享账号、共享浏览器状态和不受控后台 Worker 会产生随机失败。使用 Context Deadline、事件等待、唯一 Run ID、隔离 fixture 和失败 Trace。

### Release Full Test 过慢

先保证正确性，再根据实测拆分并行 Job、复用构建产物和分片。不能为了时长把关键场景重新降为手动。

### 真实第三方不可控

Contract Test 是协议证据，Live Smoke 是真实兼容性证据。两者必须分别展示、全部执行，不能相互替代。真实第三方故障不能记为产品 Pass，也不能无限期锁死发布；通过显式 waiver 保留风险所有权和审计记录。

### 测试实现反向污染生产代码

Fake 应尽量位于测试边界，通过公开协议接入。若某场景需要修改生产协议、增加测试专用后门或依赖外部网络，必须重新评审设计。

### 平台覆盖不足

System Test、Service、Sandbox、Binary 和 Docker/Helm 的支持平台不同。Capability Report 必须按平台展示场景，Skip 不能计为 Pass。

## 未决问题

以下问题不阻碍确认总体方案，但会影响实现计划：

1. Release Full Test 可以接受的目标时长，以及如何在不减少场景的前提下拆分并行 Job。
2. Service Install、Docker Start 和 Helm Install 的完整平台/发布物矩阵如何提供测试环境。
3. Live Smoke 使用哪些专用第三方测试账户、Secret 管理方式和预算上限，以及 waiver 由谁批准和保存。
4. Release Full Test 应运行在本地受控环境还是 Self-hosted Runner。

已确认的执行边界是：日常开发继续使用现有 CI/CD，不运行本套件；Tag Release 在发布 Artifact 前必须通过 Validate Job；每次发布使用同一份 Scenario 和 `release_policy`，所有阻塞场景取得 Pass 或经规则允许的显式 waiver 后才能继续。
