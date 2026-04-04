# Tasks: Self-Improvement System

## Phase 1: Skill Lifecycle

- [x] 1.1 Add `Status` + `CreatedAt` fields to `Skill` and `skillFrontmatter`, add `normalizeSkillStatus()` helper
- [x] 1.2 Modify `FormatSkillsForPrompt()` to filter `deprecated` and include `<status>` tag
- [x] 1.3 Create `AtomicWriteFile` + package-level write lock
- [x] 1.4 Implement `create`, `patch`, `deprecate` actions with validation and scoping
- [x] 1.5 Extend skills tool schema and routing for new actions
- [x] 1.6 Include `status` in list output
- [x] 1.7 Tests for skill lifecycle

## Phase 2: Database + Queries

- [x] 2.1 Add `self_improve_reviewed_at TEXT` column to `ctx_conversations` schema
- [x] 2.2 Generate migration via `mise run db:diff -- add-self-improve-reviewed-at`
- [x] 2.3 Add `ListUnreviewedConversations` and `MarkConversationReviewed` queries
- [x] 2.4 Add `GetMessagesSince` query for incremental loading
- [x] 2.5 Regenerate sqlc via `mise run generate`

## Phase 3: Review Engine

- [x] 3.1 Create restricted `ReviewSkillsTool`
- [x] 3.2 Create review agent system prompt
- [x] 3.3 Create `Reviewer` with local engine, fast model, restricted tools
- [x] 3.4 Tests for review engine

## Phase 4: Scheduled Task + Integration

- [x] 4.1 Create `ReviewTask` entry point + `Config` + `ReviewDeps` with incremental loading
- [x] 4.2 Add draft expiry logic — global scan, deprecate drafts >30 days
- [x] 4.3 Add `SelfImproveConfig` type and wire into `Snapshot`
- [x] 4.4 Wire self-improve into scheduler startup
- [x] 4.5 Add draft-skill promotion guidance to system prompt template
- [x] 4.6 Tests for scheduled task
