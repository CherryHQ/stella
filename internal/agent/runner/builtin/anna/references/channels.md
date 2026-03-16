# Channel setup

All channel configuration is stored in the database and managed via the admin panel (`anna onboard`). A single bot per platform serves all agents.

## Agent routing

- **DMs**: Use the user's default agent (set via `/agent` command)
- **Groups**: Use the group's assigned agent (set via `/agent` command in the group)
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
3. Run `anna onboard` and add it in the Channels tab

Telegram channel config (JSON):
```json
{
  "token": "BOT_TOKEN",
  "notify_chat": "123456789",
  "channel_id": "@my_channel",
  "group_mode": "mention",
  "allowed_ids": [136345060],
  "enable_notify": true
}
```

Or set `ANNA_TELEGRAM_TOKEN` env var for the token only.

4. Start: `anna gateway`

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

Add user IDs to `allowed_ids` array. Leave empty to allow all users. Send `/whoami` to the bot to get your user ID.

### Notifications

Set `enable_notify: true` and `notify_chat` to a chat ID for proactive messages (scheduler results, notify tool).

## QQ bot

1. Register at https://q.qq.com/
2. Get AppID and AppSecret
3. Run `anna onboard` and add it in the Channels tab

QQ channel config (JSON):
```json
{
  "app_id": "YOUR_APP_ID",
  "app_secret": "YOUR_APP_SECRET",
  "group_mode": "mention",
  "allowed_ids": [],
  "enable_notify": false
}
```

4. Start: `anna gateway`

Connects via WebSocket (no public URL needed). QQ currently uses the default agent only.

### QQ features

- Native Stream API for progressive responses
- C2C (private) and group @mention support
- Image input support
- Commands: `/start`, `/help`, `/new`, `/compact`, `/model`, `/whoami`

## Feishu bot

1. Create an app at the Feishu Developer Console
2. Get AppID and AppSecret
3. Enable the Bot capability and subscribe to message events
4. Run `anna onboard` and add it in the Channels tab

Feishu channel config (JSON):
```json
{
  "app_id": "YOUR_APP_ID",
  "app_secret": "YOUR_APP_SECRET",
  "encrypt_key": "",
  "verification_token": "",
  "notify_chat": "oc_xxx",
  "group_mode": "mention",
  "allowed_ids": [],
  "enable_notify": false
}
```

5. Start: `anna gateway`

Connects via WebSocket (no public URL or webhook needed). Feishu currently uses the default agent only.

### Feishu features

- Edit-in-place streaming for progressive responses
- Private (p2p) and group @mention support
- Commands: `/new`, `/compact`, `/model`, `/whoami`
