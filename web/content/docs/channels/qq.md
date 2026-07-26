---
title: QQ Bot
---

Stella includes a QQ bot that connects via WebSocket -- a persistent connection with no public URL required. You can chat with your AI assistant directly in QQ, send images and files for analysis, and use it in group chats.

## Prerequisites

Before you start, make sure you have:

- A running Stella server (`stellad server`)
- At least one AI provider configured in the Web UI (e.g. Anthropic, OpenAI)
- A QQ Bot registered at the [QQ Bot Platform](https://q.qq.com/) with your AppID and AppSecret

## Setup

1. Register a QQ Bot at [QQ Bot Platform](https://q.qq.com/) and note your AppID and AppSecret.
2. Start your Stella server if it is not already running:

   ```bash
   stellad server
   ```

3. Open the Web UI at `http://localhost:25678`.
4. Go to the **Channels** page and add a new QQ channel instance.
5. Enter your AppID and AppSecret, then save.
6. Restart `stellad server` to activate the new channel.

All channel configuration (credentials, allowed IDs, etc.) is managed through the Web UI.

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

QQ group messages are received as @mention events. In group chats the bot participates automatically and responds when @mentioned. To stop a bot from participating in a group, remove it from that group.

## Access Control

You can restrict which QQ users can interact with your bot by adding allowed OpenIDs in the Web UI. Leave the list empty to allow all users. Use the `/whoami` command to find your OpenID.

## Image Support

You can send images to the bot for analysis. The bot downloads image attachments, saves them to your private assets directory, and passes them to the AI model as multimodal content alongside any caption text.

## File Support

You can send file attachments (non-image, non-video) to the bot. When you send a file:

1. The bot downloads the file from the QQ attachment URL
2. Saves it to your private assets directory on disk
3. A Xberg extraction hint is passed to the agent so it can read the file content

The agent can then use the `xberg extract` command to parse the file.

> **Note:** File uploads require the Xberg skill to be enabled for the active agent.

## Commands

Send these commands as text messages to the bot:

| Command             | Description                                               |
| ------------------- | --------------------------------------------------------- |
| `/start` or `/help` | Welcome and help                                          |
| `/new`              | Start a fresh session (previous history stays searchable) |
| `/compact`          | Compress the current session in place                     |
| `/abort`            | Cancel the in-progress response                           |
| `/model`            | List available models                                     |
| `/model <number>`   | Switch to model by number                                 |
| `/model <query>`    | Filter models by name                                     |
| `/whoami`           | Show your user ID for config                              |

Each agent in a group keeps its own session, so `/new` in a group with several agents needs a target: `/new @agent`. The command itself never becomes part of the group's history. See [Memory](/docs/guides/memory) for what a fresh session keeps.

## Configuration Reference

All settings below are managed through the Web UI.

| Field         | Description                        | Default    |
| ------------- | ---------------------------------- | ---------- |
| `app_id`      | QQ Bot AppID                       | (required) |
| `app_secret`  | QQ Bot AppSecret                   | (required) |
| `allowed_ids` | User OpenIDs allowed (empty = all) | `[]`       |

## Troubleshooting

**Bot not responding to messages?**

- Make sure `stellad server` is running and the QQ channel is configured in the Web UI.
- Verify your AppID and AppSecret are correct.
- If you set up access control, confirm your QQ OpenID is in the `allowed_ids` list. Send `/whoami` to the bot to check.

**Bot not responding in groups?**

- QQ group messages require an @mention to trigger the bot. Make sure the bot is a member of the group and that you @mention it.

**Files not being analyzed?**

- Make sure the Xberg skill is enabled for the active agent.

**Connection issues?**

- The bot uses a persistent WebSocket connection. If it drops, restart `stellad server`.
- Check your network connectivity and ensure the QQ Bot Platform credentials are valid.
