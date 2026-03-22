# Plan: WeChat (Weixin) iLink Bot Channel

## Overview

Add a `weixin` channel to anna implementing the WeChat iLink Bot API protocol. The channel supports QR-code-based login, long-poll message receipt, text/image/file/video messaging with client-side AES encryption, typing indicators, and full integration with anna's agent pool, auth, and notification systems.

### Goals

- Implement `channel.Channel` interface for weixin
- Support QR login with admin panel UI for QR code display
- Receive messages via `getupdates` long-polling
- Send text replies with 2000-char smart splitting
- Full media support: image/file/video upload and download with AES-128-ECB
- Typing indicators via `sendtyping` API
- Auth integration (link codes, identity resolution)
- Notification dispatch support
- Admin panel config form

### Success Criteria

- [ ] Bot can be configured and QR-scanned via admin panel
- [ ] Text messages received and replied to correctly
- [ ] Images/files/videos can be received (downloaded + decrypted)
- [ ] Images/files/videos can be sent (encrypted + uploaded)
- [ ] Typing indicator shown while processing
- [ ] `/link CODE` account linking works
- [ ] Notifications dispatched via `Notify()`
- [ ] Shared commands work (`/start`, `/new`, `/compact`, `/whoami`)
- [ ] Tests pass with `-race`, lint clean
- [ ] Admin panel shows weixin config form with QR login UI

### Out of Scope

- Voice messages (skip for v1)
- Group chat (DM only for v1)
- Auto-reconnect on session expiry (`ret=-14`) — clear all state (credentials, cursor, context_token cache) and stop; requires manual re-scan via admin panel
- Streaming via `GENERATING` state — use `FINISH` only

## Technical Approach

Follow the established channel pattern (telegram/qq/feishu). The weixin channel is most similar to Telegram: both use long-polling, both are direct HTTP APIs (no SDK).

### Key Design Decisions

1. **Pure HTTP client** — no external SDK. All iLink API calls in `client.go`.
2. **Credential storage in DB** — store `bot_token`, `baseurl`, `bot_id`, `user_id`, `get_updates_buf` in `settings_channels` config JSON (same as other channels store their config).
3. **`context_token` cache** — in-memory `sync.Map` keyed by `(botID, userID)`. Lost on restart (acceptable — new messages bring fresh tokens).
4. **`typing_ticket` cache** — in-memory per user, refreshed on failure.
5. **QR login is admin-panel-only** — QR flow is triggered manually from admin UI, never auto-started on server boot. The gateway only starts the weixin channel when `bot_token` already exists in DB config (same pattern as other channels requiring valid credentials). New API endpoint `POST /api/channels/weixin/qr` triggers QR generation, returns QR URL. Admin UI polls status. On confirmation, credentials saved to DB channel config.
6. **Media crypto** — all in `media.go`. AES-128-ECB with PKCS7. Handle dual key format (base64-of-raw vs base64-of-hex). Image AES key precedence: `image_item.aeskey` (hex) > `media.aes_key` (base64). Plaintext fallback when no key present.
7. **Notify limitation** — `Notify()` requires a cached `context_token` which is in-memory only. After restart, notifications to weixin users fail until they send a new message. The `notify_chat` config field stores a `to_user_id` but still requires a cached `context_token` to work — this limitation is documented in the admin UI tooltip.

### Components

- **`internal/channel/weixin/model.go`** — Protocol types (WeixinMessage, MessageItem, CDNMedia, etc.)
- **`internal/channel/weixin/client.go`** — HTTP client for all iLink API endpoints
- **`internal/channel/weixin/media.go`** — AES-128-ECB crypto, CDN upload/download
- **`internal/channel/weixin/weixin.go`** — Bot struct, New(), Start(), Stop(), Name(), Notify(), resolve()
- **`internal/channel/weixin/handler.go`** — Message parsing, command routing, agent dispatch
- **`internal/channel/weixin/stream.go`** — Event streaming, typing indicator, tool tracker
- **`internal/channel/weixin/render.go`** — Response formatting, message chunking, media sending
- **`internal/channel/weixin/weixin_test.go`** — Unit tests for client, media, message splitting
- **`cmd/anna/gateway.go`** — Gateway integration (config type, init, channel registration)
- **`internal/admin/`** — Admin panel: config form, QR login API + UI

