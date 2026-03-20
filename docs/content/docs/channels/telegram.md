---
title: Telegram Bot
---

anna includes a Telegram bot that runs via long polling -- no webhook or public IP needed.

## Setup

1. Create a bot via [@BotFather](https://t.me/BotFather) and note the bot token
2. Run `anna --open` to launch the admin panel
3. In the admin panel: add an AI provider, then configure the Telegram channel with your bot token
4. Start the daemon:

```bash
anna
```

All channel configuration (token, group mode, allowed IDs, etc.) is managed through the admin panel. Environment variables are limited to provider API keys (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`) and `ANNA_HOME`.

## Multi-User Support

Each Telegram user is automatically resolved from their platform identity. Sessions are scoped per user per agent, with session keys in the form `{agentID}:tg:{userID}:{context}`. No manual user setup is required.

## Agent Switching

The `/agent` command lets users switch between available agents:

- `/agent` -- list all available agents
- `/agent <name>` -- switch to a specific agent

In DMs, this sets the user's default agent. In groups, it sets the active agent for the entire group.

## Streaming Responses

The bot streams LLM responses in real time with two strategies:

### Private Chats: Draft API (Bot API 9.3+)

Uses `sendMessageDraft` for smooth animated streaming without rate-limiting issues. If the API is not available, automatically falls back to edit mode.

### Group Chats: Edit-in-Place

Sends an initial message and edits it periodically (every ~1 second) as tokens arrive. The streaming message is deleted once complete, then the final response is sent.

### Tool Indicators

During tool execution, the stream shows status with emoji indicators:

| Tool     | Emoji            |
| -------- | ---------------- |
| `bash`   | lightning        |
| `read`   | book             |
| `write`  | pencil           |
| `edit`   | wrench           |
| `search` | magnifying glass |

## Image Support

The bot accepts photo messages in private chats. When you send an image:

1. The bot downloads the highest-resolution version from Telegram's file API
2. Detects the MIME type and base64-encodes the image
3. Sends it as a multimodal message (image + optional caption text) to the model
4. The model analyzes the image and responds with text

Use cases: describe screenshots, analyze diagrams, read documents from photos, etc.

If the model returns images (e.g. from tool results), they are sent back as Telegram photos after the text response.

> **Note:** Image support requires a vision-capable model (e.g. Claude 3+, GPT-4o).

## Group Support

The bot supports group chats with configurable response behavior. Set the group mode in the admin panel:

- `mention` -- only respond when @mentioned (default)
- `always` -- respond to all messages
- `disabled` -- ignore group messages entirely

## Access Control

Restrict which Telegram users can interact with the bot by adding allowed user IDs in the admin panel. Leave empty to allow all users. Use the `/whoami` command in Telegram to get your user ID.

## Notifications

The bot doubles as a notification backend. Configure a default notification chat and optional broadcast channel in the admin panel.

Used by:

- The `notify` agent tool (in server mode)
- Scheduler job result broadcasting

See [notification-system.md](/docs/features/notification-system) for the full notification architecture.

## Model Switching

Users can switch models mid-conversation via an inline keyboard triggered by the `/model` command. The model list is paginated with text filtering support.

## Commands

| Command             | Description                                             |
| ------------------- | ------------------------------------------------------- |
| `/start` or `/help` | Welcome and help                                        |
| `/new`              | Start a fresh session                                   |
| `/compact`          | Compress conversation history                           |
| `/model`            | List available models                                   |
| `/model <number>`   | Switch to model by number                               |
| `/model <query>`    | Filter models by name                                   |
| `/agent`            | List available agents                                   |
| `/agent <name>`     | Switch active agent (user default in DM, group in chat) |
| `/whoami`           | Show your user ID                                       |

## Configuration Reference

All settings below are managed through the admin panel (`anna --open`).

| Field         | Description                                     | Default    |
| ------------- | ----------------------------------------------- | ---------- |
| `token`       | Bot API token                                   | (required) |
| `notify_chat` | Chat ID for proactive notifications             |            |
| `channel_id`  | Broadcast channel (@name or numeric ID)         |            |
| `group_mode`  | Group behavior: `mention`, `always`, `disabled` | `mention`  |
| `allowed_ids` | User IDs allowed to use bot (empty = all)       | `[]`       |
