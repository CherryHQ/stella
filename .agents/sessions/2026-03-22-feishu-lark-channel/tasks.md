# Tasks: Feishu/Lark Channel Enhancement

## Phase 1: Tool Infrastructure + Client Wrapper

- [x] 1.1 — Create `feishutool/context.go`: context keys for open_id, chat_id, message_id (`internal/feishutool/context.go`)
- [x] 1.2 — Create `feishutool/client.go`: Lark client wrapper with bot token, rate limiter (`internal/feishutool/client.go`)
- [x] 1.3 — Create `feishutool/helpers.go`: time parsing, error formatting, pagination, JSON builders (`internal/feishutool/helpers.go`)
- [x] 1.4 — Create `feishutool/user.go`: `feishu_user` tool as reference implementation (`internal/feishutool/user.go`)
- [x] 1.5 — Wire tools into `setup()`: load feishu config, create client, append to sharedTools (`cmd/anna/commands.go`)
- [x] 1.6 — Inject feishu context in handler: open_id/chat_id/message_id into context before rc.Chat() (`internal/channel/feishu/handler.go`)
- [x] 1.7 — Tests for client wrapper, context, and user tool (`feishutool/client_test.go`, `feishutool/user_test.go`)

## Phase 2: UAT OAuth + Token Storage

- [x] 2.1 — Create DB schema for `feishu_tokens` table (`internal/db/schemas/tables/feishu_tokens.sql`)
- [x] 2.2 — Generate migration + regenerate sqlc (`internal/db/migrations/`, `internal/db/queries/`)
- [x] 2.3 — Create `feishutool/token_store.go`: TokenStore interface + SQLite impl with AES encryption (`internal/feishutool/token_store.go`)
- [x] 2.4 — Extend `feishutool/client.go` with UAT: InvokeAsUser(), auto-refresh, fallback (`internal/feishutool/client.go`)
- [x] 2.5 — Create `feishu/oauth.go`: Device Flow UI, auth card, polling, token storage (`internal/channel/feishu/oauth.go`)
- [x] 2.6 — Wire TokenStore into setup + gateway (`cmd/anna/commands.go`, `cmd/anna/gateway.go`)
- [x] 2.7 — Tests for token store and OAuth flow (`feishutool/token_store_test.go`, `feishu/oauth_test.go`)

## Phase 3: Core Feishu Tools (Calendar, Tasks, Bitable)

- [x] 3.1 — Create `feishutool/calendar.go`: events CRUD, attendees, freebusy (`internal/feishutool/calendar.go`)
- [x] 3.2 — Create `feishutool/task.go`: tasks, tasklists, subtasks (`internal/feishutool/task.go`)
- [x] 3.3 — Create `feishutool/bitable.go`: app/table/record/field CRUD, batch ops (`internal/feishutool/bitable.go`)
- [x] 3.4 — Register tools in setup() (`cmd/anna/commands.go`)
- [x] 3.5 — Tests for each tool (`feishutool/calendar_test.go`, `feishutool/task_test.go`, `feishutool/bitable_test.go`)

## Phase 4: Extended Feishu Tools (Chat, Docs, Wiki, Sheets, Drive, Search, IM)

- [x] 4.1 — Create `feishutool/chat.go`: search, info, members (`internal/feishutool/chat.go`)
- [x] 4.2 — Create `feishutool/im.go`: send/read messages, get resources (`internal/feishutool/im.go`)
- [x] 4.3 — Create `feishutool/doc.go`: document create/read/update (`internal/feishutool/doc.go`)
- [x] 4.4 — Create `feishutool/wiki.go`: spaces, nodes CRUD (`internal/feishutool/wiki.go`)
- [x] 4.5 — Create `feishutool/sheets.go`: spreadsheet operations (`internal/feishutool/sheets.go`)
- [x] 4.6 — Create `feishutool/drive.go`: file management (`internal/feishutool/drive.go`)
- [x] 4.7 — Create `feishutool/search.go`: global doc search (`internal/feishutool/search.go`)
- [x] 4.8 — Register tools in setup() (`cmd/anna/commands.go`)
- [x] 4.9 — Tests for each tool (`feishutool/*_test.go`)

## Phase 5a: Message Handling + Threading + Abort

- [x] 5a.1 — Extend message parsing: audio, video, file, sticker, location, share_chat, merge_forward (`internal/channel/feishu/handler.go`)
- [x] 5a.2 — Create thread.go: thread_id extraction, thread session keys, reply-in-thread (`internal/channel/feishu/thread.go`)
- [x] 5a.3 — Integrate threading into handler: onMessage + resolve + handleMessage (`internal/channel/feishu/handler.go`)
- [x] 5a.4 — Abort/cancel fast-path: activeStreams map, detect cancel text, context cancellation (`internal/channel/feishu/feishu.go`, `handler.go`, `stream.go`)
- [x] 5a.5 — Tests for message parsing, threading, abort (`feishu/feishu_test.go`)

## Phase 5b: CardKit 2.0 + Per-Group Config + Reactions

- [ ] 5b.1 — Upgrade streaming to CardKit 2.0: thinking/generating/complete phases, elapsed time footer (`internal/channel/feishu/stream.go`)
- [ ] 5b.2 — Per-group config: Groups map[string]GroupConfig, tool allow/deny, system prompt (`internal/channel/feishu/feishu.go`, `cmd/anna/gateway.go`)
- [ ] 5b.3 — Reaction event subscription + handler (`internal/channel/feishu/feishu.go`, `handler.go`)
- [ ] 5b.4 — Create `feishutool/reaction.go`: feishu_reaction tool (`internal/feishutool/reaction.go`)
- [ ] 5b.5 — Tests for streaming, per-group config, reactions (`feishu/stream_test.go`, `feishu/feishu_test.go`)
