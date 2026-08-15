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
  "allow_group": false,
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

`allow_group` is one fail-closed switch for every group the bot was added to; it defaults to `false`. Group messages require a bot mention by default. Disable `require_mention` only when semantic routing is intended and BotFather privacy mode is disabled. Every member of a group the bot joined can address the bound agent, so control who can add the bot to a group.

### Access control

Direct messages can be disabled with `allow_dm`. Unlinked private senders are denied unless `allow_unlinked_dm` is explicitly enabled on a channel-bound agent. Guests reuse persistent restricted sessions with configurable rate, count, and retention limits, but have no profile, reflection, tools, skills, files, workspace, plugins, or delegation. Guest attachments are rejected before download. Enabling guest access exposes model usage publicly; recommend a dedicated guest-safe agent whose base prompt contains no secrets.

### Notifications

Set `enable_notify: true` for proactive messages (scheduler results, notify tool). Notification targets are resolved automatically from auth_identities.

## Discord bot

1. Create an application and bot in the [Discord Developer Portal](https://discord.com/developers/applications)
2. Enable the **Message Content Intent** on the bot page; turn off **Public Bot** for private deployments
3. Invite the bot with permission to view channels, send messages, read message history, and attach files
4. Open the Web UI, add a Discord channel, paste the bot token, and turn on **Allow server channels** if the bot should serve the servers it joined

Discord channel config (JSON):

```json
{
  "token": "BOT_TOKEN",
  "allow_group": false,
  "allow_dm": true,
  "allow_unlinked_dm": false,
  "guest_message_limit_per_minute": 10,
  "guest_max_per_channel": 1000,
  "guest_retention_days": 30,
  "require_mention": true
}
```

The bot connects through Discord Gateway, so Stella does not need a public webhook URL. It supports direct messages, guild channels, forum and text threads, attachments, replies, and shared channel commands. Long-running replies show a typing indicator plus an editable progress message with generated text and tool activity; a reply to the bot also satisfies the default mention requirement. When first mentioned later in a thread, Stella imports the starter and bounded recent human history before answering. `allow_group` is one fail-closed switch for server channels: while it is `false` all guild messages are disabled and direct messages continue to work. `allow_dm` defaults to `true`; disable it for a guild-only bot. `require_mention` defaults to `true`, so unmentioned guild messages are ignored before reaching shared history or an agent. Every member who can access a channel the bot can read may mention it, so use Discord channel and role permissions for access control and invite the bot only to trusted servers. Bind the channel instance to an agent before using it in guild channels. Use a Discord channel ID as an explicit notification target; do not invent one.

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
  "allow_group": false,
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

`allow_group` is one fail-closed switch for Feishu group chats and defaults to `false`; `require_mention` defaults to `true`. `allow_dm` controls private chat, account linking, and private-message auto-provisioning. `allow_unlinked_dm` has the same restricted guest semantics and operational limits as Telegram and Discord; it is disabled by default and requires a channel-bound agent. Guest attachments are rejected before download.

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
  "allow_group": false,
  "allow_dm": true,
  "allow_unlinked_dm": false,
  "guest_message_limit_per_minute": 10,
  "guest_max_per_channel": 1000,
  "guest_retention_days": 30,
  "require_mention": true
}
```

DingTalk connects through Stream mode, so Stella needs no public callback URL. It supports direct text messages, group @mentions, account linking, shared commands, and final text replies. `allow_group` is one fail-closed switch for group conversations; while it is `false` every group is disabled and DMs continue to work.

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
