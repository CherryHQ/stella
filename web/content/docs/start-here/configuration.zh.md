---
title: 配置
---

所有配置都通过Web UI进行管理。使用 `stellad server` 启动服务器，然后在浏览器中打开 [http://localhost:25678](http://localhost:25678)。所有配置存储在 `~/.stella/stella.db` 这个 SQLite 数据库中，无需编辑任何配置文件。

主目录默认为 `~/.stella`，可以通过设置 `STELLA_HOME` 环境变量来更改。

## 提供商

在Web UI中打开 **提供商** 页面，添加你的 AI 提供商凭证。Stella 支持 Anthropic、OpenAI 以及任何兼容 OpenAI API 的服务（Perplexity、Together.ai、通过 Ollama 运行的本地模型等）。

环境变量 `ANTHROPIC_API_KEY` 和 `OPENAI_API_KEY` 在Web UI中未填写凭证时可作为备用。

## 代理

打开 **代理** 页面来创建和配置代理。每个代理包含：

- **名称** — 在渠道和Web UI中显示的名称
- **模型** — 默认模型（格式为 `provider/model`，如 `anthropic/claude-sonnet-4-6`）
- **强力模型** — 可选，用于复杂推理任务（未设置时回退到默认模型）
- **快速模型** — 可选，用于快速检查和判断（未设置时回退到默认模型）
- **系统提示** — 自定义人格和指令
- **沙箱设置** — 代理代码执行的网络访问策略

你也可以在代理工作空间 `~/.stella/workspaces/{agent-id}/` 中放置 `SOUL.md` 文件来覆盖系统提示。

## 渠道

打开 **渠道** 页面来连接消息平台。你可以创建同一平台的多个实例（例如两个 Telegram 机器人用于不同的代理）。

每个渠道实例可以绑定到特定代理。如果不绑定，用户可以通过 `/agent` 命令切换代理。

各渠道的设置说明：

- [Telegram](/docs/channels/telegram)
- [QQ](/docs/channels/qq)
- [飞书](/docs/channels/feishu)
- [微信](/docs/channels/weixin)

## 认证

默认情况下，你通过设置时创建的用户名和密码登录 Web UI。

如需使用外部身份提供商（Zitadel、Keycloak、Auth0 或任何兼容 OIDC 的服务），可通过环境变量配置 OIDC。详见 [OIDC 认证指南](/docs/guides/oidc-authentication)。

## 用户

当有人通过已连接的渠道发送消息时，用户会自动创建。每个用户获得独立的每代理记忆。你可以在Web UI的 **用户** 页面管理用户、角色和权限。

## Runner 设置

Runner 控制代理如何处理消息。你可以在Web UI的 **设置** 页面进行配置：

| 设置         | 默认值        | 描述                           |
| ------------ | ------------- | ------------------------------ |
| 空闲超时     | 10 分钟       | 空闲代理会话被清理前的等待时间 |
| 委派超时     | 15 分钟       | 委派任务的最长时间             |
| 压缩阈值     | 80,000 tokens | 历史记录超过此大小时自动压缩   |
| 保留最近消息 | 20            | 压缩后保持原文的最近消息数量   |

## 心跳

心跳让 Stella 能够监视文件并在内容变化时采取行动。在Web UI的 **设置** 页面进行配置：

- **启用** — 开启或关闭心跳轮询
- **间隔** — 检查频率（例如 `10m`）
- **文件** — 心跳文件路径（例如代理工作空间中的 `HEARTBEAT.md`）

心跳仅在服务器模式（`stellad server`）下运行。它使用快速模型来判断是否需要采取行动，以降低成本。

## 目录结构

所有数据存储在 `~/.stella` 下（可通过 `STELLA_HOME` 配置）：

| 路径                                           | 用途                                       |
| ---------------------------------------------- | ------------------------------------------ |
| `~/.stella/stella.db`                          | 数据库（配置、记忆、调度器）— 请备份此文件 |
| `~/.stella/workspaces/{agent-id}/`             | 每个代理的工作空间、技能和覆盖文件         |
| `~/.stella/workspaces/{agent-id}/SOUL.md`      | 可选的代理人格覆盖                         |
| `~/.stella/workspaces/{agent-id}/SYSTEM.md`    | 可选的系统提示覆盖                         |
| `~/.stella/workspaces/{agent-id}/HEARTBEAT.md` | 心跳指令                                   |
| `~/.stella/cache/`                             | 模型缓存（可安全删除）                     |

## 环境变量

仅识别少量环境变量：

| 变量                         | 描述                                                                                     |
| ---------------------------- | ---------------------------------------------------------------------------------------- |
| `STELLA_HOME`                | 覆盖主目录（默认 `~/.stella`）                                                           |
| `ANTHROPIC_API_KEY`          | Anthropic 的备用 API 密钥                                                                |
| `OPENAI_API_KEY`             | OpenAI 的备用 API 密钥                                                                   |
| `STELLA_VAULT_KEY`           | [密钥库](/docs/guides/secrets-and-keys)的主密钥 — 密钥管理、OAuth 和 Bearer Token 所必需 |
| `STELLA_DOCKER_SANDBOX_MODE` | 仅 `docker` 沙箱后端需要：`host`、`bind` 或 `volume`                                     |
| `STELLA_HOME_HOST`           | `STELLA_HOME` 的宿主机侧路径；仅 `STELLA_DOCKER_SANDBOX_MODE=bind` 时需要                |
| `STELLA_HOME_VOLUME`         | `STELLA_HOME` 的 Docker named volume 名称；仅 `STELLA_DOCKER_SANDBOX_MODE=volume` 时需要 |

有关如何选择沙箱后端和配置 Docker 沙箱模式，请参阅[沙箱指南](/docs/guides/sandbox)。

所有其他配置通过Web UI进行管理。
