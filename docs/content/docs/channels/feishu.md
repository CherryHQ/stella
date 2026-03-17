---
title: Feishu Bot
---

anna includes a Feishu (Lark) bot that connects via WebSocket (persistent connection, no public URL required).

## Setup

1. Create a Feishu app at [Feishu Open Platform](https://open.feishu.cn/)
2. Enable the **Bot** capability in your app settings
3. Under **Event Subscriptions**, add `im.message.receive_v1` event
4. Get your App ID, App Secret, Encrypt Key, and Verification Token from the app settings
5. Run `anna onboard` to launch the admin panel
6. In the admin panel: add an AI provider, then configure the Feishu channel with your app credentials
7. Start the gateway:

```bash
anna gateway
```

All channel configuration (credentials, group mode, allowed IDs, etc.) is managed through the admin panel. Environment variables are limited to provider API keys (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`) and `ANNA_HOME`.

## Multi-User Support

Each Feishu user is automatically resolved from their platform identity. Sessions are scoped per user per agent. No manual user setup is required. The Feishu channel currently uses the default agent (the `/agent` command is not yet available for Feishu).

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

In group chats, the bot responds to @mentions. Set the group mode in the admin panel:

- `mention` -- respond to @mentions (default)
- `always` -- respond to all group messages
- `disabled` -- ignore group messages entirely

## Access Control

Restrict which users can interact with the bot by adding allowed open_ids in the admin panel. Leave empty to allow all users. Use the `/whoami` command to get your open_id.

## Notifications

Configure a default notification chat in the admin panel for proactive notifications (scheduler results, agent-triggered alerts).

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

All settings below are managed through the `anna onboard` admin panel.

| Field                | Description                                        | Default    |
| -------------------- | -------------------------------------------------- | ---------- |
| `app_id`             | Feishu App ID                                      | (required) |
| `app_secret`         | Feishu App Secret                                  | (required) |
| `encrypt_key`        | Event encrypt key (from Events & Callbacks)        | (optional) |
| `verification_token` | Event verification token (from Events & Callbacks) | (optional) |
| `notify_chat`        | Chat ID for proactive notifications                | (optional) |
| `group_mode`         | Group behavior: `mention`, `always`, `disabled`    | `mention`  |
| `allowed_ids`        | User open_ids allowed (empty = all)                | `[]`       |
