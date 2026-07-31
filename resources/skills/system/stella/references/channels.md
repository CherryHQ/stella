# Channel setup

All channel configuration is stored in the database and managed via the Web UI (`http://localhost:25678` by default). Each channel row is a bot instance with an `id`, platform `type`, optional dedicated `agent_id`, enabled flag, and JSON config. Multiple instances can share the same type, such as two Feishu bots bound to different agents.

## Agent routing

- **DMs**: Use the user's default agent (set via `/agent` command)
- **Groups**: Use the group's assigned agent (set via `/agent` command in the group)
- **Dedicated channels**: If a channel instance has `agent_id`, all chats on that channel use the bound agent and `/agent` switching is disabled there
- **Fallback**: First enabled agent

In the Web UI, groups appear alongside agents in the main agent sidebar. A group conversation uses the same transcript and composer shape as an agent chat, plus a group monitor for member activity and shared upload context. Mention a member agent with `@<agent-id>` when you need deterministic routing in a web group.

Commands available in all channels:

- `/agent` -- List available agents or switch to a specific one
- `/new` -- Start a fresh session; the previous one is archived and stays searchable
- `/compact` -- Compress the current session in place (same session, shorter context)
- `/model` -- Switch model interactively
- `/whoami` -- Show your user/chat ID

`/new` works in direct messages only. A group's context is shared by every
member, so a group `/new` is refused and resets nothing; `/compact` is not
available in groups either. Neither command enters the group's shared history.
If a user asks in words for a fresh session, point them at `/new` in a direct
message rather than claiming to have reset anything.

## Telegram bot

1. Create a bot via @BotFather on Telegram
2. Copy the token
3. Open the Web UI and add it in the Channels tab

Telegram channel config (JSON):

```json
{
  "token": "BOT_TOKEN",
  "channel_id": "@my_channel",
  "enable_notify": true
}
```

Or set `STELLA_TELEGRAM_TOKEN` env var for the token only.

4. Start: `stellad server`

### Features

- Streaming responses via Draft API (Bot API 9.3+), falls back to edit-in-place
- Image input: send photos for vision-based analysis (requires vision-capable model)
- Multi-agent: `/agent` to list or switch agents per DM or group
- In-chat commands: `/new`, `/compact`, `/model`, `/agent`, `/whoami`

### Group support

In group chats the bot participates automatically and group routing is decided semantically. @mentions always route to the mentioned bot. To stop a bot from participating in a group, remove it from that group.

### Access control

Channel access is enforced by Stella's Authority-based services. Use the Web UI to manage users and channel configuration.

### Notifications

Set `enable_notify: true` for proactive messages (scheduler results, notify tool). Notification targets are resolved automatically from auth_identities.

## QQ bot

1. Register at https://q.qq.com/
2. Get AppID and AppSecret
3. Open the Web UI and add it in the Channels tab

QQ channel config (JSON):

```json
{
  "app_id": "YOUR_APP_ID",
  "app_secret": "YOUR_APP_SECRET",
  "enable_notify": false
}
```

4. Start: `stellad server`

Connects via WebSocket (no public URL needed). QQ supports the same channel instance routing as other chat channels.

### QQ features

- Native Stream API for progressive responses
- C2C (private) and group @mention support
- Image input support
- Commands: `/start`, `/help`, `/new`, `/compact`, `/model`, `/whoami`

## Feishu bot

1. Create an app at the Feishu Developer Console
2. Get AppID and AppSecret
3. Enable the Bot capability and subscribe to message events
4. Open the Web UI and add it in the Channels tab

Feishu channel config (JSON):

```json
{
  "app_id": "YOUR_APP_ID",
  "app_secret": "YOUR_APP_SECRET",
  "encrypt_key": "",
  "verification_token": "",
  "enable_notify": false
}
```

5. Start: `stellad server`

Connects via WebSocket (no public URL or webhook needed).

### Feishu features

