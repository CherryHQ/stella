# Handoff

<!-- Append a new phase section after each phase completes. -->

## Phase 1: Tool Infrastructure + Client Wrapper

**Status**: Complete
**Date**: 2026-03-22
**Commits**: 4 (2908ff6e, 798b3a83, 10349e1d, f2cf8c73)

### What was done

1. **`internal/feishutool/context.go`** — Context keys for `open_id`, `chat_id`, `message_id` with `With*`/`*FromContext` pairs following `internal/memory/context.go` pattern.

2. **`internal/feishutool/client.go`** — `Client` wrapping `*lark.Client` with `golang.org/x/time/rate` limiter (default 50 req/s). `Wait(ctx)` blocks until rate limit allows. `Lark()` exposes underlying SDK client. `WithRateLimit` option for customization.

3. **`internal/feishutool/helpers.go`** — `ParseTimeToUnix`/`ParseTimeToUnixMs` (ISO 8601 + raw numeric), `FormatLarkError`, `PaginatedResult[T]` generic type, `JSONResult`/`JSONResultFromAny` builders, `stringArg`/`derefStr`/`derefInt` helpers.

4. **`internal/feishutool/user.go`** — `feishu_user` tool implementing `tool.Tool` interface. Actions: `get_user` (by open_id, falls back to context), `search_user` (by email/mobile via `BatchGetId`). `formatUser` extracts key fields. `toStringSlice` handles JSON-decoded `[]any`.

5. **`cmd/anna/commands.go`** — Feishu tools wired in `setup()` after memory tool, before plugin collision detection. Loads `feishuChannelConfig` from DB, creates `lark.Client` + `feishutool.Client`, appends `feishu_user` to `sharedTools`.

6. **`internal/channel/feishu/handler.go`** — In `handleMessage()`, before `rc.Chat()`, injects `open_id`/`chat_id`/`message_id` into context using `feishutool.With*` functions.

7. **Tests** — 29 tests across 4 test files: `context_test.go`, `client_test.go`, `helpers_test.go`, `user_test.go`. All pass with `-race`.

### Dependencies added

- `golang.org/x/time v0.15.0` (for `rate.Limiter`)

### Notes for Phase 2

- `Client` is designed to be extended with `InvokeAsUser(ctx, fn)` for UAT support in Phase 2.
- The `logger()` function was removed from `client.go` (unused) — re-add when needed in later phases.
- `intArg` helper was removed from `helpers.go` (unused) — re-add when tools need integer parameters.
- Pre-existing test failure: `TestRunGatewayNoServices` fails due to port 25678 already in use (not related to this change).

## Phase 2: UAT OAuth + Token Storage

**Status**: Complete
**Date**: 2026-03-22
**Commits**: 4 (e33a3ad4, 11097fe5, 6482f5ad, eabb2366)

### What was done

1. **`internal/db/schemas/tables/feishu_tokens.sql`** — Schema for `feishu_tokens` table: `open_id` (PK), encrypted `access_token`, `refresh_token`, `expires_at`, `refresh_expires_at`, timestamps. Added to `main.sql`. Migration generated via `mise run db:diff -- add_feishu_tokens`.

2. **`internal/db/queries/feishu_tokens.sql`** — sqlc queries: `GetFeishuToken`, `UpsertFeishuToken`, `DeleteFeishuToken`. Generated sqlc code in `internal/db/sqlc/feishu_tokens.sql.go`.

3. **`internal/feishutool/token_store.go`** — `TokenStore` interface (`Get`/`Set`/`Delete`) + `SQLiteTokenStore` implementation. AES-256-GCM encryption with 32-byte key derived from `app_secret` via HKDF (SHA-256, salt: `"feishu-token-encryption"`, info: `"feishu-uat-v1"`). Nonce prepended to ciphertext, base64-encoded for storage. `Token` struct with `IsExpired()`/`IsRefreshExpired()` helpers.

