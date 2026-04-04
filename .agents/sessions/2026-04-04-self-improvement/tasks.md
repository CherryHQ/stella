# Tasks: Self-Improvement System

## Phase 1: Skill Lifecycle

- [ ] 1.1 Add `Status` + `CreatedAt` fields to `Skill` and `skillFrontmatter`, add `normalizeSkillStatus()` helper
- [ ] 1.2 Modify `FormatSkillsForPrompt()` to filter `deprecated` and include `<status>` tag
- [ ] 1.3 Create `AtomicWriteFile` + package-level write lock
- [ ] 1.4 Implement `create`, `patch`, `deprecate` actions with validation and scoping
- [ ] 1.5 Extend skills tool schema and routing for new actions
- [ ] 1.6 Include `status` in list output
- [ ] 1.7 Tests for skill lifecycle

## Phase 2: Database + Queries

- [ ] 2.1 Add `self_improve_reviewed_at TEXT` column to `ctx_conversations` schema
- [ ] 2.2 Generate migration via `mise run db:diff -- add-self-improve-reviewed-at`
- [ ] 2.3 Add `ListUnreviewedConversations` and `MarkConversationReviewed` queries
- [ ] 2.4 Add `GetMessagesSince` query for incremental loading
- [ ] 2.5 Regenerate sqlc via `mise run generate`

## Phase 3: Review Engine

- [ ] 3.1 Create restricted `ReviewSkillsTool`
- [ ] 3.2 Create review agent system prompt
- [ ] 3.3 Create `Reviewer` with local engine, fast model, restricted tools
- [ ] 3.4 Tests for review engine

## Phase 4: Scheduled Task + Integration

- [ ] 4.1 Create `ReviewTask` entry point + `Config` + `ReviewDeps` with incremental loading
- [ ] 4.2 Add draft expiry logic — global scan, deprecate drafts >30 days
- [ ] 4.3 Add `SelfImproveConfig` type and wire into `Snapshot`
- [ ] 4.4 Wire self-improve into scheduler startup
- [ ] 4.5 Add draft-skill promotion guidance to system prompt template
- [ ] 4.6 Tests for scheduled task
