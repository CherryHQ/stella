# Handoff

<!-- Append a new phase section after each phase completes. -->

## Phase 1: Protocol Types & HTTP Client — DONE

### Commits

1. `21ac4b18` — `model.go` — All protocol types (WeixinMessage, MessageItem, TextItem, ImageItem, FileItem, VideoItem, VoiceItem, CDNMedia, RefMessage, BaseInfo) and constants (message type, state, item type, media type). API request/response types for all endpoints. QRCodeResponse and QRCodeStatusResponse for auth flow.
2. `814187d2` — `client.go` — HTTP Client struct with methods: randomWechatUIN(), commonHeaders(), GetQRCode(), GetQRCodeStatus() (with iLink-App-ClientVersion:1), GetUpdates() (adaptive timeout), SendMessage(), GetConfig(), SendTyping(), GetUploadURL(). Error handling: ErrSessionExpired for ret=-14/errcode=-14, generic errors for other non-zero codes.
3. `ad0449ff` — `media.go` — AES-128-ECB encrypt/decrypt with PKCS7, CiphertextSize(), DecodeAESKey() (dual format A/B), ResolveImageKey() (precedence: hex field > media base64), UploadToCDN(), DownloadFromCDN(), RandomFileKey(), RandomClientID().
4. `11a8b5d9` — `weixin_test.go` — 17 tests covering randomWechatUIN format, AES round-trip, known vector, CiphertextSize, DecodeAESKey both formats, ResolveImageKey precedence, RandomFileKey, RandomClientID. All pass with `-race`.

### Files Created

- `internal/channel/weixin/model.go` (236 lines)
- `internal/channel/weixin/client.go` (355 lines — slightly over 300 but each method is focused)
- `internal/channel/weixin/media.go` (235 lines)
- `internal/channel/weixin/weixin_test.go` (300 lines)

### Notes for Phase 2

- `Client` is fully stateless — token and URLs are set at construction. Phase 2's Bot struct will own a Client instance.
- `GetUpdates` accepts a `timeout` parameter so the caller can adapt based on `longpolling_timeout_ms` from previous response.
- `SendMessage` accepts the full `WeixinMessage` struct — Phase 2/3 will construct messages with proper client_id, context_token, etc.
- No external dependencies added — all stdlib (`crypto/aes`, `encoding/base64`, `encoding/hex`, `net/http`, `crypto/rand`).
- The `logger()` function is defined in `client.go` following the same pattern as telegram/qq channels.

## Phase 2: Bot Core & Message Loop — DONE

### Commits

1. `48f239ea` — Task 2.1: Bot struct, Config, New(), WithAuth(), BotOption pattern, isAllowed(), Name(), Stop(). Follows telegram/qq patterns exactly.
2. `e300d0f8` — Task 2.2: Start() with getupdates long-poll loop. Adaptive timeout from `longpolling_timeout_ms`, cursor persistence to DB via `persistCursor()`, retry/backoff (2s wait, 30s after 3 consecutive failures), session expiry handling (clear all credentials + state on ret=-14), local timeout treated as empty response. Includes `loadCursor()`, `clearCredentials()`, `isTimeoutError()`, and placeholder `handleUpdates()`.
3. `c0021741` — Task 2.3: Notify() sends text via sendmessage using cached context_token from sync.Map. Returns explicit error when no token available. Falls back to NotifyChat config.
4. `94f562db` — Task 2.4: resolve() delegates to channel.Resolve() with platform="weixin", DM-only (isGroup=false).

### Files Created

- `internal/channel/weixin/weixin.go` (338 lines)

### Notes for Phase 3

- `handleUpdates()` is a placeholder that just logs message count. Phase 3 must implement full message dispatch.
- `contextTokens` sync.Map is ready for Phase 3 to populate via `b.contextTokens.Store(userID, contextToken)` when processing incoming messages.
- `typingTickets` sync.Map is ready for Phase 3's typing indicator goroutine.
- `resolve()` is ready — returns `*channel.ResolvedChat` for agent dispatch.
- `agentCmd` is initialized in New() and recreated in WithAuth() — ready for /agent command handling.
- `listFn` and `switchFn` are stored — ready for /model command handling.
- `client` is created in Start() from config values, not in New() — this matches the pattern where credentials may come from DB.
- `dbConfig` type embeds Config + GetUpdatesBuf for DB JSON serialization.
- `clearCredentials()` deletes bot_token, bot_id, user_id, get_updates_buf from DB config and clears both sync.Maps.

## Phase 3: Message Handling & Streaming — DONE

### Commits