- Edit-in-place streaming for progressive responses
- Private (p2p) and group @mention support
- Commands: `/new`, `/compact`, `/model`, `/agent`, `/whoami`
- Feishu OAuth login and Feishu channel chat share `union_id`; Feishu OAuth login immediately links the channel identity to the same Stella user, with first channel message as a fallback.
- Chat transport only.

## WeChat bot (iLink)

1. Open the Web UI and go to Channels → New Channel
2. Select Weixin, enter a name, keep the fixed channel ID `weixin`, and bind an enabled agent
3. Click "Scan to create WeChat bot"
4. Scan the QR code with WeChat and confirm bot creation
5. Stella saves the returned iLink credentials automatically

WeChat channel config (JSON):

```json
{
  "bot_token": "OBTAINED_VIA_SCAN_OR_MANUAL_SETUP",
  "base_url": "https://ilinkai.weixin.qq.com",
  "bot_id": "AUTO",
  "user_id": "AUTO"
}
```

Manual setup remains available in the Web UI under "I already have WeChat iLink credentials". WeChat is singleton-only in Stella because one iLink account does not support multiple independent bots.

Uses long-polling via iLink Bot API (no public URL needed). DM only for v1.

### WeChat features

- Long-polling message receipt via `getupdates`
- Text messaging with 2000-char smart splitting
- Image input/output with AES-128-ECB encryption
- Typing indicators while processing
- QR-code-based bot creation via Web UI
- Commands: `/start`, `/help`, `/new`, `/compact`, `/model`, `/agent`, `/whoami`

### WeChat limitations

- Notifications require a cached `context_token` (in-memory only, lost on restart)
- No group chat support (DM only)
- Session expiry requires a fresh scan-created bot or a fresh manual credential bundle

## Webhook (inbound trigger)

Not a chat bot: an inbound-only HTTP endpoint that runs the bound agent. There is no outbound messaging and no in-chat commands. Access is a **capability** — a one-time URL that carries an opaque secret and fixes which user the agent runs as. There is no caller token and no `Authorization` header.

1. Every authenticated user can add their own Webhook channel with a channel ID (e.g. `deploy-notify`) and bind an agent they can use. Other channel types remain admin-managed deployment resources.
2. In the channel's **Capability endpoint** panel, click **Activate endpoint**. It always runs as the current channel owner; there is no owner picker and administrators cannot manage another user's webhook.
3. Copy the **one-time URL** (`https://your-host/webhooks/<capability>`) shown once on activation; it cannot be recovered afterward
4. Callers `POST` to that URL; the request body becomes the agent's message. Any headers sent are ignored — the URL itself is the credential

The agent always runs **as the endpoint's fixed owner**, with that owner's tools, memory, and permissions re-checked on every request. If the owner loses access (assignment removed, account deactivated, agent or channel disabled), later triggers fail closed with an opaque `404`.

- **Rotate** issues a fresh one-time URL and immediately invalidates the previous one (use it if a URL may have leaked, or to recover one you did not copy).
- **Revoke** deletes the endpoint; every URL for it stops working at once. The channel remains, and you can activate a new endpoint later.

Webhook channel config (JSON):

```json
{
  "wait_timeout_seconds": 60,
  "max_run_timeout_seconds": 300
}
```

- Every request chooses `?wait=true|false&session_mode=ephemeral|persistent`. Omitted options default to async (`wait=false`) and an ephemeral session. Invalid values return 400; persistent mode keeps one session per owner/webhook (busy session → 429).
- Limits: 256 KiB body (413 when exceeded), a per-endpoint read deadline (408 if the body stalls), and a per-endpoint rate/ingress limit plus max 10 in-flight runs (429 when exceeded)
- **Cutover:** the old `POST /webhooks/<channel-id>` route with a Bearer personal access token no longer works; activate a capability endpoint and re-point callers at the one-time URL. Full guide: [Webhook channel docs](/docs/channels/webhook)
