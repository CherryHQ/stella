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

All channel configuration (token, group access, dedicated agent binding, etc.) is managed through the Web UI.

## Multi-User Support

Each Telegram user is automatically identified from their platform identity. Sessions are scoped per user per agent, so different users keep separate conversation histories. No manual user setup is required.

## Streaming Responses

The bot streams LLM responses in real time using two strategies:

### Private Chats: Draft API (Bot API 9.3+)

Uses `sendMessageDraft` for smooth animated streaming without rate-limiting issues. If the API is not available, the bot automatically falls back to edit mode.

### Group Chats and Topics: Edit-in-Place

In groups, supergroups, and forum topics, Stella sends one progress message and edits it about once per second as text or tool status changes. The same message becomes the final response; long responses continue in additional messages. Stella also keeps Telegram's typing indicator active while it works. If a response stream fails, the progress message shows a safe failure notice instead of silently disappearing.

### Reaction Lifecycle

Stella reacts with 🤔 on your message once it starts working on it, and clears that reaction when the answer arrives. Only a failed turn leaves a mark, 👎. This works in private chats, groups, and forum topics.

Success deliberately adds no reaction: the reply is the signal, and every Telegram reaction plays an animation on a message you are already watching. Telegram also only allows reactions from a fixed emoji list, which is why failure is 👎 rather than the ❌ the Discord channel uses. Reactions are cosmetic: if the bot lacks permission to react, the reply itself is unaffected.

### Tool Indicators

While the assistant runs tools, you will see status indicators in the stream:

| Tool     | Indicator        |
| -------- | ---------------- |
| `bash`   | lightning        |
| `read`   | book             |
| `write`  | pencil           |
| `edit`   | wrench           |
| `search` | magnifying glass |

## Markdown Rendering

Responses are converted to Telegram MarkdownV2. If a message fails to send as markdown, Stella retries it as plain text so nothing is lost.

Telegram has no collapsible container, so a `<details>` section is flattened: the `<summary>` becomes a bold heading and the body follows as normal markdown. `<https://example.com>` style autolinks are rewritten as inline links; without that they would be dropped by the MarkdownV2 converter.

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

Group chats are off until you turn on **Allow group chats** in the Web UI. While off, every group message is rejected. Forum topics keep separate group context and history from other topics in the same supergroup.

Optionally set **Allowed chat IDs** to limit the bot to specific groups, and **Allowed topic IDs** to limit it further to specific forum topics. Topic entries use `chat_id:thread_id` (for example, `-1001234567890:42`). Empty lists preserve the broad behavior: every group the bot has joined is eligible when group chats are enabled. Once either list is configured, a non-matching group or topic is rejected before it reaches the agent.

Group messages must @mention the bot by default. Commands follow the same rule: use `/help@your_bot` (or reply to a bot message) in a group when mentions are required. You can turn off **Require a mention** to enable Stella's group collaboration; also disable privacy mode for the bot in BotFather so it can read ordinary messages.

## Access Control

**Allow direct messages** controls private chat and account linking. Linked Stella users keep their normal sessions and permissions.

Unlinked private senders are denied by default. Enable **Allow guest direct messages** only when you want them to use the channel-bound agent through persistent restricted guest sessions. Guest history persists and compacts, but guests have no profile, reflection, tools, skills, files, workspace, plugins, or delegation. They can use only `/link`, `/help`, `/new`, `/compact`, and `/abort`; attachments are rejected before download, and linking does not merge previous guest history.

Guest traffic is limited per sender, each channel has a durable guest cap, and inactive guest identities and sessions are removed after the configured retention period. Public guest access can still create model cost and abuse risk. Use a dedicated guest-safe agent whose base prompt contains no secrets.

## Notifications

The bot doubles as a notification backend. You can configure a default notification chat and optional broadcast channel in the Web UI.

Used by:

- The `notify` agent tool (in server mode)
- Scheduler job result broadcasting

## Commands

| Command             | Description                                                   |
| ------------------- | ------------------------------------------------------------- |
| `/start` or `/help` | Welcome and help                                              |
| `/new`              | Start a fresh session (previous history leaves memory search) |
| `/compact`          | Compress the current session in place                         |
| `/abort`            | Cancel the in-progress response                               |
| `/whoami`           | Show your user ID                                             |

`/new` works in a direct message only. A group's context is shared by everyone in it, so `/new` in a group replies that the shared session cannot be reset and changes nothing; the command itself never becomes part of the group's history. See [Memory](/docs/guides/memory) for what a fresh session keeps.

## Configuration Reference

All settings below are managed through the Web UI.

| Field                            | Description                                                   | Default    |
| -------------------------------- | ------------------------------------------------------------- | ---------- |
| `token`                          | Bot API token                                                 | (required) |
| `channel_id`                     | Default proactive notification target (@name or numeric ID)   |            |
| `allow_group`                    | Accept messages from groups the bot was added to              | `false`    |
| `allowed_chat_ids`               | Optional group or supergroup IDs allowed to use the bot       | empty      |
| `allowed_topic_ids`              | Optional `chat_id:thread_id` forum topics allowed to use it   | empty      |
| `allow_dm`                       | Accept private messages and account linking                   | `true`     |
| `allow_unlinked_dm`              | Allow restricted guest sessions for unlinked private senders  | `false`    |
| `guest_message_limit_per_minute` | Per-guest message and command limit                           | `10`       |
| `guest_max_per_channel`          | Maximum durable guest identities for this channel             | `1000`     |
| `guest_retention_days`           | Delete inactive guest identities and sessions after this time | `30`       |
| `require_mention`                | Require an @mention in group chats                            | `true`     |

When both allowlists are empty, `allow_group` keeps its backward-compatible behavior: every group the bot belongs to can reach the bound agent. Configure an allowlist when the bot is present in groups that should not have access. If both lists are set, a message must match both; a topic list never grants access to the same thread number in another chat.

## Troubleshooting

**Bot not responding to messages?**

- Make sure `stellad server` is running and the Telegram channel is configured in the Web UI.
- Double-check that your bot token is correct. You can verify it by messaging [@BotFather](https://t.me/BotFather) and checking your bot list.
- Confirm **Allow direct messages** is enabled if you are testing in a private chat.

**Bot not responding in groups?**

- @mention the bot for the most reliable trigger.
- Turn on **Allow group chats**. It is off by default and rejects every group message.
- If you expect replies without @mentions, make sure at least one group agent has a routing-capable model and that the message is a clear request, not casual chatter.
- Make sure the bot has been added to the group and has permission to read messages. For Telegram, disable bot privacy mode in BotFather if you want no-mention routing.

**Images or files not being analyzed?**

- Configure **Settings -> Vision** for ordinary-session baselines. A model declaring `text, image` receives active-turn pixels; group history without a stored baseline uses the unavailable marker.
- For file uploads, the Xberg skill must be enabled for the active agent.
