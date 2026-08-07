---
title: Discord 机器人
---

通过 Bot Gateway 连接将 Stella 接入 Discord，无需 webhook 或公网 URL。机器人支持私信、服务器频道、回复、图片、文件、命令和主动通知。

## 前提条件

- 一个正在运行的 Stella 服务器（`stellad server`）
- 至少在 Web UI 中配置了一个 AI 提供商
- 有权创建 Discord 应用并将机器人邀请到服务器

## 设置

1. 打开 [Discord Developer Portal](https://discord.com/developers/applications) 并创建应用。
2. 打开 **Bot**，按需创建机器人，然后复制 token。请将 token 作为 secret 保管。
3. 在 **Privileged Gateway Intents** 下启用 **Message Content Intent**。如果只有你的组织可以安装此机器人，请关闭 **Public Bot**。
4. 在 **OAuth2 → URL Generator** 中选择 `bot` scope，授予 **View Channels**、**Send Messages**、**Read Message History** 和 **Attach Files**，然后用生成的 URL 邀请机器人。
5. 启用 Discord Developer Mode，右键点击每个你信任且希望 Stella 服务的服务器，然后选择 **Copy Server ID**。
6. 在 Stella Web UI 中打开 **Channels**，创建 Discord 渠道，粘贴 bot token，并将可信服务器 ID 填入 **Allowed Guild IDs**，然后启用。多个 ID 之间使用英文逗号分隔。
7. 如果渠道未自动启动，请重启 `stellad server`。

已关联的用户可以在私信中使用自己选择的 agent。未关联的 Discord 用户可以请求关联账号，但无法访问 agent、session 或工具。使用服务器频道前，请将 Discord 渠道实例绑定到一个 agent，以便 Stella 将该 agent 加入所遇频道的群聊路由。

## 使用机器人

向机器人发送私信，或在允许的服务器频道中 @提及它。未列入 **Allowed Guild IDs** 的服务器消息会被忽略。默认情况下，未 @提及机器人的服务器消息也会在进入共享历史或调用 agent 前被忽略。任何能访问允许频道的成员都可以 @机器人，因此请使用 Discord 频道和 Role 权限控制访问。Agent 输出不会触发 `@everyone` 等 Discord 提及。

机器人在私信中支持 `/start`、`/help`、`/new`、`/compact`、`/abort`、`/agent`、`/whoami` 和 `/link`。暂不支持 `/model` 和服务器频道中的 `/agent`。Discord 将命令作为普通文本消息接收，无需注册 Discord application commands。

机器人会从 Discord 附件服务下载不超过 25 MiB 的图片和文件，并在存储可用时保存到你的私有 assets 目录。Agent 生成的图片和文件也可上传回 Discord。

## 关联账号

在 Stella 的用户或 agent 渠道设置中生成 Discord 关联码，然后用你的 Discord 账号向机器人发送 `/link <code>`。此后 Stella 会使用该身份进行私信路由和通知。

若要显式指定通知目标，请使用真实的 Discord 频道 ID。启用 Discord Developer Mode，右键点击频道并选择 **Copy Channel ID**。

## 配置参考

| 字段                | 描述                                    | 默认值 |
| ------------------- | --------------------------------------- | ------ |
| `token`             | Discord bot token                       | 必需   |
| `allowed_guild_ids` | 允许使用 Stella 的服务器 ID，以逗号分隔 | 无     |
| `allow_dm`          | 接受账号关联和已关联用户的私信          | `true` |
| `require_mention`   | 服务器频道消息必须 @机器人              | `true` |

## 故障排除

**机器人已连接但无法读取消息：** 在 Developer Portal 中启用 **Message Content Intent**，然后重启 Stella。

**机器人无法回复或上传文件：** 同时检查频道权限覆盖和服务器角色。机器人需要 View Channels、Send Messages、Read Message History 和 Attach Files。

**机器人忽略私信：** 将 `allow_dm` 设为 `true`。未关联用户只能提交账号关联码，无法调用 agent、创建 session 或访问 memory 和工具。

**机器人在服务器频道中不响应：** 先将该服务器 ID 加入 **Allowed Guild IDs**，再 @提及机器人。若需要无 @提及的语义路由，请将 `require_mention` 设为 `false`，并确认已配置可用的群聊路由模型。

**渠道报告认证错误：** 在 Developer Portal 中重置 token，在 Stella 中替换它，切勿将 token 粘贴到聊天或日志中。
