# Channel setup

All channel configuration is stored in the database and managed via the Web UI (`http://localhost:25678` by default). Each channel row is a bot instance with an `id`, platform `type`, optional dedicated `agent_id`, enabled flag, and JSON config. Multiple instances can share the same type, such as two Feishu bots bound to different agents.

## Agent routing

- **DMs**: Use the user's default agent
- **Groups**: Use the group's assigned agent
- **Dedicated channels**: If a channel instance has `agent_id`, all chats on that channel use the bound agent
- **Fallback**: First enabled agent

In the Web UI, groups appear alongside agents in the main agent sidebar. A group conversation uses the same transcript and composer shape as an agent chat, plus a group monitor for member activity and shared upload context. Mention a member agent with `@<agent-id>` when you need deterministic routing in a web group.

Commands available in all channels:

- `/new` -- Start a fresh session; the previous one is archived and leaves memory search
- `/compact` -- Compress the current session in place (same session, shorter context)
- `/whoami` -- Show your user/chat ID

`/new` works in direct messages only. A group's context is shared by every
member, so a group `/new` is refused and resets nothing; `/compact` is not
available in groups either. Neither command enters the group's shared history.
If a user asks in words for a fresh session, point them at `/new` in a direct
message rather than claiming to have reset anything.

## Telegram bot

1. Create a bot via @BotFather on Telegram
2. Copy the token
3. Open the Web UI and add it in the Channels tab

Telegram channel config (JSON):

```json
{
  "token": "BOT_TOKEN",
  "channel_id": "@my_channel",
  "allowed_chat_ids": "-1001234567890",
  "allow_dm": true,
  "allow_unlinked_dm": false,
  "guest_message_limit_per_minute": 10,
  "guest_max_per_channel": 1000,
  "guest_retention_days": 30,
  "require_mention": true,
  "enable_notify": true
}
```

Or set `STELLA_TELEGRAM_TOKEN` env var for the token only.

4. Start: `stellad server`

### Features

- Streaming responses via Draft API (Bot API 9.3+), falls back to edit-in-place
- Image input: configure **Settings -> Vision** for ordinary-session baselines; only a model declaring image input receives active-turn pixels, and group history without a baseline uses the unavailable marker
- In-chat commands: `/new`, `/compact`, `/abort`, `/whoami`

### Group support

`allowed_chat_ids` is a comma-separated, fail-closed group allowlist. Group messages require a bot mention by default. Disable `require_mention` only when semantic routing is intended and BotFather privacy mode is disabled. Every member of an allowed group can address the bound agent, so allow only trusted groups.

### Access control

Direct messages can be disabled with `allow_dm`. Unlinked private senders are denied unless `allow_unlinked_dm` is explicitly enabled on a channel-bound agent. Guests reuse persistent restricted sessions with configurable rate, count, and retention limits, but have no profile, reflection, tools, skills, files, workspace, plugins, or delegation. Guest attachments are rejected before download. Enabling guest access exposes model usage publicly; recommend a dedicated guest-safe agent whose base prompt contains no secrets.

### Notifications

Set `enable_notify: true` for proactive messages (scheduler results, notify tool). Notification targets are resolved automatically from auth_identities.

## Discord bot

