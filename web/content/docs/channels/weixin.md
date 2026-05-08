---
title: WeChat Bot
---

anna includes a WeChat bot that connects via the iLink Bot API using long-polling (no public URL required). The bot supports text messaging, image input/output with AES-128-ECB encryption, and QR-code-based login.

## Setup

1. Run `anna --open` to launch the admin panel
2. In the admin panel, go to the Channels tab and find the WeChat section
3. Click "Scan QR to Login" to generate a QR code
4. Scan the QR code with your WeChat account to authorize the bot
5. Once confirmed, credentials are saved automatically
6. Start the daemon:

```bash
anna
```

All channel configuration is managed through the admin panel. The QR login flow is admin-panel-only and must be re-done if the session expires.

## How It Works

The WeChat channel uses the iLink Bot protocol. After QR login, anna receives a `bot_token` that it uses for all subsequent API calls. Messages are received via long-polling (`getupdates`) and responses are sent via `sendmessage`.

### Session Expiry

If the iLink session expires (`ret=-14`), anna clears all credentials and stops the WeChat channel. You will need to re-scan the QR code from the admin panel to re-authorize.

## Multi-User Support

Each WeChat user is automatically resolved from their iLink user ID. Sessions are scoped per user per agent. No manual user setup is required.

## Messaging

The bot sends and receives text messages with a 2000 character limit. Messages exceeding this limit are automatically split at paragraph breaks, newlines, or spaces.

### Image Support

Users can send images to the bot for analysis. Images are downloaded from CDN, decrypted using AES-128-ECB, and passed to the AI model as multimodal content.

The bot can also send images back. Generated images are encrypted, uploaded to the WeChat CDN, and delivered as image messages.

File messages are downloaded from CDN, decrypted using AES-128-ECB, saved to the user's private assets directory, and passed to the agent with a kreuzberg extraction hint. The agent can then use the `kreuzberg extract` command to parse the file content. Video messages are logged and skipped.

### Typing Indicators

While the agent processes a message, a typing indicator is shown to the user. The indicator refreshes every 5 seconds until the response is ready.

## Access Control

Restrict which WeChat users can interact with the bot by adding their iLink user IDs in the admin panel's "Allowed IDs" field. Leave empty to allow all users. Use the `/whoami` command to get your user ID.

## Commands

Send these commands as text messages to the bot:

| Command             | Description                     |
| ------------------- | ------------------------------- |
| `/start` or `/help` | Welcome and help                |
| `/new`              | Start a fresh session           |
| `/compact`          | Compress conversation history   |
| `/abort`            | Cancel the in-progress response |
| `/model`            | List available models           |
| `/model <p/m>`      | Switch to model by name         |
| `/model <query>`    | Filter models by name           |
| `/agent`            | List or switch agents           |
| `/whoami`           | Show your user ID for config    |

## Notifications

The WeChat channel supports notifications (scheduler results, notify tool). Set "Enable Notify" and configure "Notify Chat" with a user ID in the admin panel.

**Important limitation**: Notifications require a cached `context_token` which is in-memory only. After a restart, notifications to WeChat users will fail until they send a new message. This is a known limitation of the iLink protocol.

## Configuration Reference

All settings below are managed through the admin panel (`anna --open`).

| Field         | Description                                | Default    |
| ------------- | ------------------------------------------ | ---------- |
| `bot_token`   | iLink bot token (obtained via QR login)    | (required) |
| `base_url`    | iLink API base URL                         | (auto)     |
| `bot_id`      | iLink bot ID (obtained via QR login)       | (auto)     |
| `user_id`     | iLink user ID (obtained via QR login)      | (auto)     |
| `notify_chat` | Default user ID for notifications          | `""`       |
| `allowed_ids` | User IDs allowed to interact (empty = all) | `[]`       |
