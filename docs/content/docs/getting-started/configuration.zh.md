---
title: 配置
---

所有配置都存储在一个单独的 SQLite 数据库中，位于 `~/.anna/anna.db`。没有 YAML 配置文件。要设置或修改配置，请运行 `anna --open` 打开 Web 管理面板。

主目录默认为 `~/.anna`，可以通过设置 `ANNA_HOME` 环境变量来更改。

## 数据库表

配置分布在 SQLite 数据库的几个表中。

### settings

全局设置的键值存储。每行包含一个 `key`（文本）和一个 `value`（JSON 文本）。已知的键：

| 键             | 描述                                      |
| -------------- | ----------------------------------------- |
| `runner`       | Runner 类型、系统提示、空闲超时、压缩设置 |
| `compaction`   | 压缩阈值（max_tokens、keep_tail）         |
| `heartbeat`    | 心跳轮询开关和间隔                        |
| `plugins`      | 插件定义的 JSON 数组                      |
| `models_cache` | 从提供商缓存的模型列表                    |

### settings_agents

每个 agent 一行。

| 列              | 类型    | 描述                                  |
| --------------- | ------- | ------------------------------------- |
| `id`            | TEXT    | Agent 标识（例如 `anna`）             |
| `name`          | TEXT    | 显示名称                              |
| `model`         | TEXT    | 默认模型，格式为 `provider/model`     |
| `model_strong`  | TEXT    | 强力层级模型，格式为 `provider/model` |
| `model_fast`    | TEXT    | 快速层级模型，格式为 `provider/model` |
| `system_prompt` | TEXT    | 自定义系统提示（绕过默认构建器）      |
| `workspace`     | TEXT    | Agent 工作空间目录的绝对路径          |
| `enabled`       | INTEGER | 1 = 启用，0 = 禁用                    |

### settings_channels

每个消息平台一行。

| 列        | 类型    | 描述                                     |
| --------- | ------- | ---------------------------------------- |
| `id`      | TEXT    | 平台标识符：`telegram`、`qq`、`feishu` 或 `weixin` |
| `enabled` | INTEGER | 1 = 启用，0 = 禁用                       |
| `config`  | TEXT    | 包含平台特定设置的 JSON 数据（见下文）   |

### settings_users

将外部平台用户映射到 agents。

| 列                 | 类型    | 描述                                              |
| ------------------ | ------- | ------------------------------------------------- |
| `id`               | INTEGER | 自增主键                                          |
| `external_id`      | TEXT    | 平台上的用户 ID                                   |
| `platform`         | TEXT    | 平台标识符（例如 `telegram`）                     |
| `name`             | TEXT    | 显示名称                                          |
| `default_agent_id` | TEXT    | 外键到 `settings_agents.id` —— 该用户的默认 agent |

### settings_channel_agents

将特定群聊路由到特定 agent。

| 列         | 类型 | 描述                        |
| ---------- | ---- | --------------------------- |
| `platform` | TEXT | 平台标识符                  |
| `chat_id`  | TEXT | 平台上的群组或聊天 ID       |
| `agent_id` | TEXT | 外键到 `settings_agents.id` |

组合主键：`(platform, chat_id)`。

## Runner 设置

存储在 `settings` 表中，键为 `runner`，值为 JSON 对象。

| 字段                    | 默认值  | 描述                             |
| ----------------------- | ------- | -------------------------------- |
| `type`                  | `go`    | Runner 实现（目前仅支持 `go`）   |
| `system`                | `""`    | 自定义系统提示（绕过默认构建器） |
| `idle_timeout`          | `10`    | 回收空闲 runners 前的分钟数      |
| `compaction.max_tokens` | `80000` | 当历史记录超过此值时自动压缩     |
| `compaction.keep_tail`  | `20`    | 压缩后保留 N 条最近消息          |

## Channel 配置数据

每个平台在 `settings_channels` 的 `config` 列中存储自己的 JSON 结构。

**Telegram**

```json
{
  "token": "BOT_TOKEN",
  "enable_notify": false,
  "notify_chat": "123456789",
  "channel_id": "@my_channel",
  "group_mode": "mention",
  "allowed_ids": [136345060]
}
```

**QQ**

```json
{
  "app_id": "QQ_BOT_APP_ID",
  "app_secret": "QQ_BOT_APP_SECRET",
  "enable_notify": false,
  "group_mode": "mention",
  "allowed_ids": []
}
```

**Feishu**

```json
{
  "app_id": "FEISHU_APP_ID",
  "app_secret": "FEISHU_APP_SECRET",
  "encrypt_key": "",
  "verification_token": "",
  "enable_notify": false,
  "notify_chat": "oc_xxx",
  "group_mode": "mention",
  "allowed_ids": []
}
```

`group_mode` 可接受的值为 `mention`、`always` 或 `disabled`。

## 目录结构

| 路径                                         | 用途                                | 类别 |
| -------------------------------------------- | ----------------------------------- | ---- |
| `~/.anna/anna.db`                            | SQLite 数据库（配置、记忆、调度器） | 数据 |
| `~/.anna/workspaces/{agent-id}/skills/`      | 每个 agent 安装的技能               | 数据 |
| `~/.anna/workspaces/{agent-id}/anna.log`     | 每个 agent 的日志文件               | 数据 |
| `~/.anna/workspaces/{agent-id}/SOUL.md`      | 可选的灵魂/身份覆盖                 | 数据 |
| `~/.anna/workspaces/{agent-id}/SYSTEM.md`    | 可选的系统提示覆盖                  | 数据 |
| `~/.anna/workspaces/{agent-id}/HEARTBEAT.md` | 心跳指令                            | 数据 |
| `~/.anna/cache/`                             | 模型缓存（可安全删除）              | 缓存 |

