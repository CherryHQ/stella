---
title: Feishu Bot
---

anna includes a Feishu (Lark) bot that connects over WebSocket, so you do not need a public webhook URL. The Feishu integration is now chat-only: messages, streaming responses, threads, groups, and notifications stay in anna, while Lark workspace automation moved to `lark-cli`.

## Setup

1. Create a Feishu app at [Feishu Open Platform](https://open.feishu.cn/).
2. Enable the **Bot** capability.
3. Under **Event Subscriptions**, add:
   - `im.message.receive_v1`
   - `im.message.reaction.created_v1` if you want reaction events
4. Copy your App ID, App Secret, Encrypt Key, and Verification Token.
5. Run `anna --open` and configure the Feishu channel in the admin panel.
6. Start anna:

```bash
anna
```

## Lark Workspace Automation

The old built-in `feishu_*` tools and `/auth` flow were removed.

For calendar, tasks, docs, wiki, sheets, drive, contacts, and other workspace operations, install a `lark-cli` skill if you want one, and use it with the external [`lark-cli`](https://github.com/larksuite/cli) tool.

Typical setup:

```bash
command -v lark-cli
npm install -g @larksuite/cli
lark-cli config init --new
lark-cli auth login --recommend
lark-cli auth status
```

A user-installed `lark-cli` skill can map the retired `feishu_calendar`, `feishu_task`, `feishu_im`, `feishu_doc`, `feishu_wiki`, `feishu_sheets`, `feishu_drive`, `feishu_bitable`, `feishu_user`, and `feishu_search` workflows to `lark-cli` services.

## Multi-User Support

Each Feishu user is resolved from platform identity automatically. Sessions are scoped per user and per agent, so different users keep separate memory and default-agent state.

## Streaming Responses

The bot streams responses by editing messages in place:

1. Send an initial placeholder quickly.
2. Update the visible response while the model is generating.
3. Finish with the complete response and elapsed time footer.

Tool activity from the runner is summarized inline during streaming.

## Supported Message Types

| Type | Behavior |
| --- | --- |
| Text | Sent to the LLM as text |
| Image | Downloaded and passed as multimodal input |
| Post | Raw rich-text JSON is forwarded |
| Audio | Sent as descriptive text with duration |
| Video | Sent as descriptive text with duration |
| File | Sent as descriptive text with file metadata |
| Sticker | Sent as descriptive text |
| Location | Sent as descriptive text with coordinates when present |
| Shared chat/user | Sent as descriptive text |
| Forwarded messages | Sent as a summary marker |

## Native Threading

When a user messages inside a Feishu thread, anna keeps the response in that thread and scopes the session to the thread root. Replies outside threads stay in the parent chat session.

## Group Behavior

`group_mode` controls whether anna responds in groups:

- `mention`: respond only when the bot is mentioned
- `always`: respond to every message
- `disabled`: never respond in groups

You can also set per-group overrides with the `groups` map in channel config.

## Commands

Feishu supports the standard chat commands:

| Command | Description |
| --- | --- |
| `/new` | Start a fresh session |
| `/compact` | Compact session history |
| `/model` | List or switch models |
| `/agent` | List or switch agents |
| `/whoami` | Show your platform identity |

## Config Reference

```json
{
  "app_id": "FEISHU_APP_ID",
  "app_secret": "FEISHU_APP_SECRET",
  "encrypt_key": "",
  "verification_token": "",
  "group_mode": "mention",
  "enable_notify": false,
  "groups": {
    "oc_example": {
      "group_mode": "always",
      "system_prompt": "Answer as the infra assistant for this group."
    }
  }
}
```

| Field | Description |
| --- | --- |
| `app_id` | Feishu app ID |
| `app_secret` | Feishu app secret |
| `encrypt_key` | Optional event encryption key |
| `verification_token` | Optional event verification token |
| `group_mode` | Default group behavior: `mention`, `always`, or `disabled` |
| `enable_notify` | Allow scheduler and notify output to target Feishu |
| `groups` | Optional per-chat overrides keyed by Feishu `chat_id` |
