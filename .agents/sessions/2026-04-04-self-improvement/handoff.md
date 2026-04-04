# Handoff

<!-- Append a new phase section after each phase completes. -->

## Phase 1: Skill Lifecycle — Complete

All 7 tasks implemented, tests passing with `-race`, lint clean.

### Commits (on `feat/self-improvement` branch)

1. `0d2b1f33` — Add Status + CreatedAt fields to Skill and NormalizeSkillStatus helper
2. `48862b5c` — Filter deprecated skills from prompt and add status tag
3. `c861229c` — Add AtomicWriteFile and skill create/patch/deprecate actions
4. `8d9979c4` — Extend skills tool with create, patch, deprecate actions
5. `22c53e8b` — Include status field in skills list output
6. `9cc23075` — Add skill lifecycle tests

### Files Changed

- `internal/agent/runner/skill.go` — Added `Status`, `CreatedAt` to `Skill` struct; `status`, `created-at` to `skillFrontmatter`; exported `NormalizeSkillStatus()` helper; `FormatSkillsForPrompt` now filters deprecated and includes `<status>` tag; status constants `SkillStatusDraft/Active/Deprecated`
- `internal/agent/runner/skill_test.go` — Tests for NormalizeSkillStatus, deprecated filtering, status tag inclusion, status loading from file, default status
- `internal/skills/atomicwrite.go` — **New**: `AtomicWriteFile` (temp+rename), `skillWriteMu` package-level mutex
- `internal/skills/manage.go` — **New**: `Create`, `Patch`, `Deprecate` functions with validation, frontmatter splitting/rendering
- `internal/skills/manage_test.go` — **New**: Tests for all CRUD operations, validation errors, atomic write
- `internal/skills/tool.go` — Added `create`, `patch`, `deprecate` to action enum, schema, routing, and description
- `internal/skills/list.go` — Added `Status` field to `installedSkill` struct

### Key Design Decisions

- `NormalizeSkillStatus` is exported so `internal/skills/manage.go` can use it from the `runner` package
- Skills with unknown status values default to "active" (backward compatible)
- `Create` always sets status=draft and created-at=now (RFC 3339)
- `Deprecate` is a thin wrapper around `Patch` with status=deprecated
- All writes go through `skillWriteMu` lock + `AtomicWriteFile` for safety
- Frontmatter is stored as `map[string]string` in manage.go for flexible patching

### Ready for Phase 2

Phase 2 (Database + Queries) can proceed. The skill lifecycle is fully functional — skills can be created as drafts, patched, deprecated, and the prompt system correctly filters deprecated skills.

## Phase 2: Database + Queries — Complete

All tasks implemented, tests passing with `-race`, lint clean.

### Commit

1. `9f4653cd` — Add self-improvement review tracking to database

### Changes

- **`internal/db/schemas/tables/ctx_conversations.sql`** — Added `self_improve_reviewed_at TEXT` nullable column
- **`internal/db/migrations/20260404052636_add-self-improve-reviewed-at.sql`** — **New**: Atlas-generated migration adding the column
- **`internal/db/migrations/atlas.sum`** — Updated checksum
- **`internal/db/queries/ctx_conversations.sql`** — Added `ListUnreviewedConversations` (finds conversations needing review: not archived, has agent+user, either never reviewed or has activity since last review) and `MarkConversationReviewed` (sets reviewed timestamp to now)
- **`internal/db/queries/ctx_messages.sql`** — Added `GetMessagesSince` (loads messages after a given timestamp for incremental review)
- **`internal/db/sqlc/`** — Regenerated Go code with new queries and `SelfImproveReviewedAt` field on model

### New Queries

| Query | Type | Purpose |
|-------|------|---------|
| `ListUnreviewedConversations` | `:many` | Finds conversations eligible for self-improvement review (archived=0, has agent_id and user_id, never reviewed or has new activity) |
| `MarkConversationReviewed` | `:exec` | Sets `self_improve_reviewed_at` to now |
| `GetMessagesSince` | `:many` | Loads messages created after a timestamp, ordered by seq |

### Ready for Phase 3

Phase 3 (Review Job) can proceed. The database layer is complete — the review job can query for unreviewed conversations, load their messages incrementally, and mark them as reviewed after processing.

## Phase 3: Review Engine — Complete

All tasks implemented, 17 new tests passing with `-race`, lint clean. No regressions in skills or runner packages.

### Commit

1. `97a13426` — Add review engine for self-improvement skill extraction

### Files Created

