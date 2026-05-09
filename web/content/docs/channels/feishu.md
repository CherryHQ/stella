---
title: Feishu Bot
---

stella includes a Feishu (Lark) bot that connects over WebSocket, so you do not need a public webhook URL. The Feishu integration is now chat-only: messages, streaming responses, threads, groups, and notifications stay in stella, while Lark workspace automation moved to `lark-cli`.

## Setup

1. Create a Feishu app at [Feishu Open Platform](https://open.feishu.cn/).
2. Enable the **Bot** capability.
3. Under **Event Subscriptions**, add:
   - `im.message.receive_v1`
   - `im.message.reaction.created_v1` if you want reaction events
4. Copy your App ID, App Secret, Encrypt Key, and Verification Token.
5. Run `stella --open` and configure the Feishu channel in the admin panel.
6. Start stella:

```bash
stella
```

You can create multiple Feishu channel instances in the admin panel. Each instance can use its own Feishu app credentials and can optionally be bound to a dedicated agent.

## Lark Workspace Automation

The old built-in `feishu_*` tools and `/auth` flow were removed.

stella now ships a generated builtin `lark` system skill, and release builds embed `lark-cli` automatically. For calendar, tasks, docs, wiki, sheets, drive, contacts, and other workspace operations, enable the builtin `lark` skill and use it with [`lark-cli`](https://github.com/larksuite/cli).

Typical setup:

```bash
command -v lark-cli || npm install -g @larksuite/cli
lark-cli config init --new
lark-cli auth login --recommend
lark-cli auth status
```

The builtin `lark` skill maps the retired `feishu_calendar`, `feishu_task`, `feishu_im`, `feishu_doc`, `feishu_wiki`, `feishu_sheets`, `feishu_drive`, `feishu_bitable`, `feishu_user`, and `feishu_search` workflows to `lark-cli` services.

## Auto-Provisioning

When enabled for a Feishu channel instance, stella automatically creates an Stella account for an employee the first time they message that bot. No manual registration or `/link` step is needed.

### How it works

1. A user messages the bot.
2. Auto-provision runs only for messages that the bot actually handles. In groups, the default `group_mode` is `mention`, so the user must `@` the bot unless you change that channel or group override to `always`.
3. Stella determines the bot tenant key:
   - if `tenant_key` is configured, that value is used
   - otherwise stella tries to auto-detect it at startup via the Feishu tenant API
4. If the message event includes a `tenant_key` and it does not match the bot tenant, stella skips auto-provision for that sender.
5. Stella calls the Feishu Contact API (`contact.v3.user.get`) to retrieve the user's `union_id`, display name, and email.
6. A new Stella user is created with the email local-part as username (`alice` from `alice@corp.com`), falling back to `feishu-<union_id[:8]>` if no email is available. Username collisions get a `-2`, `-3`, … suffix.
7. The provisioned user has no password — they can chat with the bot immediately but cannot log into the admin UI until an admin sets a password for them.
8. Provisioned users are assigned the `user` role and the system default agent.

Auto-provisioning is best-effort. If tenant detection or the Contact API lookup fails, the message still goes through the normal channel flow, but no Stella user is created.

### Required app scopes

Add these scopes to your Feishu app under **Permissions & Scopes**:

- `contact:user.base:readonly`
- `contact:user.id:readonly`

### Finding your tenant key

In the Feishu Admin Console, go to **Enterprise Information** (企业信息). The tenant key is labeled **企业标识** or **Tenant Key**.

`tenant_key` is optional in the current implementation because stella can auto-detect it at startup. Still, setting it explicitly is recommended because it removes one failure mode and makes auto-provisioning more predictable.

### Configuration

```json
{
  "app_id": "FEISHU_APP_ID",
  "app_secret": "FEISHU_APP_SECRET",
  "tenant_key": "YOUR_TENANT_KEY",
  "auto_provision": true
}
```

> **Warning:** External guests in shared groups are not auto-provisioned. If their tenant key differs from the bot tenant, stella skips account creation for them. This is by design.

> **Note:** If no admin user exists yet, auto-provisioning is refused until the first admin registers via the admin UI. This prevents stranding a fresh deployment with zero admins.

## Multi-User Support

Each Feishu user is resolved from platform identity automatically. stella prefers Feishu `union_id` when the event payload includes it, and falls back to `open_id` for older links. That makes multi-instance Feishu setups work across multiple Feishu apps owned by the same developer account, because `union_id` is stable across those apps while `open_id` is app-scoped.

Existing older Feishu links that were stored as `open_id` are upgraded opportunistically the next time the user messages from the linked bot after upgrading stella. If a user was linked only on an older bot and has not talked to it since upgrading, they can also re-run `/link` once from any Feishu app to refresh the link onto the stable identifier.

Sessions are scoped per user and per agent, so different users keep separate memory and default-agent state.

## Streaming Responses

The bot streams responses by editing messages in place:

1. Send an initial placeholder quickly.
2. Update the visible response while the model is generating.
3. Finish with the complete response and elapsed time footer.

Tool activity from the runner is summarized inline during streaming.

## Supported Message Types

| Type               | Behavior                                                                            |
| ------------------ | ----------------------------------------------------------------------------------- |
| Text               | Sent to the LLM as text                                                             |
| Image              | Downloaded and passed as multimodal input                                           |
| Post               | Raw rich-text JSON is forwarded                                                     |
| Audio              | Sent as descriptive text with duration                                              |
| Video              | Sent as descriptive text with duration                                              |
| File               | Downloaded, saved to disk, and passed to the agent with a kreuzberg extraction hint |
| Sticker            | Sent as descriptive text                                                            |
| Location           | Sent as descriptive text with coordinates when present                              |
| Shared chat/user   | Sent as descriptive text                                                            |
| Forwarded messages | Sent as a summary marker                                                            |

## Native Threading

When a user messages inside a Feishu thread, stella keeps the response in that thread and scopes the session to the thread root. Replies outside threads stay in the parent chat session.

## Group Behavior

`group_mode` controls whether stella responds in groups:

- `mention`: respond only when the bot is mentioned
- `always`: respond to every message
- `disabled`: never respond in groups

You can also set per-group overrides with the `groups` map in channel config.

## Commands

Feishu supports the standard chat commands:

| Command    | Description                     |
| ---------- | ------------------------------- |
| `/new`     | Start a fresh session           |
| `/compact` | Compact session history         |
| `/abort`   | Cancel the in-progress response |
| `/model`   | List or switch models           |
| `/agent`   | List or switch agents           |
| `/whoami`  | Show your platform identity     |

## Config Reference

```json
{
  "app_id": "FEISHU_APP_ID",
  "app_secret": "FEISHU_APP_SECRET",
  "encrypt_key": "",
  "verification_token": "",
  "group_mode": "mention",
  "enable_notify": false,
  "tenant_key": "",
  "auto_provision": false,
  "groups": {
    "oc_example": {
      "group_mode": "always",
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
| `group_mode`         | Default group behavior: `mention`, `always`, or `disabled`                                                           |
| `enable_notify`      | Allow scheduler and notify output to target Feishu                                                                   |
| `tenant_key`         | Your enterprise tenant key. Optional: stella can auto-detect it at startup, but setting it explicitly is recommended |
| `auto_provision`     | Automatically create Stella accounts for users handled by this Feishu channel instance                               |
| `groups`             | Optional per-chat overrides keyed by Feishu `chat_id`                                                                |

## Troubleshooting Auto-Provisioning

If new Feishu users are not being created, check these first:

1. **You enabled it on the correct Feishu instance.** Auto-provision is configured per channel instance, not globally.
2. **The bot is actually handling the message.** In groups, `group_mode: mention` requires an `@` mention.
3. **`tenant_key` is set or startup auto-detection succeeded.** If tenant detection fails and no explicit `tenant_key` is configured, auto-provision is skipped.
4. **The Feishu app has these scopes:**
   - `contact:user.base:readonly`
   - `contact:user.id:readonly`
5. **At least one Stella admin already exists.** Fresh deployments refuse auto-provision until the first admin account is created.
6. **The sender is an internal tenant member.** External guests are intentionally not auto-provisioned.
7. **You restarted stella after config changes.**

A practical, reliable setup is:

```json
{
  "app_id": "FEISHU_APP_ID",
  "app_secret": "FEISHU_APP_SECRET",
  "tenant_key": "YOUR_TENANT_KEY",
  "auto_provision": true,
  "group_mode": "always"
}
```

Use `group_mode: "always"` only if you want provisioning and replies to happen for every group message. Otherwise keep `mention` and make sure users `@` the bot on first contact.
