---
title: Feishu Bot
---

Stella includes a Feishu (Lark) bot that connects over WebSocket, so you do not need a public webhook URL. You can chat with your AI assistant in Feishu, send images, documents, voice, and video, and use it in group chats with threading support. Agent-created task, goal, and article references render as compact Feishu cards, with an "Open Web UI" button to jump to the item when `STELLA_BASE_URL` is set.

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
4. Keep or edit the suggested name. Stella generates the channel ID for you.
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

Feishu OAuth and Feishu chat use the same canonical Feishu `union_id`. Configure the built-in OAuth provider as `feishu`; a Feishu OAuth login then links the channel identity to that user. Auto-provisioning creates that same pair of identities, so a later OAuth login reuses the account instead of creating another one. `/link` remains available for manual linking and never triggers auto-provisioning.

Enable auto-provisioning per Feishu channel only when you want verified members of that bot's tenant to be admitted on first contact.

### Admission contract

Stella creates or completes an account only when all of these are true:

1. `auto_provision` is enabled for the channel and the event is a normal message, not a reaction or `/link` event.
2. The bot has an effective tenant key: the configured `tenant_key`, or a key successfully discovered at startup from the Feishu tenant API.
3. The message event itself carries a non-empty `tenant_key` exactly matching that effective key. Contact API access alone is not tenant-membership proof.
4. Stella can read the sender through the Feishu Contact API and the profile contains a non-empty canonical `union_id`.
5. At least one active Stella administrator already exists.

In a group, auto-provisioning requires an explicit `@` mention of this bot; semantic routing of an unmentioned message does not enroll its sender. External guests, missing or mismatched event tenant keys, reaction events, Contact API failures, and profiles without a `union_id` create no account. Provisioning failures do not stop the normal message flow.

The Contact API supplies the canonical `union_id`, display name, and email. Stella never persists the event `union_id` as new identity evidence. It uses `union_id`, not email, to converge Feishu OAuth and channel identities; an email already owned by a different user is rejected rather than silently merged.

### Account and access behavior

A successful first enrollment atomically creates one active Stella user, a Feishu login identity, and a Feishu channel identity. The new account always has the `user` role; auto-provisioning can neither create nor promote an administrator.

If Feishu omits an email address, Stella stores a stable synthetic internal address derived from the canonical `union_id` and tenant key (for example, `union_id@tenant_key.feishu.local`). It is an account identifier, not a deliverable mailbox. The same normalization is used by Feishu OAuth, so the two entry points converge.

Auto-provisioning does not assign an agent or set a default agent. The user's access to agents continues through Stella's existing authorization and channel-routing rules; a channel bound to a dedicated agent remains subject to that channel's routing policy.

An inactive user is never reactivated by a message. Existing inactive Feishu channel or OAuth identities are denied channel access. To offboard someone, an administrator must deactivate their Stella user; this feature does not synchronize departures from the Feishu directory or remove accounts automatically.

### Required app scopes

Add these scopes to your Feishu app under **Permissions & Scopes** so Stella can read the Contact API profile:

