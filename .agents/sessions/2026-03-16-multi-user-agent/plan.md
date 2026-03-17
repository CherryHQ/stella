# Plan: Multi-User × Multi-Agent Support

## Overview

Replace anna's single-user/single-agent architecture with N×N multi-user × multi-agent support. Migrate all configuration from YAML files to normalized SQLite tables. Add a web admin panel for managing the entire system.

### Goals

- Support multiple agents, each with dedicated workspace (skills, model)
- Support multiple users, each remembered independently per agent via `user_agent_memory`
- Move all config from `config.yaml` to normalized DB tables
- `anna onboard` bootstraps the system (creates DB, opens admin UI)
- Web admin panel for full CRUD of providers, agents, channels, users, scheduler
- Single bot per channel platform; users select agent via `/agent` command
- Single SQLite database for all data (`anna.db`)

### Success Criteria

- [ ] `anna onboard` creates `ANNA_HOME/anna.db`, runs migrations, opens admin panel
- [ ] Admin panel can CRUD providers, agents, channels, view users/sessions, manage scheduler jobs
- [ ] All config reads come from DB (no more `config.yaml` or `state.yaml`)
- [ ] Single Telegram bot serves all agents; users switch via `/agent` command
- [ ] DMs use per-user default agent; groups use per-group agent
- [ ] Sessions scoped to (agent_id, user_id, channel_context)
- [ ] Each agent has isolated workspace dir with own skills
- [ ] Per-user-per-agent memory (`user_agent_memory`) replaces global `SOUL.md`/`USER.md`
- [ ] QQ/Feishu still work with the default agent
- [ ] CLI chat works with the new config system (with `--agent` flag)
- [ ] Env vars `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` still serve as fallbacks

### Out of Scope