## Implementation Phases

### Phase 1: Protocol Types & HTTP Client

Foundation layer — types and API client with no dependencies on anna internals.

1. Create `internal/channel/weixin/model.go` — all protocol types: `WeixinMessage`, `MessageItem`, `TextItem`, `ImageItem`, `FileItem`, `VideoItem`, `CDNMedia`, `RefMessage`, constants for message type/state, `base_info` struct. (files: `model.go`)
2. Create `internal/channel/weixin/client.go` — HTTP client struct with: `X-WECHAT-UIN` header generation, common request builder, `GetQRCode()`, `GetQRCodeStatus()` (include `iLink-App-ClientVersion: 1` header per spec section 2.2), `GetUpdates()` (use `longpolling_timeout_ms` from response as client timeout for next poll; treat local timeout/`AbortError` as empty `"wait"` response), `SendMessage()`, `GetConfig()`, `SendTyping()`, `GetUploadURL()`. Error handling: both `ret != 0` and `errcode != 0` are failures; `ret = -14` or `errcode = -14` is session expiry. (files: `client.go`)
3. Create `internal/channel/weixin/media.go` — AES-128-ECB encrypt/decrypt with PKCS7, CDN upload (POST with encrypted body, extract `x-encrypted-param`), CDN download (GET + decrypt), dual AES key format decode, ciphertext size calculation. (files: `media.go`)
4. Unit tests for client header generation, AES encrypt/decrypt round-trip, key format parsing, ciphertext size calc. (files: `weixin_test.go`)

### Phase 2: Bot Core & Message Loop

Wire up the Bot struct and getupdates long-poll loop.

1. Create `internal/channel/weixin/weixin.go` — `Bot` struct (client, poolManager, store, authStore, engine, linkCodes, agentCmd, listFn, switchFn, contextTokens map, typingTickets map, allowed map, config, mu), `Config` type with `AllowedIDs []string`, `New()` with `BotOption` pattern (builds `allowed` map from `AllowedIDs`), `WithAuth()`, `Name()`, `Stop()`, `isAllowed(userID string) bool` (same pattern as telegram/qq/feishu — empty map = allow all). (files: `weixin.go`)
2. Implement `Start()` — require valid `bot_token` in DB config (do NOT auto-start QR flow; QR is admin-panel-only). Enter `getupdates` long-poll loop with cursor management and retry/backoff (2s on failure, 30s after 3 consecutive failures per spec section 4.4), dispatch messages to handler. Handle `-14` by clearing ALL state: credentials from DB, `get_updates_buf` cursor, in-memory `contextTokens` and `typingTickets` caches, then stop and log error. Persist `get_updates_buf` cursor to DB via `store.UpsertChannel()` after every successful poll response; load cursor from DB on startup. (files: `weixin.go`)
3. Implement `Notify()` — send text via `sendmessage` using cached `context_token` for the target user. Return explicit error `fmt.Errorf("weixin: no context_token for user %s")` when no token is available (known limitation: tokens are in-memory only, lost on restart, repopulated when user sends a new message). (files: `weixin.go`)
4. Implement `resolve()` — delegate to `channel.Resolve()` with platform="weixin". (files: `weixin.go`)

### Phase 3: Message Handling & Streaming

Process incoming messages and stream agent responses.

