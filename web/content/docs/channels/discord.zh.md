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
5. 在 Stella Web UI 中打开 **Channels**，创建 Discord 渠道，粘贴 bot token；如果希望机器人服务它已加入的服务器，打开**允许服务器频道**，按下文说明配置服务器访问控制，然后启用。
6. 如果渠道未自动启动，请重启 `stellad server`。

已关联的用户可以在私信中使用自己选择的 agent。默认情况下，未关联的 Discord 用户可以请求关联账号，但无法访问 agent。你可以按下文说明选择启用持久化访客私信。使用服务器频道前，请将 Discord 渠道实例绑定到一个 agent，以便 Stella 将该 agent 加入所遇频道的群聊路由。

## 可选的访客私信

将 `allow_unlinked_dm` 设为 `true`，可让未关联的 Discord 用户与此渠道实例绑定的 agent 对话。此选项默认关闭；同时还必须满足 `allow_dm: true`、渠道已启用且已绑定 agent。访客不能选择其他 agent。

访客的对话历史会在多次私信间持久保留，并在内容增长后自动压缩，但受限访客 session 不提供 profile、reflection、工具、skills、文件、workspace、plugins 或 delegation。访客只能使用 `/link`、`/help`、`/new`、`/compact` 和 `/abort`。关联 Stella 账号后，之前的访客历史不会合并到该账号的历史中。

系统会限制每位访客的聊天消息速率和每个渠道的持久访客数量；每日保留任务会删除超期未活动的访客身份及其 session。有效的账号关联码会在访客准入前处理，不占用访客聊天额度；被限流的请求仍会刷新访客的活动时间。你可以在 Web UI 中配置这些限制。管理员可以通过普通 session 管理界面查看和删除访客 session。

> **警告：** 即使启用了这些限制，访客私信仍会向公众开放你的模型，并可能产生意外费用和安全风险。请使用专用且对访客安全的 agent，并确保其 base prompt 不含任何 secret。

## 服务器访问控制

关闭**允许服务器频道**（`allow_group`）时，所有服务器消息都会被忽略；私信不受影响。打开它**并不会**自动重新开放机器人已加入的所有服务器：渠道还需要打开危险开关**接受所有服务器**（`allow_all_guilds`），或在服务器、频道、用户、角色白名单中至少填写一项。当 `allow_group` 已打开、`allow_all_guilds` 关闭且所有白名单均为空时，渠道不会响应任何服务器消息——这一 fail-closed 默认行为是刻意设计的，确保渠道不会意外变成完全开放。

`allow_all_guilds` 取代了此前"仅靠 `allow_group` 就能开放所有已加入服务器"的行为。升级时，此前已打开 `allow_group` 的渠道会保留原有覆盖范围：`allow_all_guilds` 会被自动设为 `true`。新建渠道默认关闭，需要显式添加白名单条目或打开 `allow_all_guilds`。

- **允许的服务器 ID**（`allowed_guild_ids`）——允许使用机器人的服务器（Guild）ID。
- **允许的频道 ID**（`allowed_channel_ids`）——允许使用机器人的频道 ID。匹配子区（Thread）自身 ID 或其父频道 ID 即可，因此列出一个论坛或文字频道也会覆盖其下的所有子区。
- **允许的用户 ID**（`allowed_user_ids`）——允许在服务器频道中使用机器人的 Discord 用户 ID，不受服务器、频道或角色限制。
- **允许的身份组 ID**（`allowed_role_ids`）——允许在服务器频道中使用机器人的 Discord 身份组（Role）ID；与消息作者的角色进行匹配。

只要消息匹配以上任意一项即可被处理；若**接受所有服务器**已打开，则任意白名单条目都足够。启用 Discord Developer Mode 后，可在对应的右键菜单中复制服务器、频道、用户和角色 ID。

> **警告：** **接受所有服务器**（`allow_all_guilds`）会完全跳过白名单，让机器人响应它已加入的所有服务器。仅当机器人只会被邀请到可信服务器时才启用它；其他情况请优先使用白名单。

## 使用机器人