- **`internal/agent/selfimprove/reviewtool.go`** — `ReviewSkillsTool` restricted to create/patch/deprecate actions only; delegates to `skills.Create`, `skills.Patch`, `skills.Deprecate`; input schema with action enum, name, description, content, status fields
- **`internal/agent/selfimprove/prompts.go`** — `reviewSystemPrompt` const instructing the review agent to analyze conversations, extract procedural knowledge, use review_skills tool, respond "Nothing to save." when nothing worth extracting; includes `%s` placeholder for existing skill names
- **`internal/agent/selfimprove/reviewer.go`** — `Reviewer` struct with `NewReviewer` constructor and `Review` method; constructs local `engine.Engine`, uses `LoopConfig{MaxTurns: 5}`, no plugin hooks, no agent tool; `countSkillMutations` counts create/patch calls in result messages
- **`internal/agent/selfimprove/reviewtool_test.go`** — Tests for definition schema, create/patch/deprecate actions, unknown action error, missing name error (6 tests)
- **`internal/agent/selfimprove/reviewer_test.go`** — Tests for construction, existing skills in prompt, tool definition wiring, system prompt content, countSkillMutations with table-driven cases (11 tests)

### Key Design Decisions

- `ReviewSkillsTool` is separate from the full `SkillsTool` — it only exposes create/patch/deprecate, preventing the review agent from installing/removing/searching skills
- Tool errors are returned as text content (not Go errors) so the engine loop continues and the review agent can recover
- `countSkillMutations` only counts create and patch (not deprecate) since the caller wants to know how many skills were added/updated
- System prompt uses `fmt.Sprintf` with existing skill names to prevent duplicates
- No plugin hooks and no agent tool prevents re-entrancy and subagent spawning

### Ready for Phase 4

Phase 4 (Scheduled Job) can proceed. The review engine is complete — the scheduled job needs to: query unreviewed conversations, format their messages as transcript text, call `Reviewer.Review()`, and mark conversations as reviewed.

## Phase 4: Scheduled Task + Integration — Complete

All 6 sub-tasks implemented, tests passing with `-race`, lint clean. Full test suite passes.

### Commits (on `feat/self-improvement` branch)

1. `e15ccd50` — Add SelfImproveConfig type and wire into Snapshot
2. `78cc0327` — Add ReviewTask entry point and StartReviewLoop for self-improvement
3. `776095ea` — Add draft skill expiry logic
4. `c8c33bbc` — Wire self-improvement review loop into gateway startup
5. `cf7560dc` — Add draft skill promotion guidance to system prompt

### Files Changed

- **`internal/config/config.go`** — Added `SelfImproveConfig` type with `Enabled`, `Every`, `BatchSize` fields; `IsEnabled()`, `Interval()`, `Batch()` methods with sensible defaults
- **`internal/config/snapshot.go`** — Added `SelfImprove SelfImproveConfig` field to `Snapshot` struct
- **`internal/config/dbstore.go`** — Populate `SelfImprove` from `"self_improve"` DB setting key in `Snapshot()`
- **`internal/agent/selfimprove/review.go`** — **New**: `ReviewDeps` struct, `StartReviewLoop()` ticker-based goroutine, `ReviewTask()` entry point iterating agents/conversations, `buildConversationText()` with first-review and re-review (incremental with summary context) paths
- **`internal/agent/selfimprove/expiry.go`** — **New**: `ExpireDrafts()` scans workspace skills dirs and per-user skills dirs, deprecates drafts older than maxAge based on `created-at` frontmatter
- **`internal/agent/selfimprove/review_test.go`** — **New**: Tests for Config defaults, buildConversationText first review, empty conversation handling
- **`internal/agent/selfimprove/expiry_test.go`** — **New**: Tests for draft expiry (old/new drafts), empty workspace, nonexistent workspace
- **`internal/agent/selfimprove/testhelper_test.go`** — **New**: Shared `setupTestDB` helper
- **`cmd/anna/gateway.go`** — Added self-improvement review loop startup after heartbeat block, uses `ai.DefaultRegistry()` and shared `Dispatcher`
- **`internal/agent/runner/template/system.md`** — Added "Draft Skills" section guiding the assistant to suggest enabling draft skills

### Key Design Decisions

- Used ticker-based goroutine (not scheduler persistence) since the review loop is ephemeral and doesn't need crash recovery
- `ReviewDeps` receives `*channel.Dispatcher` directly (not `channel.Notifier` interface) to use `NotifyUser()` for per-user notifications without type assertions
- `buildConversationText` for re-reviews prepends summary XML context before new messages, giving the review agent prior conversation context
- `ExpireDrafts` defaults to 30-day TTL for draft skills, scanning both agent-level and per-user directories
- Review errors don't mark conversations as reviewed — they'll be retried on the next cycle
- Draft skill promotion is guided via system prompt text, letting the assistant suggest enabling drafts naturally

### Configuration

Enable via admin panel setting key `self_improve`:
```json
{"enabled": true, "every": "1h", "batch_size": 5}
```

### Ready for Phase 5

The self-improvement pipeline is fully wired: config -> scheduler -> review engine -> skill creation -> user notification -> draft expiry. Phase 5 (if planned) could add admin UI for reviewing drafts, metrics/dashboards, or skill quality scoring.