1. `4b922bce` — Task 3.1: `handler.go` — handleUpdates (filter by message_type/state, cache context_token, dispatch by item type), handleText (link code → shared commands → /model → /agent → agent chat), handleImage (CDN download → AES decrypt → MIME detect → multimodal content), handleMessage (resolve → stream → render), handleModelCommand (text-based list/filter/switch, qq pattern), sendReply helper. Removed placeholder handleUpdates from weixin.go.
2. `b7eceb84` — Task 3.2: `stream.go` — streamEvents (consume events, accumulate text, track tools), toolTracker (full history, renderFinal summary matching telegram pattern), keepTyping (typing status=1 every 5s, status=2 on cancel), getTypingTicket (cache via getconfig API).
3. `eb9ef83d` — Task 3.3: `render.go` — sendFinalResponse (split at 2000 chars via SplitMessage, send chunks + images), sendImage (base64 decode → AES encrypt → CDN upload → sendmessage with image_item), sendFile (encrypt → upload media_type=3 → file_item), sendVideo (encrypt → upload media_type=2 → video_item). All media sends use no_need_thumb: true.

### Files Created

- `internal/channel/weixin/handler.go` (331 lines)
- `internal/channel/weixin/stream.go` (297 lines)
- `internal/channel/weixin/render.go` (280 lines)

### Files Modified

- `internal/channel/weixin/weixin.go` — removed placeholder handleUpdates method

### Notes for Phase 4

- All message handling is complete: text, image, file (log+skip), video (log+skip).
- Commands: /start, /help, /new, /compact, /whoami (shared), /model (text-based list/switch), /agent (shared HandleAgentCommand).
- Link code account linking works via channel.TryLinkCode.
- context_token is cached in sync.Map on every incoming message.
- typing_ticket is cached per user, fetched via getconfig on first use.
- Tool tracker renders compact summary in final message (same format as telegram).
- sendFile and sendVideo are implemented but not invoked from inbound handling (files/videos are logged and skipped per plan). They are available for future use by agents or notifications.
- The handleModelCommand follows the qq/feishu text-based pattern — no inline keyboard since WeChat doesn't support that. Uses /model <provider/model> to switch.
- No chatModels per-session cache was added since the weixin Bot struct doesn't have one. switchFn persists the model globally.

## Phase 4: Gateway Integration & Admin Panel — DONE

### Commits

1. `3b4f52ad` — Task 4.1: Gateway integration — weixinChannelConfig type, weixin init block in gateway.go, import, channel/notifier registration.
2. `f3d13681` — Task 4.2: QR login API — `internal/admin/weixin_qr.go` with `startWeixinQR` (POST) and `pollWeixinQRStatus` (GET) handlers, credential merge on confirmed, routes in server.go using adminAPI wrapper.
3. `bd608523` — Task 4.3: Channels config form — Weixin block in channels.templ (enable, notify, QR login button with status badges, notify_chat with tooltip, allowed_ids). JS: channelData.weixin, loadChannels parsing, startQR/pollQRStatus with 3s interval polling.
4. `55bb36ef` — Task 4.4: Profile link code — Added "weixin" to platform list in profile.templ and profile.js platformLabel map.
5. `d3645ab4` — Task 4.5: Lint fixes — Fixed errcheck (resp.Body.Close), De Morgan's law in isHexString, nolint:unused for sendFile/sendVideo, regenerated templ files. `mise run generate`, `mise run format`, `mise run lint` all pass clean.

### Files Created

- `internal/admin/weixin_qr.go` (89 lines)

### Files Modified

- `cmd/anna/gateway.go` — weixin config type + init block
- `internal/admin/server.go` — weixin QR API routes
- `internal/admin/ui/pages/channels.templ` — weixin channel block
- `internal/admin/ui/static/js/pages/channels.js` — weixin data + QR flow
- `internal/admin/ui/pages/profile.templ` — weixin in platform list
- `internal/admin/ui/static/js/pages/profile.js` — weixin label
- `internal/channel/weixin/client.go` — errcheck fixes
- `internal/channel/weixin/media.go` — errcheck + De Morgan fix
- `internal/channel/weixin/render.go` — nolint:unused directives

### Notes for Phase 5

- All gateway wiring is done. Server start will initialize weixin channel when bot_token exists in DB.
- QR login flow: admin clicks "Scan QR to Login" → POST /api/channels/weixin/qr → displays QR image → polls GET /api/channels/weixin/qr/status every 3s → on confirmed, credentials saved to DB and loadChannels refreshes UI.
- Admin routes use adminAPI (adminOnlyMiddleware) wrapper — same as all other channel config routes.
- Profile link code UI now shows weixin alongside telegram/qq/feishu.
- Lint is fully clean (0 issues). All earlier phases' lint issues (errcheck, staticcheck, unused) were fixed.