- QQ/Feishu multi-agent (only default agent for now)
- Cross-agent communication
- Auth/RBAC on web UI
- Multiple bots per group (future extension — current design doesn't block this)

## Technical Approach

### Architecture

```
ANNA_HOME (~/.anna/)
├── anna.db                    # Single SQLite: config + runtime data
└── workspaces/
    ├── {agent-slug}/          # Per-agent workspace
    │   ├── skills/
    │   └── anna.log
    └── ...
```

```
┌─────────────────────────────────────────────────────┐
│                    Single Process                     │
│                                                       │
│  PoolManager                                          │
│  ├─ Agent "anna"  → Pool (sessions for anna)         │
│  ├─ Agent "coder" → Pool (sessions for coder)        │
│  └─ Agent "writer"→ Pool (sessions for writer)       │
│                                                       │
│  Channels (single bot per platform)                   │
│  ├─ Telegram Bot ─→ resolve agent ─→ PoolManager.Get │
│  ├─ QQ Bot       ─→ default agent ─→ PoolManager.Get │
│  └─ Feishu Bot   ─→ default agent ─→ PoolManager.Get │
│                                                       │
│  Agent Routing:                                       │
│  ├─ DM:    users.default_agent_id                    │
│  ├─ Group: chat_agents(platform, chat_id)            │
│  └─ /agent command: update user or group preference  │
│                                                       │
│  System Prompt Assembly:                              │
│  ├─ agents.system_prompt       (agent soul, admin)   │
│  └─ + user_agent_memory.content (per-user, by agent) │
│                                                       │
│  Admin Server (HTTP) ←→ DB (anna.db)                 │
│                                                       │
│  DB Tables:                                           │
│  providers | agents | channels | users | chat_agents │
│  user_agent_memory | settings | conversations        │
│  messages | summaries | scheduler_jobs                │
└─────────────────────────────────────────────────────┘
```

### Agent Routing

Single bot per platform. Agent selection via `/agent` command:

| Context | `/agent anna` | Message arrives |
|---------|---------------|-----------------|
| **DM** | Updates `users.default_agent_id` | Look up `users.default_agent_id` → route to that agent's Pool |
| **Group** | Updates `chat_agents(platform, chat_id)` | Look up `chat_agents` → route to that agent's Pool |
| **No agent set** | — | Fall back to first enabled agent |

The `/agent` command also lists available agents when called without arguments.

**Future extension:** To support multiple agents in the same group via multiple bots, add per-agent bot tokens. The current single-bot design doesn't block this.

### System Prompt & Per-User Memory

Replaces the old `SOUL.md` / `USER.md` file-based system.

**Old (broken for multi-user):**
- `SOUL.md` — agent personality, file in workspace, agent can edit → shared across all users
- `USER.md` — user info, file in workspace, agent can edit → shared across all users

**New:**
- `agents.system_prompt` — agent's soul/personality, stored in DB, admin edits via web UI. This IS the agent's identity (what `SOUL.md` was).
- `user_agent_memory` — per-user-per-agent notes, stored in DB, agent reads/writes during chat. This IS user-specific context (what `USER.md` was, but scoped per user).

**System prompt assembly at chat time (3 layers):**
```
1. Basic system prompt      -- embedded system.md, overridden by SYSTEM.md in workspace
2. Agent soul prompt        -- agents.system_prompt from DB, overridden by SOUL.md in workspace
3. User memory              -- user_agent_memory.content, always present, write-only tool updates
4. + skills prompt          -- agent's skills
5. + project context        -- AGENTS.md files (CLI only)
```

User memory is injected per-session when the runner is created. The agent always has user context from message #1.

**Memory tool:**
- `user_memory` tool is write-only — user memory is always in the system prompt, no need to read
- Scoped to `(user_id, agent_id)` at tool construction time
- Recommended structure: `## User Preferences`, `## About the User`, `## Notes`
- Keeps high-level impressions, not every detail — like how a person remembers someone they know

### Key Design Decisions

1. **Single DB file** — `anna.db` replaces both `config.yaml` and `memory.db`. The memory engine is refactored to accept a `*sql.DB` instead of opening its own.
2. **No backward compat** — clean break. Old `config.yaml` / `state.yaml` / `memory.db` are ignored. No auto-migration from old format.
3. **Env var fallbacks** — `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` remain as fallbacks when the DB provider row has empty `api_key`. `ANNA_HOME` env var sets the home directory.
4. **All identity in DB** — Agent soul in `agents.system_prompt`. Per-user memory in `user_agent_memory`. No more `SOUL.md` / `USER.md` / `identity.md` files.
5. **Plugin config** — Stored in the `settings` table under key `"plugins"` as a JSON array (same structure as current YAML `plugins` list). Plugins remain global, not per-agent.
6. **Provider slugs allow duplicates by design** — Users can create `openai-prod` and `openai-dev` as separate providers with different base_urls. The slug is user-chosen.
7. **Single provider per agent** — An agent's `model_strong` and `model_fast` must use the same provider. This simplifies the schema. Document this limitation.
8. **Single bot per platform** — One Telegram bot serves all agents. Users switch agents via `/agent` command. Simpler deployment, one token per platform.
9. **Models cache** — Stored in `settings` table under key `"models_cache"` instead of a file.

### Database Schema (normalized)

**New tables:**

```sql
-- Global settings (key-value with JSON values)
CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL DEFAULT '{}',
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
-- Keys: "runner", "compaction", "heartbeat", "plugins", "models_cache"

-- LLM API providers
CREATE TABLE providers (
    id         TEXT PRIMARY KEY,         -- user-chosen slug: "anthropic", "openai-prod"
    name       TEXT NOT NULL,            -- display name
    api_key    TEXT NOT NULL DEFAULT '',
    base_url   TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Agent definitions
CREATE TABLE agents (
    id            TEXT PRIMARY KEY,     -- slug: "anna", "coder"
    name          TEXT NOT NULL,        -- display name
    provider_id   TEXT NOT NULL REFERENCES providers(id),
    model         TEXT NOT NULL DEFAULT '',
    model_strong  TEXT NOT NULL DEFAULT '',  -- same provider
    model_fast    TEXT NOT NULL DEFAULT '',  -- same provider
    system_prompt TEXT NOT NULL DEFAULT '',  -- agent's soul/personality (replaces SOUL.md)
    workspace     TEXT NOT NULL,        -- absolute path to workspace dir
    enabled       INTEGER NOT NULL DEFAULT 1,
    created_at    TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Global channel configurations (one row per platform)
CREATE TABLE channels (
    id         TEXT PRIMARY KEY,        -- "telegram", "qq", "feishu"
    enabled    INTEGER NOT NULL DEFAULT 1,
    config     TEXT NOT NULL DEFAULT '{}',  -- platform-specific JSON
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
-- telegram config: {"token":"...","notify_chat":"...","channel_id":"...","group_mode":"mention","allowed_ids":[123],"enable_notify":true}

-- Users (auto-created from platform identity)
CREATE TABLE users (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    external_id      TEXT NOT NULL,          -- platform user ID (string)
    platform         TEXT NOT NULL,          -- "telegram", "qq", "feishu", "cli"
    name             TEXT NOT NULL DEFAULT '',
    default_agent_id TEXT REFERENCES agents(id),  -- active agent for DMs
    created_at       TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at       TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(external_id, platform)
);

-- Per-group agent assignment
CREATE TABLE chat_agents (
    platform   TEXT NOT NULL,           -- "telegram", "qq", "feishu"
    chat_id    TEXT NOT NULL,           -- group/channel ID on the platform
    agent_id   TEXT NOT NULL REFERENCES agents(id),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY(platform, chat_id)
);

-- Per-user-per-agent memory (replaces USER.md)
-- Agent reads this into system prompt; agent writes via user_memory tool
CREATE TABLE user_agent_memory (
    user_id    INTEGER NOT NULL REFERENCES users(id),
    agent_id   TEXT NOT NULL REFERENCES agents(id),
    content    TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY(user_id, agent_id)
);
```

**Modified existing tables:**

```sql
-- conversations: add agent_id + user_id (NULLable for migration safety)
ALTER TABLE conversations ADD COLUMN agent_id TEXT REFERENCES agents(id);
ALTER TABLE conversations ADD COLUMN user_id INTEGER REFERENCES users(id);

-- scheduler_jobs: add agent_id + user_id
ALTER TABLE scheduler_jobs ADD COLUMN agent_id TEXT REFERENCES agents(id);
ALTER TABLE scheduler_jobs ADD COLUMN user_id INTEGER REFERENCES users(id);
```

Note: Since this is a clean break (no backward compat), existing rows in `conversations` and `scheduler_jobs` will have NULL `agent_id`/`user_id`. New code always populates these fields. Old NULL rows are effectively orphaned legacy data.

### Session Key Format

```
{agent_id}:{platform}:{external_user_id}:{channel_context}

Examples:
  anna:tg:123456:private           -- DM with Anna
  anna:tg:123456:group:-987654     -- User in group, talking to Anna
  coder:tg:123456:private          -- DM with Coder
```

The memory engine treats session_id as an opaque string — no parsing needed there. The key format is only constructed/parsed in the channel/pool layer.

### Components

- **`internal/config/store.go`** — New `Store` type: reads/writes config from DB. Exposes typed getters (providers, agents, channels, settings).
- **`internal/agent/pool_manager.go`** — New: maps agent_id → Pool. Reads agents from Store.
- **`internal/admin/`** — New package: HTTP API server + embedded SPA.
- **`internal/channel/identity.go`** — New: user resolution + agent routing (upsert user, look up active agent).
- **`internal/memory/usermemory.go`** — New: read/write `user_agent_memory` for a (user, agent) pair.
- **`internal/memory/tool/usermemory.go`** — New: `user_memory` tool for agent to read/write per-user notes.
- **`cmd/anna/onboard.go`** — Rewritten: bootstrap + admin server.

## Implementation Phases

### Phase 1: DB Consolidation & New Schema

Merge into single `anna.db`. Add new tables. Refactor memory engine to accept shared DB.

1. Refactor `internal/db/database.go` — `OpenDB` remains, but add `NewFromDB(*sql.DB)` pattern for the sqlc queries layer so multiple packages can share one connection (files: `internal/db/database.go`)
2. Refactor `internal/memory/engine.go` — add `NewEngineFromDB(db *sql.DB, ...)` constructor that accepts an existing `*sql.DB` instead of a path. Keep `NewEngine(path, ...)` as a convenience wrapper (files: `internal/memory/engine.go`)
3. Add new schema files: `settings.sql`, `providers.sql`, `agents.sql`, `channels.sql`, `users.sql`, `chat_agents.sql`, `user_agent_memory.sql` (files: `internal/db/schemas/tables/`)
4. Modify `conversations.sql` — add `agent_id TEXT` and `user_id INTEGER` columns (files: `internal/db/schemas/tables/conversations.sql`)
5. Modify `scheduler_jobs.sql` — add `agent_id TEXT` and `user_id INTEGER` columns (files: `internal/db/schemas/tables/scheduler_jobs.sql`)
6. Update `main.sql` imports, generate Atlas migration, run sqlc codegen (files: `internal/db/schemas/main.sql`, `internal/db/migrations/`)
7. Add sqlc queries for all new tables: CRUD for providers, agents, channels, users, chat_agents, user_agent_memory, settings (files: `internal/db/queries/`)

### Phase 2: Config Store & Bootstrap

New DB-backed config layer. `anna onboard` bootstrap.

1. Define `Store` interface — typed read/write methods: `ListProviders()`, `GetAgent(id)`, `UpsertProvider(...)`, `GetChannel(id)`, `GetSetting(key)`, `SetSetting(key, value)`, `GetUserAgent(userID)`, `SetUserAgent(userID, agentID)`, `GetChatAgent(platform, chatID)`, `SetChatAgent(platform, chatID, agentID)`, etc. (files: `internal/config/store.go`)
2. Implement `DBStore` — DB-backed implementation using sqlc queries. Includes env var fallbacks for provider API keys (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`) (files: `internal/config/dbstore.go`)
3. Add config defaults seeding — `SeedDefaults()` method: if no providers exist, create "anthropic" provider; if no agents exist, create default "anna" agent with default workspace and default soul in `system_prompt` (files: `internal/config/dbstore.go`)
4. Rewrite `anna onboard` — create `ANNA_HOME` dir, open `anna.db` via `OpenDB`, seed defaults, start admin web server (files: `cmd/anna/onboard.go`)
5. Add `Config` snapshot type — a read-only struct assembled from DB that downstream code can use (replaces old `*config.Config`). Includes helper to resolve effective model IDs per agent (files: `internal/config/config.go`)
6. Remove old config loading — delete YAML loading, `state.yaml`, `LoadFrom()`, env var parsing via `caarlos0/env`. Keep `AnnaHome()` and `ANNA_HOME` env var. Update `paths.go` (files: `internal/config/config.go`, `internal/config/state.go`, `internal/config/paths.go`, `internal/config/model.go`)

### Phase 3: Multi-Agent Core

Per-agent Pool, workspace isolation, PoolManager.

1. Create per-agent workspace setup — `SetupWorkspace(agentID, basePath)` ensures `workspaces/{agent_id}/` dir with `skills/` subdir (files: `internal/agent/workspace.go`)
2. Create `PoolManager` — manages `map[agentID]*Pool`. Methods: `Get(agentID)`, `StartAll(ctx)`, `Close()`. Reads enabled agents from config Store, creates one Pool per agent (files: `internal/agent/pool_manager.go`)
3. Update `Pool` to store `agentID` — pool knows which agent it belongs to; used for session creation and logging (files: `internal/agent/pool.go`)
4. Create per-agent runner factory — `NewAgentRunnerFactory(agentCfg, providerCfg, workspace, extraTools, pluginHooks)` returns a `runner.NewRunnerFunc` scoped to one agent's provider/model/system_prompt (files: `internal/agent/factory.go`)
5. Update skills tool to be per-agent — `skills.NewTool` takes agent workspace path instead of global workspace (files: `internal/skills/`)
6. Update `setup()` in commands.go — create single `*sql.DB`, pass to config Store and memory engine, build PoolManager from agents in DB (files: `cmd/anna/commands.go`)

### Phase 4: Multi-User, Agent Routing & User Memory

User resolution, agent routing, session scoping, `/agent` command, per-user memory tool.

1. Add user resolution — `ResolveUser(ctx, db, externalID, platform, name)` upserts into `users` table, returns user record (files: `internal/channel/identity.go`)
2. Add agent routing — `ResolveAgent(ctx, store, user, chatContext)` looks up `users.default_agent_id` for DMs or `chat_agents` for groups; falls back to first enabled agent (files: `internal/channel/identity.go`)
3. Update session key construction — new helper `BuildSessionKey(agentID, platform, externalUserID, channelContext)` used by channels when calling Pool (files: `internal/agent/session.go`)
4. Update `Pool.CreateSession` / `ResolveSession` — store `agent_id` and `user_id` in conversation record via memory engine (files: `internal/agent/pool.go`)
5. Update memory engine — `Ingest`, `SaveInfo` pass through `agent_id` and `user_id` to conversation inserts/updates (files: `internal/memory/engine.go`)
6. Add `user_agent_memory` read/write layer — `GetUserMemory(ctx, userID, agentID)`, `SetUserMemory(ctx, userID, agentID, content)` (files: `internal/memory/usermemory.go`)
7. Add `user_memory` tool — agent tool that reads/writes `user_agent_memory` for the current (user, agent) pair. Replaces the old `SOUL.md`/`USER.md` file editing (files: `internal/memory/tool/usermemory.go`)
8. Update system prompt builder — remove `SOUL.md`/`USER.md` file loading and `memories.md.tmpl`; system prompt = `agents.system_prompt` + `user_agent_memory.content` + skills + project context. Include instruction for agent to use `user_memory` tool (files: `internal/agent/runner/prompt.go`, remove `template/soul.md`, `template/user.md`, `template/memories.md.tmpl`)
9. Implement `/agent` command — in channel command handler: list available agents (no args), or set active agent for DM/group context. Updates `users.default_agent_id` or `chat_agents` row (files: `internal/channel/command.go`)
10. Refactor Telegram channel — single bot; handler resolves user, resolves agent for context, gets Pool from PoolManager, builds session key, chats (files: `internal/channel/telegram/telegram.go`, `internal/channel/telegram/handler.go`)
11. Update scheduler — jobs use `agent_id` to route to correct Pool via PoolManager; session key includes user context (files: `internal/scheduler/`)

### Phase 5: Web API & Admin UI

REST API for all entities + admin panel SPA.

1. Create `internal/admin/` package — HTTP server struct, middleware (CORS, JSON content-type), mount routes (files: `internal/admin/server.go`)
2. Implement provider APIs — `GET/POST /api/providers`, `GET/PUT/DELETE /api/providers/{id}`, `POST /api/providers/{id}/models` (fetch from upstream) (files: `internal/admin/providers.go`)
3. Implement agent APIs — `GET/POST /api/agents`, `GET/PUT/DELETE /api/agents/{id}` (files: `internal/admin/agents.go`)
4. Implement channel APIs — `GET/PUT /api/channels/{platform}` (files: `internal/admin/channels.go`)
5. Implement user, session, settings APIs — `GET /api/users`, `GET /api/sessions`, `GET/PUT /api/settings/{key}` (files: `internal/admin/users.go`, `internal/admin/sessions.go`, `internal/admin/settings.go`)
6. Implement scheduler job APIs — `GET/POST/PUT/DELETE /api/scheduler/jobs` with agent_id + user_id (files: `internal/admin/scheduler.go`)
7. Build admin SPA — extend existing `onboard.html` style (Alpine.js + Tailwind): tabs for Providers, Agents, Channels, Users, Sessions, Scheduler, Settings (files: `internal/admin/ui/index.html`)
8. Wire admin server into `anna onboard` and optionally `anna gateway` (admin panel available while gateway runs) (files: `cmd/anna/onboard.go`, `cmd/anna/gateway.go`)

### Phase 6: Integration & Cleanup

Wire everything together, update remaining subsystems, clean up.

1. Rewrite `gateway.go` — read agents + channels from DB via config Store, create PoolManager, start single bot per platform, QQ/Feishu on default agent only (files: `cmd/anna/gateway.go`)
2. Update CLI chat — add `--agent` flag (default: first enabled agent), create single Pool for selected agent (files: `cmd/anna/commands.go`, CLI chat code)
3. Update notification dispatcher — notifications still go through the single bot per platform (files: `internal/channel/dispatcher.go`)
4. Clean up dead code — remove old config types (`ChannelsConfig`, `TelegramConfig`, etc.), old `state.yaml` code, old `onboard.go` YAML-saving, old `channels.go`, old `SOUL.md`/`USER.md` defaults and templates (files: `internal/config/`, `cmd/anna/`, `internal/agent/runner/template/`)
5. Update documentation — README, docs, builtin anna skill references (files: `README.md`, `docs/`, `internal/agent/runner/builtin/anna/`)
6. Add tests — config Store round-trip, PoolManager lifecycle, admin API CRUD, session key construction, user resolution, agent routing, user_memory tool (files: `*_test.go`)

## Testing Strategy

### Unit Tests
- `internal/config/dbstore_test.go` — `SeedDefaults` populates empty DB; `ListProviders` returns seeded data; env var fallback for API keys works; `GetAgent` returns correct config
- `internal/agent/pool_manager_test.go` — `StartAll` creates pools for enabled agents only; `Get` returns nil for unknown agent
- `internal/agent/session_test.go` — `BuildSessionKey` produces correct format; round-trips parse correctly
- `internal/channel/identity_test.go` — `ResolveUser` creates new user; second call returns same ID; `ResolveAgent` returns user's default agent in DM; returns group agent in group; falls back to first enabled agent
- `internal/memory/usermemory_test.go` — `GetUserMemory` returns empty for new pair; `SetUserMemory` writes content; subsequent `Get` returns it; different user or agent returns different content
- `internal/memory/tool/usermemory_test.go` — tool reads/writes user_agent_memory correctly
- `internal/admin/` — API handler tests: create provider → create agent → configure channel → verify; CRUD round-trips

### Integration Tests
- Multi-agent setup: create 2 agents in DB, build PoolManager, verify 2 independent pools
- Session scoping: same user talking to 2 agents (via `/agent` switch) gets 2 separate sessions with independent history
- User memory isolation: user A's memory with agent X does not leak to user B's memory with agent X
- Agent routing: DM defaults, group defaults, `/agent` switch changes routing
- System prompt assembly: agent system_prompt + user_agent_memory.content correctly composed
- Config Store + memory engine sharing same `*sql.DB` — concurrent reads/writes don't deadlock

### Manual Tests
- `anna onboard` end-to-end: fresh `ANNA_HOME`, opens browser, configure provider + agent + channel
- Single Telegram bot, `/agent` to switch between agents, verify different personalities
- Tell agent "remember I prefer concise answers", switch to different user, verify the preference doesn't leak
- Group chat: set group agent, verify only that agent responds
- CLI `anna chat --agent coder` uses correct agent identity

## Risks

| Risk | Impact | Mitigation |
|------|--------|------------|
| Breaking change — no backward compat | High | `anna onboard` seeds defaults; document upgrade path clearly |
| Large blast radius — touches config, DB, channels, agent, memory | High | 6 phases with reviews between each |
| SQLite concurrent writes from multiple goroutines | Medium | WAL mode + `db.SetMaxOpenConns(1)` on the shared connection; single writer pattern |
| Web UI scope creep | Medium | Keep functional, not polished; Alpine.js + Tailwind (existing stack) |
| Memory engine refactor to share DB | Medium | Minimal change — just add `NewEngineFromDB(*sql.DB)` constructor |
| Agent routing edge cases (user without default, group without agent) | Low | Consistent fallback to first enabled agent |
| `user_agent_memory.content` grows unbounded | Low | Document recommended max size; agent can self-manage via tool |

## Assumptions

- `ANNA_HOME` defaults to `~/.anna/`, overridable via `ANNA_HOME` env var
- DB file is `anna.db` inside `ANNA_HOME`
- Agent workspaces live under `ANNA_HOME/workspaces/{agent_id}/` (skills + logs only)
- Platform-specific channel config stored as JSON in `channels.config` column
- CLI chat uses first enabled agent by default, overridable via `--agent` flag
- Old `config.yaml` / `state.yaml` / `memory.db` files are ignored (not auto-migrated)
- `model_strong` and `model_fast` on an agent must use the same provider as the agent's `provider_id`
- Agent soul = `agents.system_prompt` (admin-managed via web UI), overrideable by `SOUL.md` file in workspace
- Basic system prompt = embedded default, overrideable by `SYSTEM.md` file in workspace
- Per-user memory = `user_agent_memory.content` (agent-managed via write-only `user_memory` tool, always injected into system prompt)
- System prompt = basic + agent soul + user memory + skills + project context
- One bot per platform (single Telegram token, single QQ app, single Feishu app)
- Agent selection: per-user for DMs, per-group for group chats, via `/agent` command
- Fallback agent: first enabled agent in DB when no preference is set

## Review Feedback

### Round 1 (reviewer)

Issues addressed:
- **DB consolidation** — Added Phase 1 tasks 1-2 to refactor memory engine to accept shared `*sql.DB`
- **Env var fallbacks** — Preserved `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` in config Store
- **Phase 2 decomposition** — Split "rewrite config" into 6 separate tasks
- **Plugin config storage** — Stored in `settings` table under `"plugins"` key
- **CLI --agent flag** — Added to Phase 6 task 2
- **Provider slug uniqueness** — Documented as user-chosen, allows `openai-prod` / `openai-dev`
- **Single provider per agent** — Documented as intentional limitation
- **Existing data** — Clarified: NULL `agent_id`/`user_id` on old rows, clean break
- **Settings table** — Kept as JSON key-value for flexibility; Go structs handle validation on read/write
- **Phase ordering** — Moved multi-agent core (Phase 3) before admin UI (Phase 5)
- **SQLite concurrency** — Added `db.SetMaxOpenConns(1)` to risk mitigation
- **Models cache** — Moved to `settings` table

### Round 2 (design change — channel simplification)

- **Removed `agent_channels` table** — replaced with global `channels` table (one row per platform)
- **Single bot per platform** — one Telegram token serves all agents
- **Agent routing via `/agent` command** — per-user default in `users.default_agent_id`, per-group in `chat_agents` table
- **Added `chat_agents` table** — maps (platform, chat_id) → agent_id for group chats
- **Added `default_agent_id` to `users`** — stores per-user agent preference for DMs
- **Simplified gateway** — no multi-bot startup, one bot per channel type
- **Future-proof** — can extend to multi-bot per group later without schema redesign

### Round 3 (design change — user-agent memory)

- **Dropped `SOUL.md` / `USER.md` files** — replaced with DB-backed system
- **Agent soul → `agents.system_prompt`** — admin manages via web UI, seeded with default anna personality
- **User memory → `user_agent_memory` table** — per (user_id, agent_id) pair, agent reads/writes via `user_memory` tool
- **Removed `identity.md` from workspace** — workspace only has `skills/` and logs
- **Removed `memories.md.tmpl`** — system prompt template simplified
- **Added `user_memory` tool** — Phase 4 tasks 6-7, replaces file-based memory editing
- **Updated system prompt builder** — Phase 4 task 8, composes from DB fields instead of files

## Final Status

All 6 phases + post-phase prompt restructuring complete. Key outcomes:

- **Multi-agent**: N agents running simultaneously with isolated workspaces, models, and personalities
- **Multi-user**: Users auto-created from platform identity, each with per-agent memory
- **3-layer system prompt**: Basic (SYSTEM.md) → Agent soul (SOUL.md/DB) → User memory (DB, always injected)
- **Write-only user_memory tool**: Agent updates user memory, which appears in system prompt at next session start
- **Single DB**: All config + runtime data in `anna.db`
- **Admin panel**: Full CRUD for all entities via web UI
- **Agent routing**: DMs → user default, groups → chat_agents, fallback → first enabled
- **Session scoping**: `{agentID}:{platform}:{userID}:{context}` format

### Remaining work
- Wire `user_memory` tool into agent pools via `ExtraToolsFactory` (tool exists but not auto-injected)
- QQ/Feishu `/agent` command
- README.md and docs/ site updates
- Admin API auth/RBAC
