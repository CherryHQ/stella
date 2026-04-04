# Plan: Self-Improvement System

## Overview

Anna learns from conversations autonomously — a scheduled job reviews recent conversations, extracts reusable procedures as draft skills, and updates user memory. Users are notified of new drafts and can enable them. Drafts auto-expire after 30 days if unused.

### Goals

- Enable anna to autonomously capture reusable patterns (skills) and user insights (memory) from conversations
- Non-blocking: all learning happens asynchronously via scheduled job
- Per-user, per-agent isolation: no cross-pollination between users or agents
- Cost-efficient: fast model, small batches, incremental message loading

### Success Criteria

- [ ] Agent can create/patch/deprecate skills via existing skills tool
- [ ] Skills have lifecycle: `draft` → `active` → `deprecated`
- [ ] Scheduled job reviews unreviewed conversations every hour (batch of 5)
- [ ] Re-reviews only load new messages (incremental via cursor)
- [ ] Users are notified of new draft skills and can enable them
- [ ] Draft skills auto-expire after 30 days
- [ ] All tests pass with `-race`, lint clean

### Out of Scope

- Usage telemetry / automatic promotion based on usage counts
- Cross-user shared learning ("share to agent" promotion)
- Smarter scheduling (per-user frequency tuning)
- Admin-driven cross-user pattern promotion

## Technical Approach

Reuse existing systems: scheduler + heartbeat pattern for the review job, skills tool for mutations, notifier for user communication, fast model tier for cheap reviews.

### Key Design Decisions

- **Scheduled job, not real-time**: hourly batches of 5 conversations instead of per-turn event collection. Simpler, cheaper, matches existing heartbeat pattern.
- **Direct SQL for incremental loading**: `memory.Engine.Assemble()` doesn't support cursor-based loading. Use direct `GetMessagesSince` query with `self_improve_reviewed_at` as cursor.
- **Restricted internal tool**: review agent gets `ReviewSkillsTool` (create/patch/deprecate only), not the full public skills tool (which has install/remove/search).
- **Per-user skills only**: background review writes to `workspaces/{agentID}/users/{userID}/.agents/skills/`, never agent-level. Prevents cross-user pollution.
- **Fast model**: review agent uses `ModelTierFast` via `snapshot.ResolveModelTier()`.

### Multi-Agent, Multi-User Scoping

```
Agent "coder" + User A → reviews → User A's private drafts for "coder"
Agent "coder" + User B → reviews → User B's private drafts for "coder"
Agent "writer" + User A → reviews → User A's private drafts for "writer"
```

- Skills: per-user, per-agent (`workspaces/{agentID}/users/{userID}/.agents/skills/`)
- Memory: per-user, per-agent (`ctx_agent_memory` keyed by `(user_id, agent_id)`)
- Skip legacy: conversations with `agent_id=""` or `user_id=0` are skipped

### Components

- **Skill lifecycle** (`internal/agent/runner/skill.go`, `internal/skills/`): `status` + `created-at` frontmatter fields, create/patch/deprecate actions, atomic writes
- **Review job** (`internal/agent/selfimprove/`): scheduled task, incremental message loading, review agent with restricted tools
- **Notification** (`*channel.Dispatcher.NotifyUser()`): notify users of new draft skills
- **In-conversation promotion** (`internal/agent/runner/template/system.md`): prompt guidance for agents to suggest enabling relevant drafts
- **Draft expiry**: global scan of user skill dirs, auto-deprecate drafts older than 30 days

## Implementation Phases

### Phase 1: Skill Lifecycle

Add `status` and `created-at` to skill frontmatter, implement create/patch/deprecate actions, update prompt rendering.

1. Add `Status` + `CreatedAt` fields to `Skill` and `skillFrontmatter`, add `normalizeSkillStatus()` helper (files: `internal/agent/runner/skill.go`)
2. Modify `FormatSkillsForPrompt()` to filter `deprecated` and include `<status>` tag (files: `internal/agent/runner/skill.go`)
3. Create `AtomicWriteFile` + package-level write lock (files: `internal/skills/atomicwrite.go`)
4. Implement `create`, `patch`, `deprecate` actions with validation and scoping (files: `internal/skills/manage.go`)
5. Extend skills tool schema and routing for new actions (files: `internal/skills/tool.go`)
6. Include `status` in list output (files: `internal/skills/list.go`)
7. Tests for skill lifecycle (files: `internal/skills/manage_test.go`, `internal/agent/runner/skill_test.go`)

### Phase 2: Database + Queries

Add `self_improve_reviewed_at` column and typed queries for the review job.

1. Add `self_improve_reviewed_at TEXT` column to `ctx_conversations` schema (files: `internal/db/schemas/tables/ctx_conversations.sql`)
2. Generate migration via `mise run db:diff -- add-self-improve-reviewed-at` (files: `internal/db/migrations/`)
3. Add `ListUnreviewedConversations` and `MarkConversationReviewed` queries (files: `internal/db/queries/ctx_conversations.sql`)
4. Add `GetMessagesSince` query for incremental loading (files: `internal/db/queries/ctx_messages.sql`)
5. Regenerate sqlc via `mise run generate` (files: `internal/db/sqlc/`)

