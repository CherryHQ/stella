---
title: Telegram Bot
---

Stella includes a Telegram bot that connects via long polling -- no webhook or public IP needed. You can chat with your AI assistant directly in Telegram, send images and documents for analysis, and use it in group chats.

## Prerequisites

Before you start, make sure you have:

- A running Stella server (`stellad server`)
- At least one AI provider configured in the Web UI (e.g. Anthropic, OpenAI)
- A Telegram account

## Setup

1. Open Telegram and message [@BotFather](https://t.me/BotFather) to create a new bot. Save the bot token it gives you.
2. Start your Stella server if it is not already running:

   ```bash
   stellad server
   ```

3. Open the Web UI at `http://localhost:25678`.
4. Go to the **Channels** page and add a new Telegram channel instance.
5. Paste your bot token into the configuration and save.
6. Restart `stellad server` to activate the new channel.

You can create multiple Telegram channel instances if you have multiple bots. Each instance can optionally be bound to a dedicated agent in the Web UI.

All channel configuration (token, allowed IDs, dedicated agent binding, etc.) is managed through the Web UI.

## Multi-User Support

Each Telegram user is automatically identified from their platform identity. Sessions are scoped per user per agent, so different users keep separate conversation histories. No manual user setup is required.

## Agent Switching

You can switch between available agents using the `/agent` command:

- `/agent` -- list all available agents
- `/agent <name>` -- switch to a specific agent

In DMs, this sets your default agent. In groups, it sets the active agent for the entire group.

If a channel instance is bound to a dedicated agent in the Web UI, all chats on that bot use the bound agent and `/agent` switching is disabled.

## Streaming Responses

The bot streams LLM responses in real time using two strategies:

### Private Chats: Draft API (Bot API 9.3+)

Uses `sendMessageDraft` for smooth animated streaming without rate-limiting issues. If the API is not available, the bot automatically falls back to edit mode.

### Group Chats: Edit-in-Place

Sends an initial message and edits it periodically (every ~1 second) as tokens arrive. The streaming message is deleted once complete, then the final response is sent.

### Tool Indicators

While the assistant runs tools, you will see status indicators in the stream:

| Tool     | Indicator        |
| -------- | ---------------- |
| `bash`   | lightning        |
| `read`   | book             |
| `write`  | pencil           |
| `edit`   | wrench           |
| `search` | magnifying glass |

## Image Support

You can send photos to the bot in private chats. When you send an image:

1. The bot downloads the highest-resolution version from Telegram
2. Saves it to your private assets directory so the agent can revisit it later (crop, OCR, re-send)
3. Sends it as a multimodal message (saved path + image + optional caption) to the model
4. The model analyzes the image and responds with text

Use cases: describe screenshots, analyze diagrams, read documents from photos, etc.

If the model returns images (e.g. from tool results), they are sent back as Telegram photos after the text response.

> **Note:** Configure **Settings -> Vision** for ordinary-session baselines. A model whose **Input** declares `image` receives pixels only during the active turn; group history without a stored baseline uses the unavailable marker.

## File/Document Support

You can send documents (PDF, DOCX, XLSX, and other file types) to the bot. When you send a document:

1. The bot saves the file to your private assets directory on disk
2. Image files are passed to the model directly as multimodal input; other files get an Xberg extraction hint so the agent can read their content
3. Any caption you attached to the document is included as your text message

For non-image files, the agent can then use the `xberg extract` command to parse the file.

> **Note:** File uploads require a vision/document-capable model and the Xberg skill to be enabled for the active agent.

## Group Support

The bot supports explicitly allowed group chats. Add each numeric Telegram group or supergroup ID to **Allowed group chat IDs** in the Web UI. An empty list rejects every group message.

Group messages must @mention the bot by default. You can turn off **Require a mention** to enable Stella's semantic group routing; also disable privacy mode for the bot in BotFather so it can read ordinary messages. Every member of an allowed group can address the bound agent, so add only trusted groups.

## Access Control

**Allow direct messages** controls private chat and account linking. Linked Stella users keep their normal sessions and permissions.

Unlinked private senders are denied by default. Enable **Allow guest direct messages** only when you want them to use the channel-bound agent through persistent restricted guest sessions. Guest history persists and compacts, but guests have no profile, reflection, tools, skills, files, workspace, plugins, or delegation. They can use only `/link`, `/help`, `/new`, `/compact`, and `/abort`; attachments are rejected before download, and linking does not merge previous guest history.

Guest traffic is limited per sender, each channel has a durable guest cap, and inactive guest identities and sessions are removed after the configured retention period. Public guest access can still create model cost and abuse risk. Use a dedicated guest-safe agent whose base prompt contains no secrets.

## Notifications

The bot doubles as a notification backend. You can configure a default notification chat and optional broadcast channel in the Web UI.

Used by:

- The `notify` agent tool (in server mode)
- Scheduler job result broadcasting

## Model Switching

You can switch models mid-conversation using the `/model` command, which opens an inline keyboard. The model list is paginated with text filtering support.

## Commands

| Command             | Description                                               |
| ------------------- | --------------------------------------------------------- |
| `/start` or `/help` | Welcome and help                                          |
| `/new`              | Start a fresh session (previous history stays searchable) |
| `/compact`          | Compress the current session in place                     |
| `/abort`            | Cancel the in-progress response                           |
| `/model`            | List available models                                     |
| `/model <number>`   | Switch to model by number                                 |
| `/model <query>`    | Filter models by name                                     |
| `/agent`            | List available agents                                     |
| `/agent <name>`     | Switch active agent (user default in DM, group in chat)   |
| `/whoami`           | Show your user ID                                         |

`/new` works in a direct message only. A group's context is shared by everyone in it, so `/new` in a group replies that the shared session cannot be reset and changes nothing; the command itself never becomes part of the group's history. See [Memory](/docs/guides/memory) for what a fresh session keeps.

## Configuration Reference

All settings below are managed through the Web UI.

| Field                            | Description                                                   | Default    |
| -------------------------------- | ------------------------------------------------------------- | ---------- |
| `token`                          | Bot API token                                                 | (required) |
| `channel_id`                     | Default proactive notification target (@name or numeric ID)   |            |
| `allowed_chat_ids`               | Comma-separated numeric group IDs; empty rejects all groups   |            |
| `allow_dm`                       | Accept private messages and account linking                   | `true`     |
| `allow_unlinked_dm`              | Allow restricted guest sessions for unlinked private senders  | `false`    |
| `guest_message_limit_per_minute` | Per-guest message and command limit                           | `10`       |
| `guest_max_per_channel`          | Maximum durable guest identities for this channel             | `1000`     |
| `guest_retention_days`           | Delete inactive guest identities and sessions after this time | `30`       |
| `require_mention`                | Require an @mention in allowed groups                         | `true`     |

When upgrading, Stella adds groups already present in durable group membership to `allowed_chat_ids` once. Explicit allowlists, including an empty deny-all value, are not changed. Review the generated list after upgrading; newly encountered groups remain blocked until you add them.

## Troubleshooting

**Bot not responding to messages?**

- Make sure `stellad server` is running and the Telegram channel is configured in the Web UI.
- Double-check that your bot token is correct. You can verify it by messaging [@BotFather](https://t.me/BotFather) and checking your bot list.
- Confirm **Allow direct messages** is enabled if you are testing in a private chat.

**Bot not responding in groups?**

- @mention the bot for the most reliable trigger.
- Add the group's numeric chat ID to **Allowed group chat IDs**. The allowlist is fail-closed.
- If you expect replies without @mentions, make sure at least one group agent has a routing-capable model and that the message is a clear request, not casual chatter.
- Make sure the bot has been added to the group and has permission to read messages. For Telegram, disable bot privacy mode in BotFather if you want no-mention routing.

**Images or files not being analyzed?**

- Configure **Settings -> Vision** for ordinary-session baselines. A model declaring `text, image` receives active-turn pixels; group history without a stored baseline uses the unavailable marker.
- For file uploads, the Xberg skill must be enabled for the active agent.