- **anna.db** 是所有配置、记忆和调度器数据的唯一真实来源。
- **workspaces/** 包含每个 agent 的数据。每个 agent 都有一个以 agent ID 为键的专属目录。
- **cache/** 包含可重新生成的数据。运行 `anna models update` 来重建。
- **agent 沙箱配置**存储在每个 agent 记录（`settings_agents.sandbox`）上。管理面板可编辑网络策略；也可以直接在存储的 JSON 中设置 backend。
  - `backend`：`auto`（默认）、`boxsh`、`local` 或 `docker`
  - `network.mode`：`disabled`（默认）、`allow_all` 或 `whitelist`
  - `network.allowlist`：仅当 mode 为 `whitelist` 时必填
  - Linux 和 macOS 会在选择 `boxsh` 时进行验证。若沙箱后端不可用或无法执行已配置的网络模式，runner 启动时会失败关闭。
  - `local` 是一个宽松的后端，仅提供建议性约束；仅用于明确的本地/开发场景。
  - 当前 `boxsh` 客户端构建可能在运行时拒绝 `whitelist` 模式；仅在运行时支持白名单执行时使用。
  - `docker` 在从 `sandbox.docker.image`（默认 `alpine:3.20`）启动的专用容器中运行每个会话。该后端为手动选择；`auto` 不会自动选择它。需要 `PATH` 中有 `docker` CLI 且 daemon 可达。适用于 Windows（`boxsh` 不可用）以及需要特定 Linux 用户空间的工作流。
  - Docker 后端配置位于 `sandbox.docker` 下：
    - `image`：Docker 镜像引用，默认 `alpine:3.20`。
    - `user`：可选的 `uid:gid` 字符串，传递给 `docker --user`。在 Linux/macOS 上默认使用 anna 进程的 uid/gid；Windows 上为空（由 Docker Desktop 处理映射）。如需在容器内使用 `root`，可覆盖此项。
    - `allow_pull`：若为 `true`，缺失的镜像将在预检时自动拉取。默认为 `false`。未设置 `image` 使用默认镜像时始终允许自动拉取，以便全新安装无需手动开关此项。
    - `extra_mounts`：额外的绑定挂载，格式为 `host:container[:ro|:rw]` 字符串，路径必须是绝对路径；省略时默认为 `:rw`。
    - `container_path_prefix` / `host_path_prefix`：Docker-outside-of-Docker 的高级覆盖项。两者同时设置后，anna 视角下位于 `container_path_prefix` 之下的绝对路径，在作为 bind-mount 源发送给守护进程前会被重写到 `host_path_prefix` 之下。常见场景下请优先使用 `ANNA_HOME_HOST` 环境变量 —— 当 anna 检测到自己运行在容器中时会自动派生这两个前缀。仅在需要按 agent 覆盖时才设置这两个字段。在宿主机部署时两者均留空。
  - Docker 后端目前**不**实现 `whitelist` 网络模式或 HTTP 中介 —— 它会像 `boxsh` 一样失败关闭。请使用 `disabled` 或 `allow_all`。
  - Docker 将工作区根目录绑定挂载到 `/workspace`，将每个只读路径挂载到 `/workspace-readonly/<index>`。依赖绝对宿主路径的脚本需要改用容器内路径。

docker 后端 agent `sandbox` 字段的 JSON 示例：

```json
{
  "backend": "docker",
  "network": { "mode": "allow_all" },
  "docker": {
    "image": "alpine:3.20",
    "user": "1000:1000",
    "allow_pull": true,
    "extra_mounts": ["/var/cache/build:/cache:ro"]
  }
}
```

## 环境变量

旧的 `ANNA_*` 前缀覆盖所有配置字段的方式已被移除。现在只识别以下环境变量：

| 变量                | 用途                            |
| ------------------- | ------------------------------- |
| `ANNA_HOME`         | 覆盖主目录（默认为 `~/.anna`）  |
| `ANNA_HOME_HOST`    | 当 anna 运行在容器中并通过宿主机 docker 守护进程工作时（Docker-outside-of-Docker），`ANNA_HOME` 对应的宿主机路径。仅在该部署下必须；其他情况忽略。 |
| `ANTHROPIC_API_KEY` | Anthropic 提供商的备用 API 密钥 |
| `OPENAI_API_KEY`    | OpenAI 提供商的备用 API 密钥    |

所有其他配置必须通过管理面板（`anna --open`）或直接在数据库中设置。

## 记忆默认设置

无损上下文管理（Lossless Context Management）设置目前是硬编码的默认值。它们将在未来版本中变为可配置。

| 设置              | 默认值 | 描述                           |
| ----------------- | ------ | ------------------------------ |
| Fresh tail count  | `20`   | 在上下文中逐字保留的最近消息数 |
| Context threshold | `0.75` | 触发压缩的上下文窗口占用比例   |
| Leaf chunk size   | `10`   | 每个叶子摘要分组的消息数       |

## 心跳

心跳仅在 `anna` 守护进程中运行。配置存储在 `settings` 表中，键为 `heartbeat`。每次心跳首先使用快速模型决定 `skip` 还是 `run`，只有 `run` 决策会被发送到主心跳会话，然后通过通知器发送。指令从 agent 的 `HEARTBEAT.md` 文件中读取。

## 插件

插件存储在 `settings` 表中，键为 `plugins`，值为 JSON 数组。每个条目包含一个指向 JS 文件的 `path` 和一个可选的 `config` 对象：

```json
[
  { "path": "~/plugins/hello.js" },
  { "path": "/abs/path/notify.js", "config": { "webhook_url": "https://example.com" } }
]
```