### Phase 3: Review Engine

Core review agent: restricted tool, reviewer, prompts.

1. Create restricted `ReviewSkillsTool` — only create/patch/deprecate, delegates to `internal/skills/manage.go` (files: `internal/agent/selfimprove/reviewtool.go`)
2. Create review agent system prompt with "Nothing to save." exit condition (files: `internal/agent/selfimprove/prompts.go`)
3. Create `Reviewer` — constructs local engine, uses fast model, restricted tools, max 5 turns (files: `internal/agent/selfimprove/reviewer.go`)
4. Tests for review engine (files: `internal/agent/selfimprove/reviewtool_test.go`, `internal/agent/selfimprove/reviewer_test.go`)

### Phase 4: Scheduled Task + Integration

Wire the review job into scheduler, add config, notifications, draft expiry, and system prompt guidance.

1. Create `ReviewTask` entry point + `Config` + `ReviewDeps` with incremental loading and summary prepend for re-reviews (files: `internal/agent/selfimprove/review.go`)
2. Add draft expiry logic — global scan of user skill dirs, deprecate drafts >30 days (files: `internal/agent/selfimprove/review.go`)
3. Add `SelfImproveConfig` type and wire into `Snapshot` (files: `internal/config/config.go`, `internal/config/snapshot.go`, `internal/config/dbstore.go`)
4. Wire self-improve into scheduler startup alongside heartbeat (files: `cmd/anna/commands.go`, `cmd/anna/gateway.go`)
5. Add draft-skill promotion guidance to system prompt template (files: `internal/agent/runner/template/system.md`)
6. Tests for scheduled task (files: `internal/agent/selfimprove/review_test.go`)

## Testing Strategy

- **Unit tests**: all new files get `*_test.go`, run with `-race`
- **Skill lifecycle**: create/patch/deprecate round-trips, validation errors, refuse non-managed dirs, atomic write behavior
- **Status filtering**: `FormatSkillsForPrompt` filters deprecated, includes status tag, normalizes empty status
- **Review engine**: mock engine, verify restricted tools only, re-entrancy guard (no agent tool, no plugin hooks), fast model usage
- **Scheduled task**: mock deps, verify batch processing, incremental loading, skip `agent_id=""`/`user_id=0`, mark-reviewed only on clean completion, draft expiry global scan
- **Manual**: create skill → verify draft, have conversation → trigger review → verify draft created + notification, continue conversation → re-review → verify incremental, old draft → verify auto-deprecated

## Risks

| Risk | Impact | Mitigation |
|------|--------|------------|
| Review agent creates low-quality skills | Medium | All auto-created skills start as `draft`, require user promotion. Auto-expire after 30 days |
| Cost multiplication with many users | Medium | Hourly batch of 5 conversations max. Fast model. Incremental loading |
| Concurrent skill writes from multiple reviewers | Low | Package-level write lock in `internal/skills/atomicwrite.go` keyed by skills dir |
| Review agent infinite loop / re-entrancy | High | No collector, no agent tool, no plugin hooks on review agents. Max 5 turns |
| Large conversations expensive to review | Medium | Incremental loading (only new messages). Summary prepend for re-reviews |
| Legacy conversations crash review | Low | Skip `agent_id=""` or `user_id=0` in eligibility query |

## Open Questions

(All resolved through 3 rounds of Codex review + user feedback)

## Review Feedback

### Codex Review Round 1 (REQUEST_CHANGES)
- Drop `view` action (duplicates `load`)
- Restricted review tool (don't expose install/remove/search)
- Package-level write lock (not per-reviewer)
- Don't store AssistantDelta events
- Status normalization helper
- Mutations scoped to writable dir only
- Context propagation for memory writes
- Config in `config.go` not `types.go`
- Scheduler via heartbeat pattern
- Defer auto-promotion/deprecation

### Codex Review Round 2 (REQUEST_CHANGES)
- Fix reviewed eligibility: `reviewed_at IS NULL OR last_active > reviewed_at`
- One global job iterating agents
- Skip legacy sessions
- Add review input budget
- Mark-reviewed only on clean completion
- Tighten file list

### User Feedback
- Simplify: scheduled job instead of real-time collection
- Multi-agent/multi-user scoping: per-user skills, rate limiting
- Small frequent batches (1h, batch of 5) instead of daily dump
- Fast model for reviews
- Notify via heartbeat, enable in-conversation
- Draft expiry (30 days) for deprecation

### Codex Review Round 3 (REQUEST_CHANGES)
- `list()` lives in `list.go` not `tool.go`
- `ctx_conversations.sql` queries already exist (Modify not New)
- Incremental message query in `ctx_messages.sql`
- Direct SQL instead of `Assemble()` (doesn't support cursor)
- `*channel.Dispatcher` not `Notifier` for user-targeted notification
- Exact wiring: `commands.go` ~line 168, `gateway.go` ~line 188
- System prompt template change needed for draft promotion
- `ai.ProviderGetter` — construct engine locally
- `atlas.sum` committed alongside migration
- Global scan for draft expiry
- Summary prepend via `GetSummariesByConversation` + `FormatSummaryXML()`

## Final Status

(Updated after implementation completes)