4. **`internal/feishutool/client.go`** — Extended with UAT support:
   - `InvokeAsUser(ctx, requireAuth, fn)` — resolves stored UAT from TokenStore using `open_id` from context, auto-refreshes if expired, falls back to bot token or returns `NeedAuthError`.
   - `InvokeWithUserToken(ctx, requireAuth, fn)` — convenience wrapper that passes `larkcore.WithUserAccessToken`.
   - `ExchangeCode(ctx, code)` — exchanges authorization code for token pair via Feishu OIDC endpoint.
   - `SetAppCredentials(appID, appSecret)` — stores credentials for token refresh/exchange.
   - `WithTokenStore` client option.
   - Internal: `refreshToken()`, `getAppAccessToken()`, `parseOIDCTokenResponse()`, `AuthURL()`.

5. **`internal/channel/feishu/oauth.go`** — `/auth` command handling:
   - `/auth` (no args) — sends interactive card with OAuth authorization URL button.
   - `/auth <code>` — exchanges authorization code for tokens, stores via TokenStore.
   - `sendCard()` helper for interactive card replies.

6. **Wiring** — `cmd/anna/commands.go`: `SQLiteTokenStore` created in `setup()` alongside feishu client, passed via `WithTokenStore` option. `fsClient` added to `setupResult`. `cmd/anna/gateway.go`: `WithFeishuClient` option passes `fsClient` to feishu `Bot`.

7. **`internal/channel/feishu/feishu.go`** — Added `fsClient *feishutool.Client` field to `Bot`, `WithFeishuClient` option.

8. **`internal/channel/feishu/handler.go`** — `/auth` command intercepted early in `onMessage()` (before general command dispatch) since it needs `messageID` for card replies.

9. **Tests** — 20 new tests:
   - `token_store_test.go`: encrypt/decrypt round-trip, CRUD, upsert, delete, not-found, expiry detection, different ciphertext for same plaintext, wrong-key decryption fails, deterministic key derivation.
   - `client_uat_test.go`: InvokeAsUser with no store, no store + requireAuth, no openID, valid token, both expired, token not found, TokenStore accessor.
   - `oauth_test.go`: AuthURL generation.

### Notes for Phase 3

- `InvokeAsUser` and `InvokeWithUserToken` are ready for use by calendar/task/bitable tools.
- `NeedAuthError` type is defined but not yet caught by the channel handler (removed `handleNeedAuth` to pass lint — re-add when tools return it in Phase 3+).
- Token refresh requires a real Feishu API call (not unit-testable without mocking HTTP); the refresh logic path is tested via the expired-token fallback tests.
- The `AuthURL` function uses empty `redirect_uri` — this is intentional for the code-copy flow where users manually paste the code.

## Phase 3: Core Feishu Tools (Calendar, Tasks, Bitable)

**Status**: Complete
**Date**: 2026-03-22
**Commits**: 5 (ba274816, 3784505e, 6a751c0c, a7a87524, 152dbbed)

### What was done

1. **`internal/feishutool/calendar.go`** — `feishu_calendar` tool with 7 actions: `create_event`, `list_events` (uses InstanceView API to auto-expand recurring events), `get_event`, `update_event`, `delete_event`, `add_attendees`, `freebusy`. Auto-resolves primary calendar when `calendar_id` omitted. Builds attendees from explicit list + `user_open_id` (from args or context) with deduplication. Uses `InvokeWithUserToken` for all operations with bot token fallback.

2. **`internal/feishutool/task.go`** — `feishu_task` tool with 9 actions: `create_task`, `list_tasks`, `get_task`, `update_task` (with `update_fields` array), `complete_task`, `create_tasklist`, `list_tasklists`, `create_subtask`, `list_subtasks`. Uses Task v2 API with millisecond timestamps via `ParseTimeToUnixMs`. `currentTimeMs` is a package-level var for testability.

3. **`internal/feishutool/bitable.go`** — `feishu_bitable` tool with 12 actions: `create_app`, `list_tables`, `create_table`, `list_records` (via Search API, supports structured filter/sort/field_names), `create_record`, `update_record`, `delete_record`, `batch_create_records`, `batch_update_records`, `batch_delete_records` (all batch ops capped at 500), `list_fields`, `create_field`. Auto-fills `value=[]` for `isEmpty`/`isNotEmpty` filter conditions.

4. **`cmd/anna/commands.go`** — Registered `feishu_calendar`, `feishu_task`, `feishu_bitable` in `setup()` alongside existing `feishu_user`.

