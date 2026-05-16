---
title: QQ Bot
---

Stella includes a QQ bot that connects via WebSocket -- a persistent connection with no public URL required. You can chat with your AI assistant directly in QQ, send images and files for analysis, and use it in group chats.

## Prerequisites

Before you start, make sure you have:

- A running Stella server (`stella server`)
- At least one AI provider configured in the admin panel (e.g. Anthropic, OpenAI)
- A QQ Bot registered at the [QQ Bot Platform](https://q.qq.com/) with your AppID and AppSecret

## Setup

1. Register a QQ Bot at [QQ Bot Platform](https://q.qq.com/) and note your AppID and AppSecret.
2. Start your Stella server if it is not already running:

   ```bash
   stella server
   ```

3. Open the admin panel at `http://localhost:25678`.
4. Go to the **Channels** page and add a new QQ channel instance.
5. Enter your AppID and AppSecret, then save.
6. Restart `stella server` to activate the new channel.

All channel configuration (credentials, group mode, allowed IDs, etc.) is managed through the admin panel. Environment variables are limited to provider API keys (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`) and `STELLA_HOME`.

## Multi-User Support

Each QQ user is automatically identified from their platform identity. Sessions are scoped per user per agent. No manual user setup is required. The QQ channel currently uses the default agent (the `/agent` command is not yet available for QQ).

## Streaming Responses

The bot uses QQ's native Stream API for progressive response delivery. As the model generates tokens, updates are sent to you in real time without editing previous messages.

### Tool Indicators

While the assistant runs tools, you will see status indicators in the stream:

| Tool     | Indicator        |
| -------- | ---------------- |
| `bash`   | lightning        |
| `read`   | book             |
| `write`  | pencil           |
| `edit`   | wrench           |
| `search` | magnifying glass |

## Group Support

QQ group messages are received as @mention events. You can set the group mode in the admin panel:

- `mention` -- respond to @mentions (default)
- `always` -- same as mention for QQ (AT events are always mentions)
- `disabled` -- ignore group messages entirely

## Access Control

You can restrict which QQ users can interact with your bot by adding allowed OpenIDs in the admin panel. Leave the list empty to allow all users. Use the `/whoami` command to find your OpenID.

## Image Support

You can send images to the bot for analysis. The bot downloads image attachments, encodes them, and passes them to the AI model as multimodal content alongside any caption text.

## File Support

You can send file attachments (non-image, non-video) to the bot. When you send a file:

1. The bot downloads the file from the QQ attachment URL
2. Saves it to your private assets directory on disk
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

All settings below are managed through the admin panel.

| Field         | Description                                     | Default    |
| ------------- | ----------------------------------------------- | ---------- |
| `app_id`      | QQ Bot AppID                                    | (required) |
| `app_secret`  | QQ Bot AppSecret                                | (required) |
| `group_mode`  | Group behavior: `mention`, `always`, `disabled` | `mention`  |
| `allowed_ids` | User OpenIDs allowed (empty = all)              | `[]`       |

## Troubleshooting

**Bot not responding to messages?**

- Make sure `stella server` is running and the QQ channel is configured in the admin panel.
- Verify your AppID and AppSecret are correct.
- If you set up access control, confirm your QQ OpenID is in the `allowed_ids` list. Send `/whoami` to the bot to check.

**Bot not responding in groups?**

- Check the `group_mode` setting in the admin panel. The default is `mention`, which means you need to @mention the bot.
- QQ group messages require an @mention to trigger the bot, even in `always` mode.

**Files not being analyzed?**

- Make sure the kreuzberg skill is enabled for the active agent.

**Connection issues?**

- The bot uses a persistent WebSocket connection. If it drops, restart `stella server`.
- Check your network connectivity and ensure the QQ Bot Platform credentials are valid.
