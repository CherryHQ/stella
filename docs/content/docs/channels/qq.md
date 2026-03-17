---
title: QQ Bot
---

anna includes a QQ bot that connects via WebSocket (persistent connection, no public URL required).

## Setup

1. Register a QQ Bot at [QQ Bot Platform](https://q.qq.com/)
2. Get your AppID and AppSecret from the bot dashboard
3. Add credentials to `~/.anna/config.yaml`:

```yaml
channels:
  qq:
    app_id: 'YOUR_APP_ID'
    app_secret: 'YOUR_APP_SECRET'
```

Or via environment:

```bash
export ANNA_QQ_APP_ID="YOUR_APP_ID"
export ANNA_QQ_APP_SECRET="YOUR_APP_SECRET"
```

4. Start the gateway:

```bash
anna gateway
```

The bot connects to QQ via WebSocket -- no public URL or webhook setup needed.

## Streaming Responses

The bot uses QQ's native Stream API for progressive response delivery. As the LLM generates tokens, updates are sent in real time without editing previous messages.

### Tool Indicators

During tool execution, the stream shows status with emoji indicators (same as Telegram):

| Tool     | Emoji            |
| -------- | ---------------- |
| `bash`   | lightning        |
| `read`   | book             |
| `write`  | pencil           |
| `edit`   | wrench           |
| `search` | magnifying glass |

## Group Support

QQ group messages are received as @mention events (`GROUP_AT_MESSAGE_CREATE`). Configure behavior:

```yaml
channels:
  qq:
    group_mode: 'mention' # Respond to @mentions (default)
    # group_mode: "always"   # Same as mention for QQ (AT events are always mentions)
    # group_mode: "disabled" # Ignore group messages entirely
```

## Access Control

Restrict which QQ users can interact with the bot using OpenIDs:

```yaml
channels:
  qq:
    allowed_ids:
      - 'USER_OPEN_ID_1'
```

Leave empty to allow all users. Use the `/whoami` command to get your OpenID.

## Image Support

Users can send images to the bot for analysis. The bot downloads image attachments, encodes them, and passes them to the AI model as multimodal content alongside any caption text.

## Commands

Send these commands as text messages to the bot:

| Command             | Description                   |
| ------------------- | ----------------------------- |
| `/start` or `/help` | Welcome and help              |
| `/new`              | Start a fresh session         |
| `/compact`          | Compress conversation history |
| `/model`            | List available models         |
| `/model <number>`   | Switch to model by number     |
| `/model <query>`    | Filter models by name         |
| `/whoami`           | Show your user ID for config  |

## Configuration Reference

| Field         | Description                                     | Default    |
| ------------- | ----------------------------------------------- | ---------- |
| `app_id`      | QQ Bot AppID                                    | (required) |
| `app_secret`  | QQ Bot AppSecret                                | (required) |
| `group_mode`  | Group behavior: `mention`, `always`, `disabled` | `mention`  |
| `allowed_ids` | User OpenIDs allowed (empty = all)              | `[]`       |
