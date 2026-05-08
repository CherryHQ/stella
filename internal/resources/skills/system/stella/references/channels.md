# Channel setup

All channel configuration is stored in the database and managed via the admin panel (`stella --open`). Each channel row is a bot instance with an `id`, platform `type`, optional dedicated `agent_id`, enabled flag, and JSON config. Multiple instances can share the same type, such as two Feishu bots bound to different agents.

## Agent routing

- **DMs**: Use the user's default agent (set via `/agent` command)
- **Groups**: Use the group's assigned agent (set via `/agent` command in the group)
- **Dedicated channels**: If a channel instance has `agent_id`, all chats on that channel use the bound agent and `/agent` switching is disabled there
- **Fallback**: First enabled agent

Commands available in all channels:
- `/agent` -- List available agents or switch to a specific one
- `/new` -- Start a fresh session (with the current agent)
- `/compact` -- Compress conversation history
- `/model` -- Switch model interactively
- `/whoami` -- Show your user/chat ID

## Telegram bot

1. Create a bot via @BotFather on Telegram
2. Copy the token
3. Run `stella --open` and add it in the Channels tab

Telegram channel config (JSON):
```json
{
  "token": "BOT_TOKEN",
  "channel_id": "@my_channel",
  "group_mode": "mention",
  "enable_notify": true
}
```

Or set `STELLA_TELEGRAM_TOKEN` env var for the token only.

4. Start: `stella`

### Features

- Streaming responses via Draft API (Bot API 9.3+), falls back to edit-in-place
- Image input: send photos for vision-based analysis (requires vision-capable model)
- Multi-agent: `/agent` to list or switch agents per DM or group
- In-chat commands: `/new`, `/compact`, `/model`, `/agent`, `/whoami`

### Group support

Set `group_mode` in the channel config:
- `"mention"` -- respond when @mentioned (default)
- `"always"` -- respond to all messages
- `"disabled"` -- ignore group messages

### Access control

Access control is handled by the RBAC system (auth_identities + policy engine). Use the admin panel to manage user roles and permissions.

### Notifications

Set `enable_notify: true` for proactive messages (scheduler results, notify tool). Notification targets are resolved automatically from auth_identities.

## QQ bot

1. Register at https://q.qq.com/
2. Get AppID and AppSecret
3. Run `stella --open` and add it in the Channels tab

QQ channel config (JSON):
```json
{
  "app_id": "YOUR_APP_ID",
  "app_secret": "YOUR_APP_SECRET",
  "group_mode": "mention",
  "enable_notify": false
}
```

4. Start: `stella`

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
4. Run `stella --open` and add it in the Channels tab

Feishu channel config (JSON):
```json
{
  "app_id": "YOUR_APP_ID",
  "app_secret": "YOUR_APP_SECRET",
  "encrypt_key": "",
  "verification_token": "",
  "group_mode": "mention",
  "enable_notify": false
}
```

5. Start: `stella`

Connects via WebSocket (no public URL or webhook needed).

### Feishu features

- Edit-in-place streaming for progressive responses
- Private (p2p) and group @mention support
- Commands: `/new`, `/compact`, `/model`, `/agent`, `/whoami`
- Chat transport only. Workspace automation moved out of builtin `feishu_*` tools; add a `lark-cli` skill yourself if you want that workflow.

## WeChat bot (iLink)

1. Run `stella --open` and go to the Channels tab
2. Click "Scan QR to Login" in the WeChat section
3. Scan the QR code with your WeChat account
4. Credentials are saved automatically on confirmation

WeChat channel config (JSON):
```json
{
  "bot_token": "OBTAINED_VIA_QR",
  "base_url": "https://ilinkai.weixin.qq.com",
  "bot_id": "AUTO",
  "user_id": "AUTO"
}
```

5. Start: `stella`

Uses long-polling via iLink Bot API (no public URL needed). DM only for v1.

### WeChat features

- Long-polling message receipt via `getupdates`
- Text messaging with 2000-char smart splitting
- Image input/output with AES-128-ECB encryption
- Typing indicators while processing
- QR-code-based login via admin panel
- Commands: `/start`, `/help`, `/new`, `/compact`, `/model`, `/agent`, `/whoami`

### WeChat limitations

- Notifications require a cached `context_token` (in-memory only, lost on restart)
- No group chat support (DM only)
- Session expiry requires manual QR re-scan from admin panel
