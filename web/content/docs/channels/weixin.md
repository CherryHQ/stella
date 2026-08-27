---
title: WeChat Bot
---

Stella includes a WeChat bot that connects via the iLink Bot API using long-polling -- no public URL required. You can chat with your AI assistant in WeChat, send images and files for analysis, and receive notifications.

## Prerequisites

Before you start, make sure you have:

- A running Stella server (`stellad server`)
- At least one AI provider configured in the Web UI (e.g. Anthropic, OpenAI)
- A WeChat account to create the bot via QR code
- An enabled Stella agent to receive messages from the bot

## Setup

1. Start your Stella server if it is not already running:

   ```bash
   stellad server
   ```

2. Open the Web UI at `http://localhost:25678`.
3. Go to the **Channels** page and click **New Channel**.
4. Select **Weixin**, keep or edit the suggested name, and choose the agent that should receive messages from this bot. WeChat is singleton-only: the channel ID is always `weixin`.
5. Click **Scan to create WeChat bot**.
6. Open WeChat on your phone, scan the QR code, and confirm bot creation.
7. Once confirmed, Stella saves the returned iLink credentials and starts the channel.

All channel configuration is managed through the Web UI. WeChat is singleton-only in Stella because one iLink account does not support multiple independent bots. If you already have an iLink credential bundle, expand **I already have WeChat iLink credentials** and enter it manually instead.

## How It Works

The WeChat channel uses the iLink Bot protocol. After QR login, Stella receives a `bot_token` that it uses for all subsequent API calls. Messages are received via long-polling and responses are sent back through the same API.

### Session Expiry

If the iLink session expires, the WeChat channel stops responding. Create a fresh bot with the scan flow or update the channel with a valid manual credential bundle.

## Multi-User Support

Each WeChat user is automatically identified from their iLink user ID. Sessions are scoped per user per agent. No manual user setup is required.

## Messaging

The bot sends and receives text messages with a 2000 character limit. Messages exceeding this limit are automatically split at paragraph breaks, newlines, or spaces.

### Image Support

You can send images to the bot for analysis. Images are downloaded, decrypted, saved to your private assets directory, and passed to the AI model as multimodal content.

The bot can also send images back. Generated images are encrypted, uploaded, and delivered as image messages.

### File Support

You can send files to the bot. Files are downloaded, decrypted, and saved to your private assets directory; image files pass to the model as multimodal input, while other files get an Xberg extraction hint so the agent can use the `xberg extract` command to parse their content. Video messages are logged and skipped.

### Typing Indicators

While the assistant processes your message, you will see a typing indicator. The indicator refreshes every 5 seconds until the response is ready.

## Access Control

You can restrict which WeChat users can interact with the bot by adding their iLink user IDs in the Web UI's "Allowed IDs" field. Leave the list empty to allow all users. Use the `/whoami` command to find your user ID.

## Commands

Send these commands as text messages to the bot:

| Command             | Description                                                   |
| ------------------- | ------------------------------------------------------------- |
| `/start` or `/help` | Welcome and help                                              |
| `/new`              | Start a fresh session (previous history leaves memory search) |
| `/compact`          | Compress the current session in place                         |
| `/abort`            | Cancel the in-progress response                               |
| `/whoami`           | Show your user ID for config                                  |

`/new` works in a direct message only. A group's context is shared by everyone in it, so `/new` in a group replies that the shared session cannot be reset and changes nothing; the command itself never becomes part of the group's history. See [Memory](/docs/guides/memory) for what a fresh session keeps.

## Notifications

The WeChat channel supports notifications (scheduler results, notify tool). Set "Enable Notify" and configure "Notify Chat" with a user ID in the Web UI.

**Important limitation**: Notifications require a cached token which is in-memory only. After restarting `stellad server`, notifications to WeChat users will fail until they send a new message. This is a known limitation of the iLink protocol.

## Configuration Reference

All settings below are managed through the Web UI.

| Field         | Description                                               | Default    |
| ------------- | --------------------------------------------------------- | ---------- |
| `bot_token`   | iLink bot token (obtained via scan setup or manual setup) | (required) |
| `base_url`    | iLink API base URL                                        | (auto)     |
| `bot_id`      | iLink bot ID (obtained via scan setup)                    | (auto)     |
| `user_id`     | iLink user ID (obtained via scan setup)                   | (auto)     |
| `notify_chat` | Default user ID for notifications                         | `""`       |
| `allowed_ids` | User IDs allowed to interact (empty = all)                | `[]`       |

## Troubleshooting

**Bot not responding to messages?**

- Make sure `stellad server` is running and the WeChat channel is configured in the Web UI.
- The iLink credentials may have expired. Go to the Web UI and run the scan setup again.

**Session expired?**

- When the iLink session expires, run **Scan to create WeChat bot** again or enter a fresh manual credential bundle.

**Notifications not being delivered?**

- WeChat notifications require the recipient to have sent a message to the bot first (so Stella has a cached context token).
- After restarting `stellad server`, all users need to send at least one message before they can receive notifications again.

**Images or files not being analyzed?**

- Configure **Admin -> Models** for ordinary-session baselines. A model declaring `text, image` receives active-turn pixels; group history without a stored baseline uses the unavailable marker.
- For file analysis, the Xberg skill must be enabled for the active agent.

**Messages getting cut off?**

- WeChat has a 2000 character limit per message. Stella automatically splits longer responses, but very long tool outputs may be truncated.