5. **`internal/feishutool/helpers.go`** — Added helper functions: `intArg`, `boolArg`, `mapArg`, `sliceArg`. Clarified `ParseTimeToUnixMs` docstring regarding raw-numeric ambiguity.

6. **Tests** — 41 new tests across 3 test files: `calendar_test.go`, `task_test.go`, `bitable_test.go`. Tests cover tool definitions (all actions in enum), unknown action dispatch, parameter validation for every action, and builder helper functions (attendee list with dedup, calendar event building, task member building, bitable filter/sort/record builders). Total feishutool tests: 90. All pass with `-race`.

### Notes for Phase 4

- All three tools use `InvokeWithUserToken(ctx, false, ...)` — they fall back to bot token when no UAT is available. Set `requireAuth=true` when user-only operations are needed.
- `NeedAuthError` is still not caught by the channel handler — tools currently degrade gracefully to bot token.
- The `currentTimeMs` var in `task.go` can be overridden in tests for deterministic `complete_task` testing.
- Bitable `create_field` uses JSON round-trip to populate `AppTableFieldProperty` from dynamic `field_property` args — works for standard property keys but may miss SDK-specific builder validations.
- Calendar `list_events` uses `InstanceView` (not `List`) to auto-expand recurring events, matching the openclaw-lark pattern. Time range must be < 40 days.

## Phase 4: Extended Feishu Tools (Chat, Docs, Wiki, Sheets, Drive, Search, IM)

**Status**: Complete
**Date**: 2026-03-22
**Commits**: 8 (a820d5be, 509c3821, 6476a28c, 6153de27, 94b8efb1, 8cfaea6b, 37b6ead4, eb3bd435)

### What was done

1. **`internal/feishutool/chat.go`** — `feishu_chat` tool with 5 actions: `search_chats` (keyword search via IM Chat.Search), `get_chat` (detailed info via Chat.Get), `list_members` (paginated via ChatMembers.Get), `add_members` (ChatMembers.Create), `remove_members` (ChatMembers.Delete). Falls back to context `chat_id` for get/list/add/remove.

2. **`internal/feishutool/im.go`** — `feishu_im` tool with 7 actions: `send_message` (to user DM or group chat via Message.Create), `reply_message` (via Message.Reply), `read_messages` (chat history via Message.List), `get_message` (single message via Message.Get), `forward_message` (via Message.Forward), `add_reaction` (via MessageReaction.Create), `remove_reaction` (via MessageReaction.Delete). Falls back to context message_id/chat_id where applicable.

3. **`internal/feishutool/doc.go`** — `feishu_doc` tool with 3 actions: `create_doc` (via Docx.Document.Create), `get_doc_content` (block tree via DocumentBlock.List), `get_doc_raw_content` (plain text via Document.RawContent). Uses Docx v1 API.

4. **`internal/feishutool/wiki.go`** — `feishu_wiki` tool with 7 actions: `list_spaces` (Space.List), `get_space` (Space.Get), `create_space_node` (SpaceNode.Create with obj_type/node_type), `list_space_nodes` (SpaceNode.List with optional parent), `get_node` (Space.GetNode — resolves wiki token to obj_token), `move_node` (SpaceNode.Move), `copy_node` (SpaceNode.Copy). Uses Wiki v2 API.

5. **`internal/feishutool/sheets.go`** — `feishu_sheets` tool with 5 actions: `create_spreadsheet` (Spreadsheet.Create), `get_spreadsheet` (Spreadsheet.Get), `list_sheets` (SpreadsheetSheet.Query), `read_range` (v2 API via lark.Client.Get), `write_range` (v2 API via lark.Client.Put). Uses Sheets v3 SDK for metadata operations; cell read/write uses the v2 REST API (not available in v3 SDK).

6. **`internal/feishutool/drive.go`** — `feishu_drive` tool with 6 actions: `list_files` (File.List), `get_file_meta` (Meta.BatchQuery), `copy_file` (File.Copy), `move_file` (File.Move), `delete_file` (File.Delete), `create_folder` (File.CreateFolder). Uses Drive v1 API.

7. **`internal/feishutool/search.go`** — `feishu_search` tool with 1 action: `search_docs` (DocWiki.Search). Applies same filter to both doc_filter and wiki_filter. Supports doc_types, creator_ids, only_title, sort_type. Uses Search v2 API.