1. Create `internal/channel/weixin/handler.go` — `handleUpdates()` filters inbound messages: skip `message_type != 1` (USER) to ignore bot's own echoes, skip `message_state != 2` (FINISH) to ignore partial/generating states, check `isAllowed(from_user_id)` before processing (matching telegram/qq/feishu guard pattern). Then dispatch by `item_list[].type`. `handleText()`: cache `context_token`, try link code, try shared commands (including `/agent` quick-switch and `/model` text-based switching like qq/feishu), fall through to `handleMessage()`. `handleImage()`: download+decrypt image from CDN using AES key precedence rules (`image_item.aeskey` hex > `media.aes_key` base64 > plaintext fallback), convert to `ai.ImageContent`. `handleFile()`/`handleVideo()`: download+decrypt, log/skip (agent can't process raw files). (files: `handler.go`)
2. Create `internal/channel/weixin/stream.go` — `streamEvents()` consumes `runner.Event` channel, accumulates text, tracks tools (reuse toolTracker pattern). `keepTyping()` goroutine: get `typing_ticket` via `getconfig`, send `sendtyping status=1` every 5s, send `status=2` on done. (files: `stream.go`)
3. Create `internal/channel/weixin/render.go` — `sendFinalResponse()` splits text at 2000 chars (prefer `\n\n` > `\n` > space > hard cut), sends each chunk via `sendmessage` with new `client_id` and same `context_token`. Tool summary footer appended to last chunk. Media sending helpers: `sendImage()` (encrypt → upload CDN with `media_type=1` → `sendmessage` with `image_item` + `mid_size`), `sendFile()` (encrypt → upload CDN with `media_type=3` → `sendmessage` with `file_item` + `file_name` + `len`), `sendVideo()` (encrypt → upload CDN with `media_type=2` → `sendmessage` with `video_item` + `video_size`). All media sends use `no_need_thumb: true` per official openclaw convention. (files: `render.go`)

### Phase 4: Gateway Integration & Admin Panel

Wire into anna server and add admin UI.

1. Add `weixinChannelConfig` struct and weixin init block in `cmd/anna/gateway.go` — load config, create bot, register with dispatcher. (files: `gateway.go`)
2. Add weixin QR login API endpoints in admin panel — `POST /api/channels/weixin/qr` (start QR flow, return QR URL + qrcode token), `GET /api/channels/weixin/qr/status?qrcode=...` (poll status, on `confirmed` save credentials to DB via `store.UpsertChannel()` merging into existing config JSON). Server-side: do NOT proxy the iLink long-poll directly — instead call iLink `get_qrcode_status` with a short timeout (5s) and return the current status to the client. Admin UI uses `setInterval` (every 3s) to poll the local status endpoint. New handler file `internal/admin/weixin_qr.go`. (files: `weixin_qr.go`, `server.go`)
3. Add weixin block to channels config form — enable toggle, notify toggle, notify chat, allowed IDs, QR login button with status display. (files: `channels.templ`, `channels.js`)
4. Add "weixin" to profile link code platform list. (files: `profile.templ`, `profile.js`)
5. Run `mise run generate` and `mise run format` and `mise run lint`. Fix any issues.

### Phase 5: Tests & Documentation

1. Add comprehensive tests — client request building, AES round-trip, message splitting, key format decode, handler routing (mock client). (files: `weixin_test.go`)
2. Update docs — add weixin section to relevant docs in `docs/content/docs/`. Update `README.md` if channel list is mentioned. (files: docs)
3. Update builtin anna skill if it references channel list. (files: `internal/agent/runner/builtin/anna/`)

## Testing Strategy

- **Unit tests**: AES encrypt/decrypt round-trip, dual key format parsing, ciphertext size calculation, `X-WECHAT-UIN` generation, message splitting at 2000 chars (boundary cases), `client_id` uniqueness
- **Integration tests**: Mock HTTP server simulating iLink API responses for `getupdates`/`sendmessage` flow
- **Edge cases**: Empty `context_token`, `ret=-14` session expiry, long-poll timeout (treat as empty), message with multiple `item_list` entries, AES key in both formats within same message
- **Race detection**: All tests run with `-race` flag

