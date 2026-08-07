# Channel setup

All channel configuration is stored in the database and managed via the Web UI (`http://localhost:25678` by default). Each channel row is a bot instance with an `id`, platform `type`, optional dedicated `agent_id`, enabled flag, and JSON config. Multiple instances can share the same type, such as two Feishu bots bound to different agents.

## Agent routing

- **DMs**: Use the user's default agent (set via `/agent` command)
- **Groups**: Use the group's assigned agent (set via `/agent` command in the group)
- **Dedicated channels**: If a channel instance has `agent_id`, all chats on that channel use the bound agent and `/agent` switching is disabled there
- **Fallback**: First enabled agent

In the Web UI, groups appear alongside agents in the main agent sidebar. A group conversation uses the same transcript and composer shape as an agent chat, plus a group monitor for member activity and shared upload context. Mention a member agent with `@<agent-id>` when you need deterministic routing in a web group.

Commands available in all channels:

- `/agent` -- List available agents or switch to a specific one
- `/new` -- Start a fresh session; the previous one is archived and stays searchable
- `/compact` -- Compress the current session in place (same session, shorter context)
- `/model` -- Switch model interactively
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
  "enable_notify": true
}
```

Or set `STELLA_TELEGRAM_TOKEN` env var for the token only.

4. Start: `stellad server`

### Features

- Streaming responses via Draft API (Bot API 9.3+), falls back to edit-in-place
- Image input: configure **Settings -> Vision** for ordinary-session baselines; only a model declaring image input receives active-turn pixels, and group history without a baseline uses the unavailable marker
- Multi-agent: `/agent` to list or switch agents per DM or group
- In-chat commands: `/new`, `/compact`, `/model`, `/agent`, `/whoami`

### Group support

In group chats the bot participates automatically and group routing is decided semantically. @mentions always route to the mentioned bot. To stop a bot from participating in a group, remove it from that group.

### Access control

Channel access is enforced by Stella's Authority-based services. Use the Web UI to manage users and channel configuration.

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

The bot connects through Discord Gateway, so Stella does not need a public webhook URL. It supports direct messages, guild channels, attachments, replies, `/agent` in direct messages, and shared channel commands. `allowed_guild_ids` is comma-separated and fail-closed: leaving it empty disables all guild messages while direct messages continue to work. `allow_dm` defaults to `true`; disable it for a guild-only bot. `require_mention` defaults to `true`, so unmentioned guild messages are ignored before reaching shared history or an agent. Every member who can access an allowed channel can mention the bot, so use Discord channel and role permissions for access control. Bind the channel instance to an agent before using it in guild channels. `/model` and guild-channel `/agent` are not yet supported. Use a Discord channel ID as an explicit notification target; do not invent one.

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
- Commands: `/start`, `/help`, `/new`, `/compact`, `/model`, `/whoami`

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
  "enable_notify": false
}
```

5. Start: `stellad server`

Connects via WebSocket (no public URL or webhook needed).

### Feishu features

- Edit-in-place streaming for progressive responses
- Private (p2p) and group @mention support
- Commands: `/new`, `/compact`, `/model`, `/agent`, `/whoami`
- Feishu OAuth login and Feishu channel chat share `union_id`; Feishu OAuth login immediately links the channel identity to the same Stella user, with first channel message as a fallback.
- Chat transport only.

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
- Commands: `/start`, `/help`, `/new`, `/compact`, `/model`, `/agent`, `/whoami`

### WeChat limitations

- Notifications require a cached `context_token` (in-memory only, lost on restart)
- No group chat support (DM only)
- Session expiry requires a fresh scan-created bot or a fresh manual credential bundle
