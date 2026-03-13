# Channel setup

## Telegram bot

1. Create a bot via @BotFather on Telegram
2. Copy the token
3. Run `anna onboard` and add it in the Channels tab, or add to config:

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

### Group support

```yaml
channels:
  telegram:
    group_mode: "mention"   # respond when @mentioned (default)
    # group_mode: "always"  # respond to all messages
    # group_mode: "disabled" # ignore group messages
```

### Access control

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
    enable_notify: true
    notify_chat: "123456789"   # chat ID (use /whoami to get it)
    channel_id: "@my_channel"  # optional broadcast channel
```

## QQ bot

1. Register at https://q.qq.com/
2. Get AppID and AppSecret
3. Run `anna onboard` and add it in the Channels tab, or add to config:

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

### QQ features

- Native Stream API for progressive responses
- C2C (private) and group @mention support
- Image input support
- Commands: `/start`, `/help`, `/new`, `/compact`, `/model`, `/whoami`

### QQ group and access control

```yaml
channels:
  qq:
    group_mode: "mention"    # respond to @mentions (default)
    allowed_ids:
      - "USER_OPEN_ID_1"    # restrict by OpenID (use /whoami)
```

## Feishu bot

1. Create an app at the Feishu Developer Console
2. Get AppID and AppSecret
3. Enable the Bot capability and subscribe to message events
4. Run `anna onboard` and add it in the Channels tab, or add to config:

```yaml
channels:
  feishu:
    app_id: "YOUR_APP_ID"
    app_secret: "YOUR_APP_SECRET"
    encrypt_key: ""                # event encrypt key (from developer console)
    verification_token: ""         # event verification token (from developer console)
```

Or:

```bash
export ANNA_FEISHU_APP_ID="YOUR_APP_ID"
export ANNA_FEISHU_APP_SECRET="YOUR_APP_SECRET"
```

5. Start: `anna gateway`

Connects via WebSocket (no public URL or webhook needed).

### Feishu features

- Edit-in-place streaming for progressive responses
- Private (p2p) and group @mention support
- Commands: `/new`, `/compact`, `/model`, `/whoami`

### Feishu group and access control

```yaml
channels:
  feishu:
    group_mode: "mention"    # respond to @mentions (default)
    allowed_ids:
      - "ou_xxxxxx"          # restrict by open_id (use /whoami)
```

### Feishu notifications

```yaml
channels:
  feishu:
    enable_notify: true
    notify_chat: "oc_xxx"   # chat or open_id for notifications (use /whoami)
```
