---
title: 配置
---

所有配置都通过Web UI进行管理。使用 `stellad server` 启动服务器，然后在浏览器中打开 [http://localhost:25678](http://localhost:25678)。所有配置存储在 PostgreSQL 中——可以使用托管在 `~/.stella` 下的内嵌集群，也可以在设置 `STELLA_DATABASE_URL` 时使用外部服务器。如果内嵌 PostgreSQL runtime 尚未安装，先运行一次 `stellad postgres download-runtime`。无需编辑任何配置文件。

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

你也可以在代理工作空间 `~/.stella/agents/{agent-id}/` 中放置 `SOUL.md` 文件来覆盖系统提示。

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

## 目录结构

所有数据存储在 `~/.stella` 下（可通过 `STELLA_HOME` 配置）：

| 路径                                    | 用途                                                                                                        |
| --------------------------------------- | ----------------------------------------------------------------------------------------------------------- |
| `~/.stella/postgres/`                   | 内嵌 PostgreSQL 数据（配置、记忆、调度器）— 请备份此目录。设置 `STELLA_DATABASE_URL` 指向外部服务器时不存在 |
| `~/.stella/pg-runtime/`                 | 下载的内嵌 PostgreSQL runtime；删除后可用 `stellad postgres download-runtime` 重建                          |
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
| `STELLA_BLOB_S3_ENDPOINT`     | 可选的 S3 兼容 endpoint，用于持久化用户资产镜像                                          |
| `STELLA_BLOB_S3_BUCKET`       | 镜像用户上传资产的 bucket；需与 endpoint/access/secret 同时设置，或全部不设置            |
| `STELLA_BLOB_S3_ACCESS_KEY`   | 资产镜像使用的 access key                                                                |
| `STELLA_BLOB_S3_SECRET_KEY`   | 资产镜像使用的 secret key                                                                |
| `STELLA_BLOB_S3_REGION`       | 可选 S3 region                                                                           |
| `STELLA_BLOB_S3_USE_SSL`      | S3 兼容存储是否使用 HTTPS；默认 `true`                                                   |
| `STELLA_VAULT_KEY`            | [密钥库](/docs/guides/secrets-and-keys)的主密钥 — 密钥管理、OAuth 和 Bearer Token 所必需 |
| `STELLA_DOCKER_SANDBOX_MODE`  | 仅 `docker` 沙箱后端需要：`host`、`bind` 或 `volume`                                     |
| `STELLA_HOME_HOST`            | `STELLA_HOME` 的宿主机侧路径；仅 `STELLA_DOCKER_SANDBOX_MODE=bind` 时需要                |
| `STELLA_HOME_VOLUME`          | `STELLA_HOME` 的 Docker named volume 名称；仅 `STELLA_DOCKER_SANDBOX_MODE=volume` 时需要 |
| `STELLA_REFLECT_MODE`         | Reflect 写入模式：`legacy`（默认值和回滚目标）或 `structured`                            |
| `STELLA_REFLECT_CURATOR_MODE` | 生命周期 curator：`shadow`（默认值和回滚目标）或 `armed`                                 |

Reflect 模式变量在服务启动时读取，修改后需要重启 Stella。无效的写入或 curator 模式会阻止启动。启用和回滚步骤见[部署](/docs/start-here/deployment#启用-structured-reflect)，详细机制见[记忆系统内部原理](/docs/development/memory-internals#structured-reflect-与-curator-上线机制)。

有关如何选择沙箱后端和配置 Docker 沙箱模式，请参阅[沙箱指南](/docs/guides/sandbox)。

所有其他配置通过Web UI进行管理。
