---
title: Feishu Bot
---

anna includes a Feishu (Lark) bot that connects via WebSocket (persistent connection, no public URL required). The bot supports 11 Feishu workspace tools, user OAuth, streaming responses, native threading, and per-group configuration.

## Setup

1. Create a Feishu app at [Feishu Open Platform](https://open.feishu.cn/)
2. Enable the **Bot** capability in your app settings
3. Under **Event Subscriptions**, add these events:
   - `im.message.receive_v1` (required) -- receive messages
   - `im.message.reaction.created_v1` (optional) -- receive reactions
4. Under **Permissions & Scopes**, add the scopes listed in the [Required Permissions](#required-permissions) section below
5. Get your App ID, App Secret, Encrypt Key, and Verification Token from the app settings
6. Run `anna --open` to launch the admin panel
7. In the admin panel: add an AI provider, then configure the Feishu channel with your app credentials
8. Start the daemon:

```bash
anna
```

All channel configuration (credentials, group mode, allowed IDs, etc.) is managed through the admin panel. Environment variables are limited to provider API keys (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`) and `ANNA_HOME`.

## User OAuth (UAT)

Many Feishu tools (calendar, tasks, etc.) can operate on behalf of individual users rather than the bot. This requires each user to authorize the app via OAuth.

### How It Works

1. **User sends `/auth`** in a chat with the bot
2. **Bot replies with an interactive card** containing an authorization link
3. **User clicks the link**, which opens the Feishu OAuth page in their browser
4. **User approves** the permission request on the OAuth page
5. **Feishu redirects to the callback page** at `anna.vaayne.com/oauth/callback` which displays the authorization code and a "Copy Command" button
6. **User copies and sends `/auth <code>`** back to the bot
7. **Bot exchanges the code** for access and refresh tokens, stores them encrypted in the database
8. **Done** -- the user's identity is now available for tool calls

### Token Lifecycle

- **Access tokens** expire after ~2 hours. The bot auto-refreshes them transparently.
- **Refresh tokens** expire after ~30 days. After that, the user must re-authorize with `/auth`.
- **Storage**: tokens are encrypted at rest using AES-256-GCM. The encryption key is derived from the app secret via HKDF.
- **Fallback**: if a user hasn't authorized, tools fall back to the bot token (with reduced permissions).

### Feishu App OAuth Configuration

For the `/auth` flow to work, configure your Feishu app:

1. Go to **Security Settings** in the Feishu Open Platform console
2. Add `https://anna.vaayne.com/oauth/callback` as a **Redirect URL**
3. Under **Permissions & Scopes**, request the user-level scopes needed by the tools you want to use (see [Required Permissions](#required-permissions))

If you're self-hosting the docs site or want a different callback URL, set `redirect_uri` in the Feishu channel config and register that URL in your Feishu app instead.

### Revoking Authorization

Users can revoke their authorization at any time from their Feishu account settings under **Connected Apps**. The bot will detect expired tokens and prompt re-authorization when needed.

## Feishu Workspace Tools

When a Feishu channel is configured, anna registers 11 multi-action tools that the LLM agent can call. These tools are available to all agents regardless of which channel the user is chatting from.

| Tool | Actions | Description |
|------|---------|-------------|
| `feishu_user` | get_user, search_user | Look up users by open_id, email, or mobile |
| `feishu_calendar` | create/list/get/update/delete events, add_attendees, freebusy | Full calendar management with recurring event support |
| `feishu_task` | create/list/get/update/complete tasks, tasklists, subtasks | Task management with Task v2 API |
| `feishu_bitable` | app/table/record/field CRUD, batch ops, search/filter | Database operations with Bitable |
| `feishu_chat` | search, info, add/remove/list members | Chat and group management |
| `feishu_im` | send/reply/read/get/forward messages, reactions | Messaging and reactions |
| `feishu_doc` | create doc, get content/raw content | Document creation and reading |
| `feishu_wiki` | spaces, nodes CRUD, move/copy | Knowledge base management |
| `feishu_sheets` | create/get spreadsheet, list sheets, read/write ranges | Spreadsheet operations |
| `feishu_drive` | list/copy/move/delete files, create folder, get meta | File and folder management |
| `feishu_search` | search docs/wiki | Global document search with filters |

Tools use the user's OAuth token when available (for user-scoped operations like creating events on their calendar) and fall back to the bot token otherwise.

## Multi-User Support

Each Feishu user is automatically resolved from their platform identity. Sessions are scoped per user per agent. No manual user setup is required.

## Streaming Responses

The bot uses Feishu's CardKit 2.0 API for streaming responses with three phases:

1. **Thinking** -- an initial "Thinking..." card is sent immediately
2. **Generating** -- the card is progressively updated with content as the LLM generates tokens
3. **Complete** -- the final card includes a response time footer (e.g., _Response time: 3.2s_)

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
| Audio            | Descriptive text with duration sent to LLM           |
| Video            | Descriptive text with duration sent to LLM           |
| File             | File name and metadata sent to LLM                   |
| Sticker          | Descriptive text sent to LLM                         |
| Location         | Name and coordinates sent to LLM                     |
| Shared chat/user | Chat or user ID sent to LLM                          |
| Forwarded msgs   | Summary description sent to LLM                      |

## Thread Support

The bot supports native Feishu threading. When a user sends a message in a thread, the bot:

- Creates a **separate agent session** for that thread (isolated from the group conversation)
- Replies within the same thread
- Maintains thread-specific conversation history

This allows multiple parallel conversations in the same group chat without interference.

## Abort / Cancel

To cancel an active streaming response, send one of these messages:

- `cancel`, `stop`, `abort` (English, case-insensitive)
- `取消`, `停止` (Chinese)

The bot immediately cancels the active stream and replies with "Cancelled."

## Group Support

On startup, the bot fetches its own `open_id` via the Feishu Bot Info API. This enables reliable @mention detection in groups and prevents the bot from responding to its own messages (infinite loop protection).

In group chats, the bot responds to @mentions. Set the group mode in the admin panel:

- `mention` -- respond to @mentions (default)
- `always` -- respond to all group messages
- `disabled` -- ignore group messages entirely

### Per-Group Configuration

You can override settings for specific group chats by adding a `groups` map to the Feishu channel config JSON. Each key is a Feishu chat_id (e.g., `oc_abc123`):

```json
{
  "app_id": "cli_xxx",
  "app_secret": "xxx",
  "groups": {
    "oc_abc123": {
      "group_mode": "always",
      "system_prompt": "You are a project management assistant for Team Alpha."
    },
    "oc_def456": {
      "group_mode": "mention",
      "system_prompt": "You are a technical support bot. Answer concisely."
    }
  }
}
```

| Field | Description |
|-------|-------------|
| `group_mode` | Override the global group mode for this chat |
| `system_prompt` | Prepend a system prompt to every message in this chat |
| `tool_allow` | Reserved for future use: allowlist of tool names |
| `tool_deny` | Reserved for future use: denylist of tool names |

## Reactions

When a user reacts to a message with an emoji, the bot sends a description of the reaction to the agent (e.g., `[User reacted with THUMBSUP on message om_xxx]`). The agent can then respond if appropriate.

The LLM can also add or remove reactions via the `feishu_im` tool's `add_reaction` and `remove_reaction` actions.

## Access Control

Restrict which users can interact with the bot by adding allowed open_ids in the admin panel. Leave empty to allow all users. Use the `/whoami` command to get your open_id.

## Notifications

Configure a default notification chat in the admin panel for proactive notifications (scheduler results, agent-triggered alerts).

## Commands

Send these commands as text messages to the bot:

| Command             | Description                                     |
| ------------------- | ----------------------------------------------- |
| `/start` or `/help` | Welcome and help                                |
| `/new`              | Start a fresh session                           |
| `/compact`          | Compress conversation history                   |
| `/model`            | List available models                           |
| `/model <number>`   | Switch to model by number                       |
| `/model <query>`    | Filter models by name                           |
| `/auth`             | Start OAuth authorization (get auth card)       |
| `/auth <code>`      | Complete OAuth with authorization code          |
| `/whoami`           | Show your user ID for config                    |

## Required Permissions

### Bot Permissions (always needed)

| Scope | Description |
|-------|-------------|
| `im:message` | Send and receive messages |
| `im:message:send_as_bot` | Send messages as bot |
| `im:resource` | Access message resources (images, files) |
| `im:chat` | Access chat info |
| `contact:user.base:readonly` | Read basic user info |

### User Permissions (for OAuth tools)

Add these based on which tools you want to enable:

| Tool | Scopes |
|------|--------|
| Calendar | `calendar:calendar`, `calendar:calendar:readonly` |
| Tasks | `task:task`, `task:task:readonly` |
| Bitable | `bitable:app`, `bitable:app:readonly` |
| Docs | `docx:document`, `docx:document:readonly` |
| Wiki | `wiki:wiki`, `wiki:wiki:readonly` |
| Sheets | `sheets:spreadsheet`, `sheets:spreadsheet:readonly` |
| Drive | `drive:drive`, `drive:drive:readonly` |

## Configuration Reference

All settings below are managed through the admin panel (`anna --open`).

| Field                | Description                                        | Default    |
| -------------------- | -------------------------------------------------- | ---------- |
| `app_id`             | Feishu App ID                                      | (required) |
| `app_secret`         | Feishu App Secret                                  | (required) |
| `encrypt_key`        | Event encrypt key (from Events & Callbacks)        | (optional) |
| `verification_token` | Event verification token (from Events & Callbacks) | (optional) |
| `notify_chat`        | Chat ID for proactive notifications                | (optional) |
| `group_mode`         | Group behavior: `mention`, `always`, `disabled`    | `mention`  |
| `allowed_ids`        | User open_ids allowed (empty = all)                | `[]`       |
| `groups`             | Per-group config overrides (see above)             | `{}`       |
| `redirect_uri`       | OAuth redirect URI                                 | `https://anna.vaayne.com/oauth/callback` |