## Risks

| Risk | Impact | Mitigation |
| --- | --- | --- |
| QR code expires before user scans | Medium | Log clear instructions, admin UI shows refresh button |
| `context_token` lost on restart | Medium | Acceptable — new incoming messages bring fresh tokens. Document this limitation. |
| AES key format ambiguity | High | Implement dual-format decode with explicit length check (16 raw vs 32 hex) |
| iLink API undocumented errors | Medium | Log full response body on unknown errors, graceful degradation |
| CDN upload failures | Medium | Retry up to 3 times with backoff, fall back to text-only response |
| 2000 char limit too conservative | Low | Use it as default, can be made configurable later |

## Open Questions

All resolved — converted to assumptions:

- **Assumption**: Credentials stored in `settings_channels.config` JSON alongside other channel-specific state (like `get_updates_buf`). This follows the existing pattern where channel config is a free-form JSON blob.
- **Assumption**: QR login is triggered manually from admin panel, not automatically on server start. Server start with existing valid credentials skips QR flow.
- **Assumption**: `context_token` is in-memory only. Restart clears it. This is acceptable since the protocol has no "open session" API — tokens come from incoming messages.

## Review Feedback

### Round 1 (Internal Reviewer)

All 8 items addressed:
1. Added explicit `get_updates_buf` DB persistence in Phase 2 (persist after every poll, load on startup, clear on -14)
2. Added retry/backoff strategy in Phase 2 (2s wait, 30s after 3 failures)
3. Specified `store.UpsertChannel()` credential persistence in Phase 4 QR admin flow
4. `Notify()` now returns explicit error when no `context_token` available
5. Clarified admin QR polling: `setInterval` every 3s, server does short-timeout iLink call (not proxy long-poll)
6. Specified `AllowedIDs []string` in Config type
7. Added `/agent` command to Phase 3 handler
8. Added `iLink-App-ClientVersion: 1` header to Phase 1 client

Note on toolTracker duplication: acknowledged as tech debt. Each channel currently has its own copy. Extracting to shared is out of scope for this PR but noted for future cleanup.

### Round 2 (Codex — gpt-5.4)

All 8 findings addressed:
1. **File/video send paths**: Added explicit `sendFile()` and `sendVideo()` helpers in Phase 3 render.go with `media_type` mapping (1=image, 2=video, 3=file) and correct `item` construction.
2. **Inbound message filtering**: Added `message_type == 1` (USER) and `message_state == 2` (FINISH) filters in `handleUpdates()` to skip bot echoes and partial states.
3. **`-14` clears ALL state**: Updated Phase 2 to clear credentials from DB, cursor, and all in-memory caches (contextTokens, typingTickets) on session expiry. Not just cursor.
4. **`AllowedIDs` enforcement**: Added `isAllowed()` guard in `handleUpdates()` matching telegram/qq/feishu pattern. Check happens before message dispatch.
5. **`/model` command**: Added text-based `/model` handling in Phase 3 handler (same approach as qq/feishu `handleModelCommand()`).
6. **QR login consistency**: Clarified: QR is admin-panel-only. Gateway requires `bot_token` in DB to start channel (same as other channels). No auto-QR on startup.
7. **`notify_chat` limitation**: Documented that `notify_chat` stores a `to_user_id` but requires cached `context_token` to work. Admin UI shows tooltip explaining this.
8. **Spec detail coverage**: Added `errcode != 0` as failure condition, `longpolling_timeout_ms` adaptive timeout, local timeout treated as `"wait"`, image AES key precedence rules with plaintext fallback.

## Final Status

**COMPLETE** — All 5 phases implemented, reviewed, and passing.

- 26 commits on `feat/weixin-channel`
- 40 files changed, 3831 insertions
- 64 tests passing with `-race`
- Lint clean (0 issues)
- Full project builds
- No deviations from plan
