---
title: 配置
---

大多数配置都通过 Web UI 管理。使用 `stellad server` 启动服务器，然后在浏览器中打开 [http://localhost:25678](http://localhost:25678)。所有配置存储在 PostgreSQL 中——可以使用托管在 `~/.stella` 下的内嵌集群，也可以在设置 `STELLA_DATABASE_URL` 时使用外部服务器。如果内嵌 PostgreSQL runtime 尚未安装，先运行一次 `stellad postgres download`。无需编辑任何配置文件。

主目录默认为 `~/.stella`，可以通过设置 `STELLA_HOME` 环境变量来更改。

## 提供商

在Web UI中打开 **提供商** 页面，添加你的 AI 提供商凭证。Stella 支持 Anthropic、OpenAI 以及任何兼容 OpenAI API 的服务（Perplexity、Together.ai、通过 Ollama 运行的本地模型等）。

## 代理

打开 **代理** 页面来创建和配置代理。每个代理包含：

- **名称** — 在渠道和Web UI中显示的名称
- **模型** — 默认模型（格式为 `provider/model`，如 `anthropic/claude-sonnet-4-6`）
- **强力模型** — 可选，用于复杂推理任务（未设置时回退到默认模型）
- **快速模型** — 可选，用于快速检查和判断（未设置时回退到默认模型）
- **系统提示** — 自定义人格和指令
- **沙箱设置** — 代理代码执行的网络访问策略
- **系统设置工具** — 内置 Stella 默认开启；其他代理默认关闭，需由代理管理者开启，且仅在前台一对一聊天中发现工具

你也可以在代理工作空间 `~/.stella/agents/{agent-id}/` 中放置 `SOUL.md` 文件来覆盖系统提示。

### 每个代理独立的提供商 API 密钥

提供商的类型、base URL、模型目录、启用状态和默认 API 密钥仍由管理员统一管理。企业集成可以通过 Agent API，按 canonical Provider ID 为某个代理写入不同的只写 API 密钥：

```json
{
  "name": "Enterprise Coder",
  "model": "openai-main/gpt-4.1",
  "provider_credentials": [{ "provider_id": "openai-main", "api_key": "write-only" }]
}
```

- 创建代理时传入 `provider_credentials`；
- 调用 `PATCH /api/agents/{id}/provider-credentials/{providerId}`，以 `{ "api_key": "write-only" }` 设置或轮换覆盖密钥；
- 对同一资源调用 `DELETE`，恢复使用提供商的全局密钥；
- 通过 List 和 Get 端点读取安全元数据。密钥永远不会返回。

代理覆盖密钥优先于全局密钥，并用于该代理通过该提供商发出的每一次调用；当 Vision 选择同一提供商时，图像理解调用也遵循这一规则。被分配的用户使用该代理时会消耗覆盖密钥对应的额度，但只有管理员和代理创建者可以修改密钥。

这个 API 只覆盖密钥。提供商 endpoint、类型、模型和启用状态仍由管理员控制。目前 Web UI 尚未提供每代理凭证编辑器。

## 在 Agent 对话中管理部分设置

内置 **Stella** 初始开启系统设置工具；其他 Agent 初始关闭，需由代理管理者在**资料 → 配置 → 高级配置**中开启，管理者也可以再次关闭 Stella 的该能力。此设置只允许该 Agent 在已登录、前台、直接一对一聊天中发现工具，不授予部署、领域或管理员权限。群聊、访客聊天、Webhook、定时或委派任务以及 `session_send` 都不能使用此能力。每次调用都会重新检查已保存的 Agent 设置和你的正常权限。

已开启的 Agent 可以管理你有权使用或管理的 Agent、其逐 Agent 工具覆盖，以及你有权操作的个人或 Agent 范围 Library 文件、托管 Skill 和 MCP 注册。管理员还可以管理 Provider 元数据、默认模型和 Embedding 设置、插件启用/禁用，以及 system 范围的 Library、Skill 和 MCP 资源。指定目标 Agent 时始终会单独校验权限。

| 设置范围          | 可用操作                                                                                                                                        | 权限                                                                        |
| ----------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------- |
| Agent             | `settings_agent_list`、`settings_agent_get`、`settings_agent_create`、`settings_agent_update`、`settings_agent_delete`                          | 按你的正常 Agent 权限。工作区、沙箱、分配关系和凭证不在范围内。             |
| 逐 Agent 工具覆盖 | `settings_agent_tool_list`、`settings_agent_tool_update`、`settings_agent_tool_delete`                                                          | 你可管理的 Agent。删除会恢复正常的工具决定。                                |
| Library 文件      | `settings_library_file_list`、`settings_library_file_get`、`settings_library_file_upload`、`settings_library_file_delete`                       | 已授权的 `user`/`user_agent` 范围；管理员还可使用 `system`/`system_agent`。 |
| 托管 Skill        | `settings_skill_list`、`settings_skill_get`、`settings_skill_create`、`settings_skill_update`、`settings_skill_delete`                          | 与 Library 相同的已授权范围。它与加载已安装 Skill 是两回事。                |
| Provider          | `settings_provider_list`、`settings_provider_get`、`settings_provider_create`、`settings_provider_update`、`settings_provider_delete`           | 仅管理员，结果会脱敏。                                                      |
| 默认模型          | `settings_default_model_get`、`settings_default_model_update`                                                                                   | 仅管理员。                                                                  |
| Embedding 设置    | `settings_embedding_setting_get`、`settings_embedding_setting_update`                                                                           | 仅管理员。                                                                  |
| 插件              | `settings_plugin_list`、`settings_plugin_enable`、`settings_plugin_disable`                                                                     | 仅管理员。插件使用 `kind` 和 `name`，不支持任意配置。                       |
| MCP 注册          | `settings_mcp_server_list`、`settings_mcp_server_get`、`settings_mcp_server_create`、`settings_mcp_server_update`、`settings_mcp_server_delete` | 与 Library 和 Skill 相同的已授权范围。                                      |

对于已有资源，Stella 会先读取其当前 `version`；更新和删除必须使用该不透明版本。资源发生变化时，Stella 必须重新读取后再决定下一步。新建 Agent、上传 Library、创建托管 Skill、Provider 或 MCP 注册都会返回服务端选定的 ID 和当前版本。

对话配置刻意不包含凭证。Provider 和逐 Agent API 密钥、MCP Bearer Token，以及所有凭证绑定变更仍须通过 Web UI 或 API 完成。在对话中创建 Provider 不会携带密钥。已有密钥的 Provider 不能在对话中改到不同的 endpoint origin。对话中新建的 MCP 注册为 no-auth；已有 bearer 的注册可修改受限的安全元数据，但不能在这里变更 endpoint origin、范围或所有者。

结果有明确边界：Agent、Provider、插件和 MCP 列表最多返回 50 项，并在仍有更多项时说明。Library 列表按每页 1–100 项返回；Library 结果不会返回原始文件字节，托管 Skill 结果也不会返回文件内容。Account、用户、Provisioning、渠道、Webhook、任意插件配置、Agent 工作区/沙箱设置和凭证变更仍须通过 Web UI 或 API 完成。

## 渠道

打开 **渠道** 页面来连接消息平台。你可以创建同一平台的多个实例（例如两个 Telegram 机器人用于不同的代理）。

每个渠道实例都可以在 Web UI 中绑定到特定代理。

各渠道的设置说明：

- [Telegram](/docs/channels/telegram)
- [Discord](/docs/channels/discord)
- [QQ](/docs/channels/qq)
- [飞书](/docs/channels/feishu)
- [钉钉](/docs/channels/dingtalk)
- [微信](/docs/channels/weixin)

## 认证

默认情况下，你通过设置时创建的用户名和密码登录 Web UI。

如需使用外部身份提供商（Zitadel、Keycloak、Auth0 或任何兼容 OIDC 的服务），可通过环境变量配置 OIDC。详见 [OIDC 认证指南](/docs/guides/oidc-authentication)。

## 用户

当有人通过已连接的渠道发送消息时，用户会自动创建。每个用户获得独立的每代理记忆。你可以在Web UI的 **用户** 页面管理用户、角色和权限。

## Runner 设置

Runner 控制代理如何处理消息。你可以在Web UI的 **设置** 页面进行配置：

| 设置              | 默认值        | 描述                                                  |
| ----------------- | ------------- | ----------------------------------------------------- |
| 空闲超时          | 10 分钟       | 空闲代理会话被清理前的等待时间                        |
| 聚焦 Session 超时 | 15 分钟       | 同步 `session_create` / `session_send` 运行的最长时间 |
| 压缩阈值          | 80,000 tokens | 历史记录超过此大小时自动压缩                          |
| 保留最近消息      | 20            | 压缩后保持原文的最近消息数量                          |

## 目录结构

所有数据存储在 `~/.stella` 下（可通过 `STELLA_HOME` 配置）：

| 路径                                    | 用途                                                                                                        |
| --------------------------------------- | ----------------------------------------------------------------------------------------------------------- |
| `~/.stella/postgres/`                   | 内嵌 PostgreSQL 数据（配置、记忆、调度器）— 请备份此目录。设置 `STELLA_DATABASE_URL` 指向外部服务器时不存在 |
| `~/.stella/pg-runtime/`                 | 下载的内嵌 PostgreSQL runtime；删除后可用 `stellad postgres download` 重建                                  |
| `~/.stella/agents/{agent-id}/`          | 每个代理的工作空间、技能和覆盖文件                                                                          |
| `~/.stella/agents/{agent-id}/SOUL.md`   | 可选的代理人格覆盖                                                                                          |
| `~/.stella/agents/{agent-id}/SYSTEM.md` | 可选的系统提示覆盖                                                                                          |
| `~/.stella/cache/`                      | 模型缓存（可安全删除）                                                                                      |

## 环境变量

仅识别少量环境变量：

| 变量                          | 描述                                                                                     |
| ----------------------------- | ---------------------------------------------------------------------------------------- |
| `STELLA_HOME`                 | 覆盖主目录（默认 `~/.stella`）                                                           |
| `STELLA_DATABASE_URL`         | 使用外部 PostgreSQL 数据库，而不是内嵌集群                                               |
| `STELLA_BLOB_S3_ENDPOINT`     | 可选的 S3 兼容 endpoint，用于 immutable BlobStore 数据                                   |
| `STELLA_BLOB_S3_BUCKET`       | immutable BlobStore 数据的 bucket；需与 endpoint/access/secret 同时设置，或全部不设置    |
| `STELLA_BLOB_S3_ACCESS_KEY`   | immutable BlobStore 数据使用的 access key                                                |
| `STELLA_BLOB_S3_SECRET_KEY`   | immutable BlobStore 数据使用的 secret key                                                |
| `STELLA_BLOB_S3_REGION`       | 可选 S3 region                                                                           |
| `STELLA_BLOB_S3_USE_SSL`      | S3 兼容存储是否使用 HTTPS；默认 `true`                                                   |
| `STELLA_VAULT_KEY`            | [密钥库](/docs/guides/secrets-and-keys)的主密钥 — 密钥管理、OAuth 和 Bearer Token 所必需 |
| `STELLA_SANDBOX_BACKEND`      | 沙箱后端：`docker`、`local`（默认）或 `none`                                             |
| `STELLA_DOCKER_RUNTIME`       | Docker 沙箱使用的可选已注册 OCI runtime，例如 gVisor 的 `runsc`；不可用时预检失败        |
| `STELLA_REFLECT_CURATOR_MODE` | 生命周期 curator：`armed`（默认值）或不产生写入的紧急停止模式 `shadow`                   |

Structured Reflect 是唯一写入器。Curator 模式在服务启动时读取，修改后需要重启 Stella；非法值会阻止启动。运行检查见[部署](/docs/start-here/deployment#structured-reflect-与-curator)，详细机制见[记忆系统内部原理](/docs/development/memory-internals#structured-reflect-与-curator)。

## Code Mode

Code Mode 是每个会话调用工具的唯一路径，无需开启。提供商仍可直接调用一个固定热集：`bash`、`memory_search`、`memory_read`、`skill_load`，以及可用时的 `view_image`；只要存在非 bash 工具，还会看到 `code`。Code 可以搜索并调用完整的已授权目录，包括这些热工具，而低频 Stella、MCP 和插件 schema 不会进入提供商上下文。直接调用和 Code child 调用共享授权、hooks、审计、脱敏、沙箱和工具生命周期。

在 Code 内，`tools.search(query, offset?)` 每次最多返回 20 个工具摘要。空查询会列出工具目录，每页结果带有 `hasMore` 和 `nextOffset`。使用 `tools.describe(name)` 获取精确 schema，使用 `tools.invoke(name, args?)` 调用工具。Child result 是结构化值：`tools.text(value)` 会拼接文本块，`tools.json(value)` 会解析 JSON 文本；捕获 `ToolInvocationError` 后，也可以对 `error.value` 使用这两个 helper。大内容应留在沙箱文件中，并使用目标工具记录的路径参数，例如 Recally `content_path`；不要为了搬运文件而让正文经过 JavaScript 和模型上下文。

Code Mode 的限制固定为：源码 100 KiB、墙钟时间 30 秒（或更早的 turn deadline）、VM 内存 64 MiB、1,024 个 stack slots、64 次 child 调用、256 条日志/256 KiB 日志，以及 invocation、child result、final result 各 1 MiB。JavaScript runtime 不提供环境文件系统、进程、网络、计时器或 module import 能力；编排内的 shell 和文件操作使用 `tools.invoke("bash", ...)`。这是进程内 capability isolation，不是可运行用户提交代码的通用沙箱；不要把它作为用户代码执行功能开放。

请参阅[沙箱指南](/docs/guides/sandbox)选择沙箱后端和可选 OCI runtime。自定义部署细节在该指南中单独说明。

所有其他配置通过Web UI进行管理。