- `contact:user.base:readonly`
- `contact:user.id:readonly`
- `contact:user.email:readonly` (to use the member's actual email; otherwise Stella uses the synthetic address)

### Finding your tenant key

In the Feishu Admin Console, go to **Enterprise Information**. The tenant key is labeled **Tenant Key**.

Setting `tenant_key` explicitly is recommended because it removes a startup discovery failure mode. Auto-provisioning is disabled for admission if Stella has no effective tenant key.

### Configuration

```json
{
  "app_id": "FEISHU_APP_ID",
  "app_secret": "FEISHU_APP_SECRET",
  "tenant_key": "YOUR_TENANT_KEY",
  "auto_provision": true
}
```

> **Warning:** An event must carry the same tenant key as this bot. A shared-group guest or any event without that evidence is not auto-provisioned.

> **Note:** Create an active administrator before enabling auto-provisioning. The feature cannot bootstrap the first admin.

## Multi-User Support

Each Feishu user is identified from their platform identity automatically. Stella prefers Feishu `union_id` when the event payload includes it, and falls back to `open_id` for older links. Feishu OAuth login uses the same `union_id`, so Web UI login and Feishu channel chat resolve to the same Stella user automatically. This makes multi-instance Feishu setups work across multiple Feishu apps owned by the same developer account, because `union_id` is stable across those apps while `open_id` is app-scoped.

Existing older Feishu links stored as `open_id` are upgraded automatically the next time the user messages from the linked bot after upgrading Stella. If a user was linked only on an older bot, they can also re-run `/link` once from any Feishu app to refresh the link.

Sessions are scoped per user and per agent, so different users keep separate memory and default-agent state.

## Media and forwarded messages

Incoming images, files, voice messages, and videos are saved to the user's agent workspace. Images are also available to vision-capable models. When you forward a merged-message card, Stella fetches its direct child messages and applies the same media handling to their images and attachments, rather than leaving the agent with a placeholder.

When an agent returns a file with a recognised audio or video extension, Stella sends a native Feishu audio or media message. Other files remain ordinary file attachments.

## Streaming Responses

The bot streams responses by editing messages in place:

1. Sends an initial placeholder quickly.
2. Updates the visible response while the model is generating.
3. Finishes with the complete response and elapsed time footer.

Tool activity from the assistant is summarized inline during streaming.

### Cancellation and delivery

To stop an in-progress response, send `/abort`.

Stella retries transient card reply and update failures up to three times. In a private chat, exhausted retries make a best effort to replace the progress card with a delivery-failure notice. In a group, Stella keeps retrying through its existing delivery queue and shows that terminal notice only after the final queue attempt fails. Group delivery is at-least-once, not exactly-once: a retry after an uncertain Feishu API result can produce a duplicate response.

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

### Collapsible sections

An HTML `<details>` block becomes a native Feishu collapsible panel. `<summary>` supplies the header (default: 详情), and the `open` attribute starts the panel expanded. The body is rendered as markdown, so tables, code blocks, and lists work inside it.

```text
<details open>
<summary>Show the details</summary>

Any markdown works here.

</details>
```

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
| Forwarded messages | Expanded into the forwarded child messages, including text and supported message summaries                          |

## Native Threading

When you message inside a Feishu thread, Stella keeps the response in that thread and scopes the group conversation to the thread root. Replies outside threads stay in the parent chat session. Each thread is initialized independently when its first message arrives, so it does not silently fall back to its parent chat's context.

## Group Behavior

Group chats are off until you turn on **Allow group chats** in the Web UI. Once on, every group the bot has been added to can use it; while off, every group message is rejected and no group membership is provisioned.

Group messages must @mention the bot by default. You can turn off **Require a mention** to enable Stella's group collaboration. Every member of a group the bot joined can address the bound agent, so control access through who can add the bot to a group.

You can also set per-group overrides with the `groups` map in channel config.

## Access Control

**Allow direct messages** controls private chat, account linking, and auto-provisioning from private messages. Linked Stella users keep their normal sessions and permissions.

Unlinked private senders are denied by default. Enable **Allow guest direct messages** only when you want users who are not auto-provisioned or linked to use the channel-bound agent through persistent restricted guest sessions. Guest history persists and compacts, but guests have no profile, reflection, tools, skills, files, workspace, plugins, or delegation. They can use only `/link`, `/help`, `/new`, `/compact`, and `/abort`; attachments are rejected before download, and linking does not merge previous guest history.

Guest traffic is limited per sender, each channel has a durable guest cap, and inactive guest identities and sessions are removed after the configured retention period. Public guest access can still create model cost and abuse risk. Use a dedicated guest-safe agent whose base prompt contains no secrets.

## Commands

Feishu supports the standard chat commands:

| Command    | Description                                                   |
| ---------- | ------------------------------------------------------------- |
| `/new`     | Start a fresh session (previous history leaves memory search) |
| `/compact` | Compress the current session in place                         |
| `/abort`   | Cancel the in-progress response                               |
| `/whoami`  | Show your platform identity                                   |

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
  "allow_group": false,
  "allow_dm": true,
  "allow_unlinked_dm": false,
  "guest_message_limit_per_minute": 10,
  "guest_max_per_channel": 1000,
  "guest_retention_days": 30,
  "require_mention": true,
  "groups": {
    "oc_example": {
      "enabled": true,
      "require_mention": false,
      "allowed_users": ["on_platform_admin"],
      "disallowed_users": ["on_former_member"],
      "system_prompt": "Answer as the infra assistant for this group."
    }
  }
}
```

| Field                | Description                                                                                                            |
| -------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `app_id`             | Feishu app ID                                                                                                          |
| `app_secret`         | Feishu app secret                                                                                                      |
| `encrypt_key`        | Optional event encryption key                                                                                          |
| `verification_token` | Optional event verification token                                                                                      |
| `enable_notify`      | Allow scheduler and notify output to target Feishu                                                                     |
| `tenant_key`         | Your enterprise tenant key. Optional: Stella can auto-detect it at startup, but setting it explicitly is recommended   |
| `auto_provision`     | Create accounts only for verified tenant members: a direct message, or a group message explicitly @mentioning this bot |
| `allow_group`        | Accept messages from Feishu groups the bot was added to; defaults to `false`                                           |
| `allow_dm`           | Accept private messages, account linking, and private-message auto-provisioning; defaults to `true`                    |
| `allow_unlinked_dm`  | Allow restricted guest sessions for unlinked private senders; defaults to `false`                                      |
| `require_mention`    | Require an @mention in group chats; defaults to `true`                                                                 |
| `groups`             | Optional per-chat overrides keyed by Feishu `chat_id`                                                                  |

Guest limits use `guest_message_limit_per_minute` (default `10`), `guest_max_per_channel` (default `1000`), and `guest_retention_days` (default `30`).

Each `groups.<chat_id>` entry can set `enabled` and `require_mention`, which override the channel-wide defaults for that chat. It can also set `allowed_users` and `disallowed_users`; list canonical `union_id` values where possible, with `open_id` supported for event compatibility. A deny entry always wins. An explicitly enabled group can be opened while `allow_group` remains `false`, so this now supports a narrow group allowlist without opening every group the bot joins. In the Web UI, save and enable the channel first, then choose from the groups the bot currently belongs to. You can also store the equivalent JSON in configuration.

## Troubleshooting

**Bot not responding to messages?**

- Make sure `stellad server` is running and the Feishu channel is configured in the Web UI.
- Verify your App ID and App Secret are correct.
- Check that you added the `im.message.receive_v1` event subscription to your Feishu app.

**Bot not responding in groups?**

- @mention the bot for the most reliable trigger.
- Turn on **Allow group chats**. It is off by default and rejects every group message.
- If you expect replies without @mentions, make sure at least one group agent has a routing-capable model and that the message is a clear request, not casual chatter.

**Auto-provisioning not creating users?**

1. Make sure you enabled auto-provision on the correct Feishu channel instance -- it is configured per instance, not globally.
2. In groups, the user must @mention this bot for first-contact enrollment. Semantic routing of an unmentioned message does not auto-provision its sender.
3. Verify that `tenant_key` is set or that startup auto-detection succeeded, and that the message event carries that exact tenant key. If either check fails, auto-provision is skipped.
4. Confirm your Feishu app has these scopes: `contact:user.base:readonly`, `contact:user.id:readonly`, and `contact:user.email:readonly` if you want to store actual member emails.
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

- Configure **Admin -> Models** for image baselines. A model declaring `text, image` receives active-turn pixels. A group image is described once, on the first turn that reads it; without a usable baseline model the history shows the unavailable marker instead.
- For file uploads, the Xberg skill must be enabled for the active agent.

**Notifications not working?**

- Set `enable_notify` to `true` in your channel config.
- Make sure the bot has already had a conversation with the target user or group.
