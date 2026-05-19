---
title: WeChat Bot
---

Stella includes a WeChat bot that connects via the iLink Bot API using long-polling -- no public URL required. You can chat with your AI assistant in WeChat, send images and files for analysis, and receive notifications.

## Prerequisites

Before you start, make sure you have:

- A running Stella server (`stella server`)
- At least one AI provider configured in the Web UI (e.g. Anthropic, OpenAI)
- A WeChat account to authorize the bot via QR code login

## Setup

1. Start your Stella server if it is not already running:

   ```bash
   stella server
   ```

2. Open the Web UI at `http://localhost:25678`.
3. Go to the **Channels** page and find the WeChat section.
4. Click **Scan QR to Login** to generate a QR code.
5. Open WeChat on your phone and scan the QR code to authorize the bot.
6. Once confirmed, your credentials are saved automatically.
7. Restart `stella server` to activate the channel.

All channel configuration is managed through the Web UI. The QR login flow is only available through the Web UI and must be re-done if the session expires.

## How It Works

The WeChat channel uses the iLink Bot protocol. After QR login, Stella receives a `bot_token` that it uses for all subsequent API calls. Messages are received via long-polling and responses are sent back through the same API.

### Session Expiry

If the iLink session expires, Stella clears all credentials and stops the WeChat channel. You will need to re-scan the QR code from the Web UI to re-authorize.

## Multi-User Support

Each WeChat user is automatically identified from their iLink user ID. Sessions are scoped per user per agent. No manual user setup is required.

## Messaging

The bot sends and receives text messages with a 2000 character limit. Messages exceeding this limit are automatically split at paragraph breaks, newlines, or spaces.

### Image Support

You can send images to the bot for analysis. Images are downloaded, decrypted, and passed to the AI model as multimodal content.

The bot can also send images back. Generated images are encrypted, uploaded, and delivered as image messages.

### File Support

You can send files to the bot. Files are downloaded, decrypted, saved to your private assets directory, and passed to the agent with a kreuzberg extraction hint. The agent can then use the `kreuzberg extract` command to parse the file content. Video messages are logged and skipped.

### Typing Indicators

While the assistant processes your message, you will see a typing indicator. The indicator refreshes every 5 seconds until the response is ready.

## Access Control

You can restrict which WeChat users can interact with the bot by adding their iLink user IDs in the Web UI's "Allowed IDs" field. Leave the list empty to allow all users. Use the `/whoami` command to find your user ID.

## Commands

Send these commands as text messages to the bot:

| Command             | Description                     |
| ------------------- | ------------------------------- |
| `/start` or `/help` | Welcome and help                |
| `/new`              | Compact conversation context    |
| `/compact`          | Compress conversation history   |
| `/abort`            | Cancel the in-progress response |
| `/model`            | List available models           |
| `/model <p/m>`      | Switch to model by name         |
| `/model <query>`    | Filter models by name           |
| `/agent`            | List or switch agents           |
| `/whoami`           | Show your user ID for config    |

## Notifications

The WeChat channel supports notifications (scheduler results, notify tool). Set "Enable Notify" and configure "Notify Chat" with a user ID in the Web UI.

**Important limitation**: Notifications require a cached token which is in-memory only. After restarting `stella server`, notifications to WeChat users will fail until they send a new message. This is a known limitation of the iLink protocol.

## Configuration Reference

All settings below are managed through the Web UI.

| Field         | Description                                | Default    |
| ------------- | ------------------------------------------ | ---------- |
| `bot_token`   | iLink bot token (obtained via QR login)    | (required) |
| `base_url`    | iLink API base URL                         | (auto)     |
| `bot_id`      | iLink bot ID (obtained via QR login)       | (auto)     |
| `user_id`     | iLink user ID (obtained via QR login)      | (auto)     |
| `notify_chat` | Default user ID for notifications          | `""`       |
| `allowed_ids` | User IDs allowed to interact (empty = all) | `[]`       |

## Troubleshooting

**Bot not responding to messages?**

- Make sure `stella server` is running and the WeChat channel is configured in the Web UI.
- Your session may have expired. Go to the Web UI and re-scan the QR code to re-authorize.

**Session expired?**

- When the iLink session expires, Stella automatically stops the WeChat channel. Go to the Web UI, click **Scan QR to Login** again, and scan with WeChat.

**Notifications not being delivered?**

- WeChat notifications require the recipient to have sent a message to the bot first (so Stella has a cached context token).
- After restarting `stella server`, all users need to send at least one message before they can receive notifications again.

**Images or files not being analyzed?**

- Ensure you are using a vision-capable model for image analysis.
- For file analysis, the kreuzberg skill must be enabled for the active agent.

**Messages getting cut off?**

- WeChat has a 2000 character limit per message. Stella automatically splits longer responses, but very long tool outputs may be truncated.