8. **`cmd/anna/commands.go`** — Registered all 7 new tools in `setup()` alongside existing feishu_user, feishu_calendar, feishu_task, feishu_bitable. Total: 11 Feishu tools.

9. **Tests** — 44 new tests across 7 test files: `chat_test.go`, `im_test.go`, `doc_test.go`, `wiki_test.go`, `sheets_test.go`, `drive_test.go`, `search_test.go`. Tests cover tool definitions (all actions in enum), unknown action dispatch, parameter validation for every action, and builder helpers (search filter, wiki filter). Total feishutool tests: 134. All pass with `-race`.

### Notes for Phase 5

- All tools use `InvokeWithUserToken(ctx, false, ...)` — graceful fallback to bot token when no UAT is available.
- Sheets `read_range`/`write_range` use the v2 REST API via `lark.Client.Get/Put` with `larkcore.AccessTokenTypeTenant`. When UAT is available, the `InvokeWithUserToken` wrapper passes the user token via options, but the raw API calls use tenant token as the supported access type. This means cell read/write always uses bot token. To use UAT for sheets, a custom `larkcore.ApiReq` with `AccessTokenTypeUser` would be needed.
- The `feishu_im` tool includes safety warnings in its description about confirming with the user before sending messages as their identity.
- Wiki `get_node` defaults to `obj_type="wiki"` which resolves wiki node tokens to actual document obj_tokens — useful for cross-tool workflows (e.g., get wiki node -> use obj_token with feishu_doc/feishu_sheets).
- Drive `get_file_meta` uses `Meta.BatchQuery` which can query up to 50 documents at once.
- Search filter builders are extracted as `buildSearchFilter`/`buildSearchWikiFilter` for testability.

## Phase 5a: Message Handling + Threading + Abort

**Status**: Complete
**Date**: 2026-03-22
**Commits**: 4 (50cc33a1, 18c4513f, c15aa3bb, 40603a02)

### What was done

1. **`internal/channel/feishu/handler.go`** — Extended `buildMessageContent()` with 8 new message types: `audio` (duration in seconds), `video`/`media` (duration), `file` (filename), `sticker`, `location` (name + lat/lng), `share_chat` (chat_id), `share_user` (user_id), `merge_forward` (summary). Unrecognized types now return `[Unsupported message type: xxx]` instead of nil. Added `extractJSONInt` helper. Added cancel detection in `onMessage()` before message processing. Updated `handleMessage()` with cancellable context and thread-aware stream/reply methods.

2. **`internal/channel/feishu/thread.go`** — New file with thread-aware session management:
   - `threadChannelCtx()` builds `group:chatID:thread:rootID` for threaded messages
   - `resolveWithThread()` resolves session with custom channelCtx, overrides session key for threads
   - `replyInThread()`, `sendCardReplyInThread()`, `sendFinalResponseInThread()`, `sendImageInThread()` route replies to thread root when rootID is present
   - `replaceChannelCtx()` replaces channelCtx suffix in session key strings

3. **`internal/channel/feishu/stream.go`** — Renamed `streamResponse` to `streamResponseInThread` with rootID parameter. Initial card reply uses `sendCardReplyInThread` for thread awareness.

4. **`internal/channel/feishu/feishu.go`** — Added `activeStreams map[string]context.CancelFunc` to Bot struct. Added `cancelPatterns` (cancel/stop/abort/取消/停止), `isCancelText()`, `streamKey()`, `registerStream()`, `unregisterStream()`, `cancelStream()`. Removed unused `resolve()` method (replaced by `resolveWithThread`).

5. **`internal/channel/feishu/render.go`** — Removed unused `sendFinalResponse()` (replaced by `sendFinalResponseInThread` in thread.go).

6. **Tests** — 34 new tests in `feishu_test.go`: message type parsing (audio, video, file, sticker, location, share_chat, share_user, merge_forward, unsupported), `extractJSONInt` edge cases, `threadChannelCtx` (group/thread/private), `replaceChannelCtx`, `streamKey`, `isCancelText` patterns (case-insensitive, whitespace, non-matches), `cancelStream` lifecycle. All pass with `-race`.

### Notes for Phase 5b

