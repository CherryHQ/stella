# Channel Setup

## Telegram Bot

1. Create a bot via @BotFather on Telegram
2. Copy the token
3. Add to config:

```yaml
channels:
  telegram:
    token: "BOT_TOKEN"
```

Or: `export ANNA_TELEGRAM_TOKEN="BOT_TOKEN"`

4. Start: `anna gateway`

### Features

- Streaming responses via Draft API (Bot API 9.3+), falls back to edit-in-place
- Image input: send photos for vision-based analysis (requires vision-capable model)
- In-chat commands: `/new`, `/compact`, `/model`, `/whoami`

### Group Support

```yaml
channels:
  telegram:
    group_mode: "mention"   # respond when @mentioned (default)
    # group_mode: "always"  # respond to all messages
    # group_mode: "disabled" # ignore group messages
```

### Access Control

```yaml
channels:
  telegram:
    allowed_ids:
      - 136345060           # Telegram user ID
```

Leave empty to allow all users. Send `/whoami` to the bot to get your user ID.

### Notifications

Configure a default chat for proactive messages (cron results, notify tool):

```yaml
channels:
  telegram:
    notify_chat: "123456789"   # chat ID (use /whoami to get it)
    channel_id: "@my_channel"  # optional broadcast channel
```

## QQ Bot

1. Register at https://q.qq.com/
2. Get AppID and AppSecret
3. Add to config:

```yaml
channels:
  qq:
    app_id: "YOUR_APP_ID"
    app_secret: "YOUR_APP_SECRET"
```

Or:

```bash
export ANNA_QQ_APP_ID="YOUR_APP_ID"
export ANNA_QQ_APP_SECRET="YOUR_APP_SECRET"
```

4. Start: `anna gateway`

Connects via WebSocket (no public URL needed).

### QQ Features

- Native Stream API for progressive responses
- C2C (private) and group @mention support
- Image input support
- Commands: `/start`, `/help`, `/new`, `/compact`, `/model`, `/whoami`

### QQ Group & Access Control

```yaml
channels:
  qq:
    group_mode: "mention"    # respond to @mentions (default)
    allowed_ids:
      - "USER_OPEN_ID_1"    # restrict by OpenID (use /whoami)
```
