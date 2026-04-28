---
title: QQ Bot
---

anna includes a QQ bot that connects via WebSocket (persistent connection, no public URL required).

## Setup

1. Register a QQ Bot at [QQ Bot Platform](https://q.qq.com/) and get your AppID and AppSecret
2. Run `anna --open` to launch the admin panel
3. In the admin panel: add an AI provider, then configure the QQ channel with your AppID and AppSecret
4. Start the daemon:

```bash
anna
```

All channel configuration (credentials, group mode, allowed IDs, etc.) is managed through the admin panel. Environment variables are limited to provider API keys (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`) and `ANNA_HOME`.

## Multi-User Support

Each QQ user is automatically resolved from their platform identity. Sessions are scoped per user per agent. No manual user setup is required. The QQ channel currently uses the default agent (the `/agent` command is not yet available for QQ).

## Streaming Responses

The bot uses QQ's native Stream API for progressive response delivery. As the LLM generates tokens, updates are sent in real time without editing previous messages.

### Tool Indicators

During tool execution, the stream shows status with emoji indicators:

| Tool     | Emoji            |
| -------- | ---------------- |
| `bash`   | lightning        |
| `read`   | book             |
| `write`  | pencil           |
| `edit`   | wrench           |
| `search` | magnifying glass |

## Group Support

QQ group messages are received as @mention events (`GROUP_AT_MESSAGE_CREATE`). Set the group mode in the admin panel:

- `mention` -- respond to @mentions (default)
- `always` -- same as mention for QQ (AT events are always mentions)
- `disabled` -- ignore group messages entirely

## Access Control

Restrict which QQ users can interact with the bot by adding allowed OpenIDs in the admin panel. Leave empty to allow all users. Use the `/whoami` command to get your OpenID.

## Image Support

Users can send images to the bot for analysis. The bot downloads image attachments, encodes them, and passes them to the AI model as multimodal content alongside any caption text.

## File Support

Users can send file attachments (non-image, non-video) to the bot. When a file is received:

1. The bot downloads the file from the QQ attachment URL
2. Saves it to the user's private assets directory on disk
3. A kreuzberg extraction hint is passed to the agent so it can read the file content

The agent can then use the `kreuzberg extract` command to parse the file.

> **Note:** File uploads require the kreuzberg skill to be enabled for the active agent.

## Commands

Send these commands as text messages to the bot:

| Command             | Description                     |
| ------------------- | ------------------------------- |
| `/start` or `/help` | Welcome and help                |
| `/new`              | Start a fresh session           |
| `/compact`          | Compress conversation history   |
| `/abort`            | Cancel the in-progress response |
| `/model`            | List available models           |
| `/model <number>`   | Switch to model by number       |
| `/model <query>`    | Filter models by name           |
| `/whoami`           | Show your user ID for config    |

## Configuration Reference

All settings below are managed through the admin panel (`anna --open`).

| Field         | Description                                     | Default    |
| ------------- | ----------------------------------------------- | ---------- |
| `app_id`      | QQ Bot AppID                                    | (required) |
| `app_secret`  | QQ Bot AppSecret                                | (required) |
| `group_mode`  | Group behavior: `mention`, `always`, `disabled` | `mention`  |
| `allowed_ids` | User OpenIDs allowed (empty = all)              | `[]`       |
