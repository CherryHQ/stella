# Tasks: WeChat (Weixin) iLink Bot Channel

## Phase 1: Protocol Types & HTTP Client

- [x] 1.1 — Create protocol types (`model.go`)
- [x] 1.2 — Create HTTP client with all iLink API endpoints (`client.go`)
- [x] 1.3 — Create AES-128-ECB crypto and CDN upload/download (`media.go`)
- [x] 1.4 — Unit tests for client, crypto, key format parsing (`weixin_test.go`)

## Phase 2: Bot Core & Message Loop

- [x] 2.1 — Create Bot struct, Config, New(), BotOption, WithAuth(), isAllowed() (`weixin.go`)
- [x] 2.2 — Implement Start() with getupdates loop, cursor persistence, retry/backoff, -14 handling (`weixin.go`)
- [x] 2.3 — Implement Notify() with context_token requirement (`weixin.go`)
- [x] 2.4 — Implement resolve() via channel.Resolve() (`weixin.go`)

## Phase 3: Message Handling & Streaming

- [x] 3.1 — Create handler with message filtering, command routing, /model, /agent (`handler.go`)
- [x] 3.2 — Create streaming with typing indicator via sendtyping (`stream.go`)
- [x] 3.3 — Create response rendering, 2000-char splitting, sendImage/sendFile/sendVideo (`render.go`)

## Phase 4: Gateway Integration & Admin Panel

- [x] 4.1 — Add weixinChannelConfig and init block in gateway.go (`gateway.go`)
- [x] 4.2 — Add QR login API endpoints (`weixin_qr.go`, `server.go`)
- [x] 4.3 — Add weixin config form with QR login UI (`channels.templ`, `channels.js`)
- [x] 4.4 — Add "weixin" to profile link code platform list (`profile.templ`, `profile.js`)
- [x] 4.5 — Run generate, format, lint and fix issues

## Phase 5: Tests & Documentation

- [x] 5.1 — Add comprehensive tests (`weixin_test.go`)
- [x] 5.2 — Update docs (docs/, README.md)
- [x] 5.3 — Update builtin anna skill (`internal/agent/runner/builtin/anna/`)