1. Create an application and bot in the [Discord Developer Portal](https://discord.com/developers/applications)
2. Enable the **Message Content Intent** on the bot page; turn off **Public Bot** for private deployments
3. Invite the bot with permission to view channels, send messages, read message history, and attach files
4. Enable Discord Developer Mode and copy the server IDs that Stella should trust
5. Open the Web UI, add a Discord channel, paste the bot token, and enter the trusted server IDs under **Allowed Guild IDs**

Discord channel config (JSON):

```json
{
  "token": "BOT_TOKEN",
  "allowed_guild_ids": "SERVER_ID_1,SERVER_ID_2",
  "allow_dm": true,
  "allow_unlinked_dm": false,
  "guest_message_limit_per_minute": 10,
  "guest_max_per_channel": 1000,
  "guest_retention_days": 30,
  "require_mention": true
}
```

The bot connects through Discord Gateway, so Stella does not need a public webhook URL. It supports direct messages, guild channels, attachments, replies, and shared channel commands. `allowed_guild_ids` is comma-separated and fail-closed: leaving it empty disables all guild messages while direct messages continue to work. `allow_dm` defaults to `true`; disable it for a guild-only bot. `require_mention` defaults to `true`, so unmentioned guild messages are ignored before reaching shared history or an agent. Every member who can access an allowed channel can mention the bot, so use Discord channel and role permissions for access control. Bind the channel instance to an agent before using it in guild channels. Use a Discord channel ID as an explicit notification target; do not invent one.

Unlinked direct messages are off by default. Setting `allow_unlinked_dm: true` requires `allow_dm: true`, an enabled Discord channel, and a channel-bound agent; guests can use only that agent. Guest conversation history persists and compacts, but profile, reflection, tools, skills, files, workspace, plugins, and delegation are unavailable. Only `/link`, `/help`, `/new`, `/compact`, and `/abort` are allowed, and linking does not merge old guest history. Per-guest rate, per-channel count, and inactivity-retention limits are configurable. It still exposes model use publicly, so warn operators about cost and security and recommend a dedicated guest-safe agent whose base prompt contains no secrets.

## QQ bot

1. Register at https://q.qq.com/
2. Get AppID and AppSecret
3. Open the Web UI and add it in the Channels tab

QQ channel config (JSON):

```json
{
  "app_id": "YOUR_APP_ID",
  "app_secret": "YOUR_APP_SECRET",
  "enable_notify": false
}
```

4. Start: `stellad server`

Connects via WebSocket (no public URL needed). QQ supports the same channel instance routing as other chat channels.

### QQ features

- Native Stream API for progressive responses
- C2C (private) and group @mention support
- Image input support
- Commands: `/start`, `/help`, `/new`, `/compact`, `/abort`, `/whoami`

## Feishu bot

1. Create an app at the Feishu Developer Console
2. Get AppID and AppSecret
3. Enable the Bot capability and subscribe to message events
4. Open the Web UI and add it in the Channels tab

Feishu channel config (JSON):

```json
{
  "app_id": "YOUR_APP_ID",
  "app_secret": "YOUR_APP_SECRET",
  "encrypt_key": "",
  "verification_token": "",
  "allowed_chat_ids": "oc_trusted_group",
  "allow_dm": true,
  "allow_unlinked_dm": false,
  "guest_message_limit_per_minute": 10,
  "guest_max_per_channel": 1000,
  "guest_retention_days": 30,
  "require_mention": true,
  "enable_notify": false
}
```

5. Start: `stellad server`

Connects via WebSocket (no public URL or webhook needed).

### Feishu features

- Edit-in-place streaming for progressive responses
- Private (p2p) and group @mention support
- Commands: `/new`, `/compact`, `/abort`, `/whoami`
- Feishu OAuth login and Feishu channel chat share `union_id`; Feishu OAuth login immediately links the channel identity to the same Stella user, with first channel message as a fallback.
- Chat transport only.

`allowed_chat_ids` is a comma-separated, fail-closed Feishu group `chat_id` allowlist, and `require_mention` defaults to `true`. `allow_dm` controls private chat, account linking, and private-message auto-provisioning. `allow_unlinked_dm` has the same restricted guest semantics and operational limits as Telegram and Discord; it is disabled by default and requires a channel-bound agent. Guest attachments are rejected before download.

## DingTalk bot

1. Create and publish an internal enterprise application in the DingTalk Developer Console
2. Add the Robot capability and choose Stream mode for message receiving
3. Copy the Client ID and Client Secret
4. Add a DingTalk channel in the Web UI and bind an enabled agent for group use

DingTalk channel config (JSON):

```json
{
  "client_id": "YOUR_CLIENT_ID",
  "client_secret": "YOUR_CLIENT_SECRET",
  "allowed_conversation_ids": "cid_trusted_group",
  "allow_dm": true,
  "allow_unlinked_dm": false,
  "guest_message_limit_per_minute": 10,
  "guest_max_per_channel": 1000,
  "guest_retention_days": 30,
  "require_mention": true
}
```

DingTalk connects through Stream mode, so Stella needs no public callback URL. It supports direct text messages, trusted group @mentions, account linking, shared commands, and final text replies. `allowed_conversation_ids` is comma-separated and fail-closed; an empty value disables every group while DMs continue to work. To discover a group ID without opening access, @mention the bot once and read the rejected `conversation_id` from the Stella server log.

Unlinked direct messages use the same restricted guest policy and limits as Telegram, Discord, and Feishu. Attachments and rich messages are not yet supported. Notifications use temporary session Webhooks cached from inbound messages; after process restart or Webhook expiry, the user or group must message the bot again before notifications can resume.

## WeChat bot (iLink)

1. Open the Web UI and go to Channels → New Channel
2. Select Weixin, enter a name, keep the fixed channel ID `weixin`, and bind an enabled agent
3. Click "Scan to create WeChat bot"
4. Scan the QR code with WeChat and confirm bot creation
5. Stella saves the returned iLink credentials automatically

WeChat channel config (JSON):

```json
{
  "bot_token": "OBTAINED_VIA_SCAN_OR_MANUAL_SETUP",
  "base_url": "https://ilinkai.weixin.qq.com",
  "bot_id": "AUTO",
  "user_id": "AUTO"
}
```

Manual setup remains available in the Web UI under "I already have WeChat iLink credentials". WeChat is singleton-only in Stella because one iLink account does not support multiple independent bots.

Uses long-polling via iLink Bot API (no public URL needed). DM only for v1.

### WeChat features

- Long-polling message receipt via `getupdates`
- Text messaging with 2000-char smart splitting
- Image input/output with AES-128-ECB encryption
- Typing indicators while processing
- QR-code-based bot creation via Web UI
- Commands: `/start`, `/help`, `/new`, `/compact`, `/abort`, `/whoami`

### WeChat limitations

- Notifications require a cached `context_token` (in-memory only, lost on restart)
- No group chat support (DM only)
- Session expiry requires a fresh scan-created bot or a fresh manual credential bundle
