---
title: Feishu Bot
---

Stella includes a Feishu (Lark) bot that connects over WebSocket, so you do not need a public webhook URL. You can chat with your AI assistant in Feishu, send images and documents, and use it in group chats with threading support. Agent-created task, goal, and article references render as compact Feishu cards, with an "Open Web UI" button to jump to the item when `STELLA_BASE_URL` is set.

## Prerequisites

Before you start, make sure you have:

- A running Stella server (`stellad server`)
- At least one AI provider configured in the Web UI (e.g. Anthropic, OpenAI)
- The Feishu mobile app, signed in as a tenant user who can create internal apps

## Setup

1. Start your Stella server if it is not already running:

   ```bash
   stellad server
   ```

2. Open the Web UI at `http://localhost:25678`.
3. Go to **Channels** and add a new Feishu channel instance.
4. Enter a channel ID and name.
5. Select the agent this bot should represent.
6. Click **Scan to create Feishu bot**.
7. Scan the QR code with the Feishu mobile app and confirm the app creation request.
8. Stella saves the returned App ID and App Secret on the channel and starts the bot.

You can create multiple Feishu channel instances in the Web UI. Each instance gets its own Feishu PersonalAgent app and can optionally be bound to a dedicated agent.

### Manual setup (advanced)

If you already have a Feishu app, you can still enter credentials manually:

