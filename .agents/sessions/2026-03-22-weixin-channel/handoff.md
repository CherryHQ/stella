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
