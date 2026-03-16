---
title: Feishu Bot
---

anna includes a Feishu (Lark) bot that connects via WebSocket (persistent connection, no public URL required).

## Setup

1. Create a Feishu app at [Feishu Open Platform](https://open.feishu.cn/)
2. Enable the **Bot** capability in your app settings
3. Under **Event Subscriptions**, add `im.message.receive_v1` event
4. Get your App ID and App Secret from the app credentials page
5. Add credentials to `~/.anna/config.yaml`:

```yaml
channels:
  feishu:
    app_id: "YOUR_APP_ID"
    app_secret: "YOUR_APP_SECRET"
    encrypt_key: "YOUR_ENCRYPT_KEY" # from Events & Callbacks page
    verification_token: "YOUR_VERIFICATION_TOKEN" # from Events & Callbacks page
```

Or via environment:

```bash
export ANNA_FEISHU_APP_ID="YOUR_APP_ID"
export ANNA_FEISHU_APP_SECRET="YOUR_APP_SECRET"
export ANNA_FEISHU_ENCRYPT_KEY="YOUR_ENCRYPT_KEY"
export ANNA_FEISHU_VERIFICATION_TOKEN="YOUR_VERIFICATION_TOKEN"
```

6. Start the gateway:

```bash
anna gateway
```

The bot connects to Feishu via WebSocket -- no public URL or webhook setup needed.

## Streaming Responses

The bot uses Feishu's Message Update API for edit-in-place streaming. When the LLM generates tokens, the bot sends an initial reply and progressively updates it with new content, providing a smooth streaming experience.

### Tool Indicators

During tool execution, the stream shows status with emoji indicators:

| Tool     | Emoji            |
| -------- | ---------------- |
| `bash`   | lightning        |
| `read`   | book             |
| `write`  | pencil           |
| `edit`   | wrench           |
| `search` | magnifying glass |

## Supported Message Types

| Type             | Behavior                                             |
| ---------------- | ---------------------------------------------------- |
| Text             | Extracted and sent to the LLM                        |
| Image            | Downloaded, base64-encoded, sent as multimodal input |
| Post (rich text) | Raw JSON passed to the LLM for full context          |

## Group Support

On startup, the bot fetches its own `open_id` via the Feishu Bot Info API. This enables reliable @mention detection in groups and prevents the bot from responding to its own messages (infinite loop protection).

In group chats, the bot responds to @mentions. Configure behavior:

```yaml
channels:
  feishu:
    group_mode: "mention" # Respond to @mentions (default)
    # group_mode: "always"   # Respond to all group messages
    # group_mode: "disabled" # Ignore group messages entirely
```

## Access Control

Restrict which users can interact with the bot using open_ids:

```yaml
channels:
  feishu:
    allowed_ids:
      - "ou_xxxx"
```

Leave empty to allow all users. Use the `/whoami` command to get your open_id.

## Notifications

Configure a default chat for proactive notifications (scheduler results, agent-triggered alerts):

```yaml
channels:
  feishu:
    notify_chat: "oc_xxxx" # Chat ID or open_id (use /whoami)
```

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

| Field                | Description                                        | Default    |
| -------------------- | -------------------------------------------------- | ---------- |
| `app_id`             | Feishu App ID                                      | (required) |
| `app_secret`         | Feishu App Secret                                  | (required) |
| `encrypt_key`        | Event encrypt key (from Events & Callbacks)        | (optional) |
| `verification_token` | Event verification token (from Events & Callbacks) | (optional) |
| `notify_chat`        | Chat ID for proactive notifications                | (optional) |
| `group_mode`         | Group behavior: `mention`, `always`, `disabled`    | `mention`  |
| `allowed_ids`        | User open_ids allowed (empty = all)                | `[]`       |
