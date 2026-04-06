# Tasks: Pluggable Memory System

## Phase 1: Interfaces, Types, and Test Infrastructure

- [ ] 1.1 — Create `pkg/memory/provider.go` with Provider + 6 capability interfaces (`pkg/memory/provider.go`)
- [ ] 1.2 — Create `pkg/memory/types.go` with all shared types (`pkg/memory/types.go`)
- [ ] 1.3 — Create `pkg/memory/summarize.go` — move Summarizer interface + LLMSummarizer from internal (`pkg/memory/summarize.go`)
- [ ] 1.4 — Create `pkg/memory/memorytest/fake.go` — in-memory Fake implementing all interfaces (`pkg/memory/memorytest/fake.go`)
- [ ] 1.5 — Create `pkg/memory/memorytest/conformance.go` — RunConformance test suite (`pkg/memory/memorytest/conformance.go`)
- [ ] 1.6 — Verify `go build ./pkg/memory/...` compiles

## Phase 2: Plugin Registry

- [ ] 2.1 — Create `plugins/memory/registry.go` with Register/Build/List (`plugins/memory/registry.go`)
- [ ] 2.2 — Add `PluginKindMemory` to config and built-in plugin list (`internal/config/plugin.go`)
- [ ] 2.3 — Seed `memory/lcm` in SeedDefaults (`internal/config/dbstore.go`)

## Phase 3: LCM Plugin

- [ ] 3.1 — Create `plugins/memory/lcm/plugin.go` — init() registration (`plugins/memory/lcm/plugin.go`)
- [ ] 3.2 — Create `plugins/memory/lcm/provider.go` — Provider core methods (`plugins/memory/lcm/provider.go`)
- [ ] 3.3 — Create `plugins/memory/lcm/engine.go` — internal helpers, message conversion (`plugins/memory/lcm/engine.go`)
- [ ] 3.4 — Create `plugins/memory/lcm/assembler.go` — context assembly (`plugins/memory/lcm/assembler.go`)
- [ ] 3.5 — Create `plugins/memory/lcm/compaction.go` — Compactor implementation (`plugins/memory/lcm/compaction.go`)
- [ ] 3.6 — Create `plugins/memory/lcm/retrieval.go` — Searcher + Explorer (`plugins/memory/lcm/retrieval.go`)
- [ ] 3.7 — Create `plugins/memory/lcm/profile.go` — ProfileStore (`plugins/memory/lcm/profile.go`)
- [ ] 3.8 — Create `plugins/memory/lcm/sessions.go` — SessionManager (`plugins/memory/lcm/sessions.go`)
- [ ] 3.9 — Create `plugins/memory/lcm/review.go` — ReviewSource (`plugins/memory/lcm/review.go`)
- [ ] 3.10 — Write LCM conformance test (`plugins/memory/lcm/provider_test.go`)
- [ ] 3.11 — Add blank import to plugins_imports.go (`cmd/anna/plugins_imports.go`)

## Phase 4: Tool Auto-Generation

- [ ] 4.1 — Create `pkg/memory/tool.go` — BuildTool with capability detection (`pkg/memory/tool.go`)
- [ ] 4.2 — Write tool tests (`pkg/memory/tool_test.go`)

## Phase 5: Wire Into Callers

- [ ] 5.1 — Update Pool struct and NewPool (`internal/agent/pool.go`)
- [ ] 5.2 — Update pool_chat.go — Append/Assemble/Session (`internal/agent/pool_chat.go`)
- [ ] 5.3 — Update pool_compaction.go — Compactor type assertion (`internal/agent/pool_compaction.go`)
- [ ] 5.4 — Update pool_runner.go — remove UserMemoryStore (`internal/agent/pool_runner.go`)
- [ ] 5.5 — Update PoolManager — Provider field (`internal/agent/pool_manager.go`)
- [ ] 5.6 — Update factory.go — pass Provider through (`internal/agent/factory.go`)
- [ ] 5.7 — Update prompt.go — ProfileStore type assertion (`internal/agent/runner/prompt.go`)
- [ ] 5.8 — Update selfimprove/review.go — ReviewSource (`internal/agent/selfimprove/review.go`)
- [ ] 5.9 — Update selfimprove/prompts.go — new action names (`internal/agent/selfimprove/prompts.go`)
- [ ] 5.10 — Update admin/server.go — type assertions (`internal/admin/server.go`)
- [ ] 5.11 — Update cmd/anna/commands.go — registry wiring (`cmd/anna/commands.go`)
- [ ] 5.12 — Update cmd/anna/gateway.go — ReviewDeps.Memory (`cmd/anna/gateway.go`)

## Phase 6: Simple Plugin

- [ ] 6.1 — Create `plugins/memory/simple/plugin.go` — init() registration (`plugins/memory/simple/plugin.go`)
- [ ] 6.2 — Create `plugins/memory/simple/provider.go` — Provider + ProfileStore + SessionManager (`plugins/memory/simple/provider.go`)
- [ ] 6.3 — Write simple conformance test (`plugins/memory/simple/provider_test.go`)
- [ ] 6.4 — Add blank import to plugins_imports.go (`cmd/anna/plugins_imports.go`)
- [ ] 6.5 — Seed `memory/simple` (disabled) in SeedDefaults (`internal/config/dbstore.go`)

## Phase 7: Delete Old Code and Update Docs

- [ ] 7.1 — Delete `internal/memory/` directory
- [ ] 7.2 — Delete `internal/agent/selfimprove/memorytool.go`
- [ ] 7.3 — Remove memory methods from config.Store interface and DBStore (`internal/config/store.go`, `internal/config/dbstore.go`)
- [ ] 7.4 — Update memory-system.md docs (`docs/content/docs/core/memory-system.md`)
- [ ] 7.5 — Update architecture.md docs (`docs/content/docs/core/architecture.md`)
- [ ] 7.6 — Run `mise run format && mise run lint && mise run test` — fix all issues