1. Create a Feishu app at [Feishu Open Platform](https://open.feishu.cn/).
2. Enable the **Bot** capability in your app settings.
3. Under **Event Subscriptions**, add:
   - `im.message.receive_v1`
   - `im.message.reaction.created_v1` (optional, for reaction events)
4. Copy your App ID, App Secret, Encrypt Key, and Verification Token.
5. Add a Feishu channel instance in the Web UI.
6. Select the agent this bot should represent.
7. Expand the manual fields, enter your credentials, and save.

## Auto-Provisioning

When a user signs in to Stella with Feishu OAuth, Stella links the Feishu channel identity immediately using the Feishu `union_id`. No `/link` command is needed.

When you enable auto-provisioning for a Feishu channel instance, Stella can also create an account for each employee the first time they message that bot. This is for users who have not signed in with Feishu OAuth yet.

### How it works

1. A user messages the bot.
2. Auto-provision runs only for messages that the bot actually handles. In groups, the most reliable first-contact trigger is an `@` mention. Non-mention messages may be handled when Stella's semantic group routing confidently selects this bot.
3. Stella determines the bot tenant key:
   - If you configured `tenant_key`, that value is used.
   - Otherwise Stella tries to auto-detect it at startup via the Feishu tenant API.
4. If the message event includes a `tenant_key` that does not match the bot tenant, Stella skips auto-provision for that sender.
5. Stella calls the Feishu Contact API to retrieve the user's `union_id`, display name, and email.
6. A new Stella user is created with the email local-part as username (`alice` from `alice@corp.com`), falling back to `feishu-<union_id[:8]>` if no email is available. Username collisions get a `-2`, `-3`, ... suffix.
7. The provisioned user has no password -- they can chat with the bot immediately but cannot log into the Web UI until an admin sets a password for them.
8. Provisioned users are assigned the `user` role and the system default agent.

Auto-provisioning is best-effort. If tenant detection or the Contact API lookup fails, the message still goes through the normal channel flow, but no Stella user is created. If a matching Feishu OAuth login identity already exists and was not linked during Web UI login, Stella links the channel identity from that login identity instead of creating a separate user.

### Required app scopes

Add these scopes to your Feishu app under **Permissions & Scopes**:

- `contact:user.base:readonly`
- `contact:user.id:readonly`

### Finding your tenant key

In the Feishu Admin Console, go to **Enterprise Information**. The tenant key is labeled **Tenant Key**.

Setting `tenant_key` explicitly is recommended because it removes one failure mode and makes auto-provisioning more predictable, though Stella can auto-detect it at startup.

### Configuration

```json
{
  "app_id": "FEISHU_APP_ID",
  "app_secret": "FEISHU_APP_SECRET",
  "tenant_key": "YOUR_TENANT_KEY",
  "auto_provision": true
}
```

> **Warning:** External guests in shared groups are not auto-provisioned. If their tenant key differs from the bot tenant, Stella skips account creation for them. This is by design.

> **Note:** If no admin user exists yet, auto-provisioning is refused until the first admin registers via the Web UI. This prevents stranding a fresh deployment with zero admins.

## Multi-User Support

Each Feishu user is identified from their platform identity automatically. Stella prefers Feishu `union_id` when the event payload includes it, and falls back to `open_id` for older links. Feishu OAuth login uses the same `union_id`, so Web UI login and Feishu channel chat resolve to the same Stella user automatically. This makes multi-instance Feishu setups work across multiple Feishu apps owned by the same developer account, because `union_id` is stable across those apps while `open_id` is app-scoped.

Existing older Feishu links stored as `open_id` are upgraded automatically the next time the user messages from the linked bot after upgrading Stella. If a user was linked only on an older bot, they can also re-run `/link` once from any Feishu app to refresh the link.

Sessions are scoped per user and per agent, so different users keep separate memory and default-agent state.

## Streaming Responses

The bot streams responses by editing messages in place:

1. Sends an initial placeholder quickly.
2. Updates the visible response while the model is generating.
3. Finishes with the complete response and elapsed time footer.

Tool activity from the assistant is summarized inline during streaming.

## Rich Card Rendering

Responses from the AI are rendered as [Feishu Card JSON 2.0](https://open.feishu.cn/document/uAjLw4CM/ukzMukzMukzM/feishu-cards/feishu-cards-v2/introduction-to-feishu-card-json-structure) messages, not plain text. This gives you native formatting inside Feishu.

### Supported markdown

Most standard markdown passes through to the card natively:

| Syntax                    | Example                    | Supported                |
| ------------------------- | -------------------------- | ------------------------ |
| Headings                  | `# H1` through `###### H6` | Yes                      |
| Bold / italic             | `**bold**` `*italic*`      | Yes                      |
| Strikethrough             | `~~text~~`                 | Yes                      |
| Inline code               | `` `code` ``               | Yes                      |
| Fenced code blocks        | ` ```go ... ``` `          | Yes                      |
| Links                     | `[text](url)`              | Yes                      |
| Blockquotes               | `> text`                   | Yes                      |
| Ordered / unordered lists | `1.` / `-`                 | Yes                      |
| Nested lists              | Indented sub-items         | Yes                      |
| Thematic break            | `---`                      | Yes                      |
| Task checkboxes           | `- [x]` / `- [ ]`          | Yes (rendered as ✅ / ☐) |
| Images                    | `![alt](img_key)`          | Partial (see below)      |

### Tables

GFM tables are rendered as native Feishu table components with full pagination — no row limit. Column alignment (`:--`, `:-:`, `--:`) is preserved. Up to 5 tables per card use native components; any additional tables fall back to code-block formatting.

### Interactive buttons

Agents can include clickable buttons in their responses using a double-curly-brace syntax. The format is:

<pre>{'{{'}button value="retry" type="primary" label="Retry"{'}}'}</pre>

| Attribute | Required | Description                                             |
| --------- | -------- | ------------------------------------------------------- |
| `value`   | Yes      | Callback identifier sent back when clicked              |
| `label`   | Yes      | Button display text                                     |
| `type`    | No       | Style: `default`, `primary`, `danger`                   |
| `confirm` | No       | Confirmation dialog text shown before the click is sent |

When you click a button, Stella forwards the action to the agent as a message (e.g. `[User clicked: retry]`), so the agent can decide what to do in context. A toast notification ("Processing...") appears immediately while the agent processes the action.

Consecutive buttons are grouped horizontally. A single button takes a full row.

### Known limitations

- **Images require a Feishu image key.** The `![alt](img_key)` syntax works only when the source is an `img_key` uploaded via the Feishu API. External URLs (e.g. `https://example.com/photo.png`) are silently ignored by Feishu. Image upload is not yet implemented.
- **HTML tags in markdown** (e.g. `<font>`, `<text_tag>`) are passed through but not validated. Use them only if you know the Feishu card renderer supports them.

## Supported Message Types

| Type               | Behavior                                                                                                            |
| ------------------ | ------------------------------------------------------------------------------------------------------------------- |
| Text               | Sent to the model as text                                                                                           |
| Image              | Downloaded, saved to your assets directory, and passed as multimodal input                                          |
| Post               | Rich text is parsed into text plus images; images are saved to your assets directory and passed as multimodal input |
| Audio              | Sent as descriptive text with duration                                                                              |
| Video              | Sent as descriptive text with duration                                                                              |
| File               | Downloaded and saved to disk; images pass as multimodal input, other files get an Xberg extraction hint             |
| Sticker            | Sent as descriptive text                                                                                            |
| Location           | Sent as descriptive text with coordinates when present                                                              |
| Shared chat/user   | Sent as descriptive text                                                                                            |
| Forwarded messages | Sent as a summary marker                                                                                            |

## Native Threading

When you message inside a Feishu thread, Stella keeps the response in that thread and scopes the session to the thread root. Replies outside threads stay in the parent chat session.

## Group Behavior

In group chats the bot participates automatically. @mentions always route to the mentioned bot; other clear group questions may also be routed by Stella's semantic group routing when an eligible routing model is available, otherwise they stay silent. To stop a bot from participating in a group, remove it from that group.

You can also set per-group overrides with the `groups` map in channel config.

## Commands

Feishu supports the standard chat commands:

| Command    | Description                                               |
| ---------- | --------------------------------------------------------- |
| `/new`     | Start a fresh session (previous history stays searchable) |
| `/compact` | Compress the current session in place                     |
| `/abort`   | Cancel the in-progress response                           |
| `/model`   | List or switch models                                     |
| `/agent`   | List or switch agents                                     |
| `/whoami`  | Show your platform identity                               |

`/new` works in a direct message only. A group's context is shared by everyone in it, so `/new` in a group replies that the shared session cannot be reset and changes nothing; the command itself never becomes part of the group's history. See [Memory](/docs/guides/memory) for what a fresh session keeps.

## Config Reference

```json
{
  "app_id": "FEISHU_APP_ID",
  "app_secret": "FEISHU_APP_SECRET",
  "encrypt_key": "",
  "verification_token": "",
  "enable_notify": false,
  "tenant_key": "",
  "auto_provision": false,
  "groups": {
    "oc_example": {
      "system_prompt": "Answer as the infra assistant for this group."
    }
  }
}
```

| Field                | Description                                                                                                          |
| -------------------- | -------------------------------------------------------------------------------------------------------------------- |
| `app_id`             | Feishu app ID                                                                                                        |
| `app_secret`         | Feishu app secret                                                                                                    |
| `encrypt_key`        | Optional event encryption key                                                                                        |
| `verification_token` | Optional event verification token                                                                                    |
| `enable_notify`      | Allow scheduler and notify output to target Feishu                                                                   |
| `tenant_key`         | Your enterprise tenant key. Optional: Stella can auto-detect it at startup, but setting it explicitly is recommended |
| `auto_provision`     | Automatically create Stella accounts for users handled by this Feishu channel instance                               |
| `groups`             | Optional per-chat overrides keyed by Feishu `chat_id`                                                                |

## Troubleshooting

**Bot not responding to messages?**

- Make sure `stellad server` is running and the Feishu channel is configured in the Web UI.
- Verify your App ID and App Secret are correct.
- Check that you added the `im.message.receive_v1` event subscription to your Feishu app.

**Bot not responding in groups?**

- @mention the bot for the most reliable trigger.
- If you expect replies without @mentions, make sure at least one group agent has a routing-capable model and that the message is a clear request, not casual chatter.

**Auto-provisioning not creating users?**

1. Make sure you enabled auto-provision on the correct Feishu channel instance -- it is configured per instance, not globally.
2. In groups, ask the user to @mention the bot for first contact. Non-mention messages can stay silent if semantic routing does not select the bot.
3. Verify that `tenant_key` is set or that startup auto-detection succeeded. If neither works, auto-provision is skipped.
4. Confirm your Feishu app has these scopes: `contact:user.base:readonly` and `contact:user.id:readonly`.
5. At least one Stella admin must already exist. Fresh deployments refuse auto-provision until the first admin account is created.
6. External guests are intentionally not auto-provisioned. Only internal tenant members qualify.
7. Restart `stellad server` after changing any configuration.

A reliable auto-provisioning configuration looks like:

```json
{
  "app_id": "FEISHU_APP_ID",
  "app_secret": "FEISHU_APP_SECRET",
  "tenant_key": "YOUR_TENANT_KEY",
  "auto_provision": true
}
```

Semantic routing may keep chatter silent, so users should @mention the bot for reliable first contact and auto-provisioning.

**Images or files not being analyzed?**

- Ensure you are using a vision-capable model for image analysis.
- For file uploads, the Xberg skill must be enabled for the active agent.

**Notifications not working?**

- Set `enable_notify` to `true` in your channel config.
- Make sure the bot has already had a conversation with the target user or group.