向机器人发送私信、在服务器频道中 @提及它，或回复它发出的服务器消息。Stella 会立即发送一条临时进度消息，在其中更新已生成的文本和工具活动，并持续显示 Discord 的“正在输入”状态。长回复或带附件的回复完成后，会用普通 Discord 消息替换这条预览。

在论坛帖子和其他 thread 中，稍后 @机器人时，Stella 还会读取帖子首条消息和最近最多 20 条历史消息，包括 @机器人之前发送的内容。Thread 上下文最多占 24 KiB；超出时会优先省略较早的非首条消息。

默认情况下，未 @提及机器人的服务器消息不会作为独立消息轮次处理，也不会调用 agent。Agent 输出不会触发 `@everyone` 等 Discord 提及。

机器人在私信中支持 `/start`、`/help`、`/new`、`/compact`、`/abort`、`/whoami` 和 `/link`。Discord 将命令作为普通文本消息接收，无需注册 Discord application commands。

机器人会从 Discord 附件服务下载不超过 25 MiB 的图片和文件，并在存储可用时保存到你的私有 assets 目录。Agent 生成的图片和文件也可上传回 Discord。

## 关联账号

在 Stella 的用户或 agent 渠道设置中生成 Discord 关联码，然后用你的 Discord 账号向机器人发送 `/link <code>`。此后 Stella 会使用该身份进行私信路由和通知。

若要显式指定通知目标，请使用真实的 Discord 频道 ID。启用 Discord Developer Mode，右键点击频道并选择 **Copy Channel ID**。

## 配置参考

| 字段                             | 描述                                       | 默认值  |
| -------------------------------- | ------------------------------------------ | ------- |
| `token`                          | Discord bot token                          | 必需    |
| `allow_group`                    | 接受机器人可读服务器频道的消息             | `false` |
| `allow_all_guilds`               | 危险操作：跳过白名单，接受所有已加入服务器 | `false` |
| `allowed_guild_ids`              | 允许使用机器人的服务器（Guild）ID          | `[]`    |
| `allowed_channel_ids`            | 允许使用机器人的频道 ID（子区或父频道 ID） | `[]`    |
| `allowed_user_ids`               | 允许在服务器频道中使用机器人的用户 ID      | `[]`    |
| `allowed_role_ids`               | 允许在服务器频道中使用机器人的身份组 ID    | `[]`    |
| `allow_dm`                       | 接受账号关联和已关联用户的私信             | `true`  |
| `allow_unlinked_dm`              | 允许访客使用渠道绑定 agent 的受限私信      | `false` |
| `guest_message_limit_per_minute` | 每位访客每分钟可发送的消息和命令数         | `10`    |
| `guest_max_per_channel`          | 渠道可持久保存的访客身份上限               | `1000`  |
| `guest_retention_days`           | 访客停止活动多少天后删除身份及 session     | `30`    |
| `require_mention`                | 服务器频道消息必须 @机器人                 | `true`  |

## 故障排除

**机器人已连接但无法读取消息：** 在 Developer Portal 中启用 **Message Content Intent**，然后重启 Stella。

**机器人无法回复或上传文件：** 同时检查频道权限覆盖和服务器角色。机器人需要 View Channels、Send Messages、Read Message History 和 Attach Files。

**机器人忽略私信：** 将 `allow_dm` 设为 `true`。若要接受未关联用户作为受限访客，还需绑定专用且对访客安全的 agent，并将 `allow_unlinked_dm` 设为 `true`。

**机器人在服务器频道中不响应：** 先打开**允许服务器频道**，再 @提及机器人。若需要无 @提及的语义路由，请将 `require_mention` 设为 `false`，并确认已配置可用的群聊路由模型。如果**允许服务器频道**已经打开，但服务器、频道、用户、角色白名单均为空且**接受所有服务器**关闭——这是刻意设计的 fail-closed 行为，请添加白名单条目或打开**接受所有服务器**。

**渠道报告认证错误：** 在 Developer Portal 中重置 token，在 Stella 中替换它，切勿将 token 粘贴到聊天或日志中。