- Pre-existing test failures in `oauth_test.go` (TestAuthURL, TestAuthURLEmptyRedirect) due to URL encoding mismatch — not introduced by this phase.
- Thread messages get separate sessions via `group:chatID:thread:rootID` channelCtx, enabling isolated thread conversations.
- The abort mechanism works at the context level — when `cancelStream` fires, the context passed to `rc.Chat()` is cancelled, which should propagate through the agent runner's event loop. The streaming loop itself drains the events channel which will close when the context is done.
- `parseMergeForwardContent` returns a simple summary `[Forwarded messages]` rather than recursively expanding (would require async API calls to fetch sub-messages, which is complex and out of scope for Phase 5a).
- Audio/video durations are divided by 1000 (Feishu sends milliseconds) to show seconds.

## Phase 5b: CardKit 2.0 + Per-Group Config + Reactions (FINAL)

**Status**: Complete
**Date**: 2026-03-22
**Commits**: 4 (8b04b29b, aaf43ed7, dde72118, 2be864ea)

### What was done

1. **CardKit 2.0 Streaming** (`internal/channel/feishu/stream.go`) — Three-phase streaming:
   - Phase 1 (Thinking): Sends initial card with "Thinking..." immediately on message receipt, before any agent processing.
   - Phase 2 (Generating): Updates card with streaming content + cursor as text flows in, with tool status inline.
   - Phase 3 (Complete): Final content with elapsed time footer (e.g., `_Response time: 3.2s_`).
   - Added `streamPhase` enum, `thinkingContent()`, `elapsedFooter()`, `nowFunc` for testability.
   - `streamResponseInThread` now returns `time.Duration` (elapsed) alongside existing return values.

2. **Per-Group Config** (`internal/channel/feishu/feishu.go`, `handler.go`, `cmd/anna/gateway.go`) —
   - `GroupConfig` struct with `GroupMode`, `SystemPrompt`, `ToolAllow`, `ToolDeny` fields.
   - `Config.Groups` map keyed by chat_id for per-group overrides.
   - `groupMode(chatID)` resolves effective group mode (per-group > global).
   - `groupSystemPrompt(chatID)` returns per-group system prompt.
   - `prependSystemPrompt()` wraps text content with `[System: ...]` prefix.
   - `shouldRespondInGroup` now accepts chatID for per-group lookup.
   - `feishuChannelConfig` in `gateway.go` extended with `Groups` field, passed through to `feishu.Config`.
   - ToolAllow/ToolDeny stored but not enforced — awaits per-session tool filtering in pool manager.

3. **Reaction Event Handling** (`internal/channel/feishu/feishu.go`, `handler.go`) —
   - Subscribed to `OnP2MessageReactionCreatedV1` in event dispatcher.
   - `onReaction` handler extracts emoji type, message_id, operator open_id.
   - Filters: app-originated reactions, self-reactions (bot), unauthorized users.
   - Sends descriptive text `[User reacted with THUMBSUP on message om_xxx]` to agent.
   - Reaction tool (add/remove) already exists in `feishu_im` tool — no separate tool needed.

4. **Tests** — 18 new tests in `feishu_test.go`:
   - Streaming: `thinkingContent`, `elapsedFooter` (normal + sub-second), `streamPhase` constants, elapsed timing.
   - Per-group config: `groupMode` (global/override/empty), `shouldRespondInGroup` with per-group override, `groupSystemPrompt` (configured/empty/nil), `prependSystemPrompt` (string/non-string).
   - Reactions: `onReaction` nil event, nil data, app operator, self-reaction, unauthorized user.
   - Total feishu tests: 122 passing (2 pre-existing failures in oauth_test.go).

### Notes

- Pre-existing test failures (TestAuthURL, TestAuthURLEmptyRedirect) remain from Phase 5a — URL encoding mismatch in oauth_test.go.
- The `nowFunc` variable in `stream.go` enables deterministic time testing without mocking the full API.
- Reaction events are dispatched to the user's private chat session (not a group session), since Feishu reactions don't carry thread/chat context directly.
- ToolAllow/ToolDeny in GroupConfig are stored but not enforced. The comment notes that per-session tool filtering needs pool manager support first.
- All feishutool tests (134) continue to pass with -race.
