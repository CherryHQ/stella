# Handoff: intent-classifier-help

## Task
Implement a lightweight short-message intent classifier that can map natural-language control requests (including Chinese) to existing channel commands before normal chat routing, improve help info so users do not need to memorize slash commands, and preserve existing command/channel behavior.

## Branch
- `feat/intent-classifier-help`

## Plan

### Phase 1 — Intent classifier scaffolding and coordinator integration
- Add a narrow pre-routing intent classifier for short plain-text messages.
- Start with safe actions only: `help`, `new`, `abort`, `compact`.
- Run classifier in `internal/channel/Coordinator.HandleIncoming` before falling through to chat.
- Keep execution delegated to existing command handlers.
- Add focused tests for routing and false-positive guards.

### Phase 2 — Help UX improvements
- Expand shared help text to explain slash commands and short natural-language phrases.
- Update any channel/CLI surfaced help that is too sparse or missing commands like `/abort`.
- Add/adjust tests for help text and CLI completion/help affordances.

### Phase 3 — Validation and cleanup
- Run targeted then broader tests via `mise run test` as appropriate.
- Fix review findings.
- Ensure docs/help copy and behavior stay aligned.

## Assumptions
- The classifier should only run on short text-only messages.
- If classification fails, times out, or returns invalid output, routing falls back to normal chat.
- Existing slash commands remain authoritative and should win over natural-language routing.
- `/model` and `/agent` stay channel-specific for now.

## Constraints
- Preserve `/abort` semantics in the coordinator queue path.
- Keep false positives low; when unsure, do nothing.
- Keep implementation lightweight and reviewable.
- Follow project workflow: use `mise run ...` for tests/format.

## Current status
- Branch created.
- Oracle planning completed.
- Phase 1 provider-wiring fix and regressions are in place.
- Final re-review completed: Phase 1 is clean enough to commit.
- Final re-review completed: Phase 2 is now clean enough to commit after the CLI `/agent` and `/whoami` discovery copy fixes.

## Phase log

### Phase 1
- Status: implemented, re-reviewed, approved for commit
- Changes:
  - added `internal/channel/intent_classifier.go` with a short-message LLM intent classifier that loads the resolved agent snapshot, uses the fast model, enforces short text-only gating, and falls back to `none` on errors/timeouts/invalid output
  - wired the classifier into `internal/channel/coordinator.go` before normal chat fallback via a new `WithIntentClassifier` coordinator option and a `handleResolvedIncoming` helper for easier testing
  - injected the classifier in `cmd/anna/gateway.go` using the configured provider registry builder path from plugin host + stored provider credentials
  - added focused classifier and coordinator routing tests in `internal/channel/intent_classifier_test.go`
  - `mise run format` also removed an unnecessary loop variable shadow in `plugins/channels/telegram/handler.go`
- Files touched:
  - `cmd/anna/gateway.go`
  - `internal/channel/coordinator.go`
  - `internal/channel/intent_classifier.go`
  - `internal/channel/intent_classifier_test.go`
  - `plugins/channels/telegram/handler.go`
- Files inspected during review:
  - `cmd/anna/gateway.go`
  - `internal/channel/coordinator.go`
  - `internal/channel/intent_classifier.go`
  - `internal/channel/intent_classifier_test.go`
  - `plugins/channels/telegram/handler.go`
  - `internal/config/dbstore.go`
  - `internal/config/snapshot.go`
  - `cmd/anna/commands.go`
  - `internal/pluginhost/builders.go`
  - `internal/channel/commands.go`
  - `docs/content/docs/core/models.md`
  - `docs/content/docs/changelog.mdx`
- Tests run:
  - `mise run format`
  - `mise run test -- ./internal/channel/... ./cmd/anna/...` ✅
  - reran `mise run format` after review fixes ✅
  - reran `mise run test -- ./internal/channel/... ./cmd/anna/...` after review fixes ✅
  - reran `mise run test -- ./internal/channel/... ./cmd/anna/...` during final re-review ✅
  - reran `mise run format` after the gateway/provider fix ✅
  - reran `mise run test -- ./internal/channel/... ./cmd/anna/...` after the gateway/provider fix ✅
- Review outcome:
  - initial review requested changes on provider alias wiring, strong-model fallback, and missing regressions
  - follow-up fixes confirmed resolved:
    - classifier now requires `model_fast` to be configured instead of silently falling back to the strong/default model
    - classifier-level regressions cover alias-shaped model refs and the no-`model_fast` case
    - coordinator routing still preserves explicit command handling first and keeps `/abort` on the queue-cancel path
    - `cmd/anna/gateway.go` now delegates provider-registry construction through `intentClassifierProviderGetterBuilder`, which uses the normalized provider key passed by the classifier directly
    - `TestIntentClassifierProviderGetterBuilderUsesProvidedProviderType` covers the real gateway/provider-registry builder path with alias-like credentials
  - final reviewer verdict: no remaining Phase 1 blockers; clean enough to commit
- Remaining known scope items:
  - help text and user-facing discoverability are still pending Phase 2
  - the classifier currently uses a strict short-message gate and a tiny action set only (`help`, `new`, `abort`, `compact`)
- Next-step context:
  - commit the Phase 1 unit with the current gateway/provider fix and regressions
  - proceed to Phase 2 help UX updates and keep the help copy aligned with the classifier action set

### Phase 2
- Status: implemented, re-reviewed, approved for commit
- Changes:
  - expanded `pkg/channel/util.go` (`WelcomeMessage`) into a more useful channel help guide with explicit slash commands, English + Chinese natural-language examples, and fallback-behavior guidance for unclear phrases
  - corrected CLI discovery in `internal/chatcli/command.go` by advertising `/help` but not `/abort`, since live in-flight abort is not a real CLI capability yet
  - replaced shared-channel help reuse in `internal/chatcli/chat_input.go` with a CLI-specific help message that explains local capabilities, channel-only natural-language shortcuts, and the `Ctrl+C` limitation clearly
  - kept typed `/abort` support in CLI as an informational hint only, explicitly telling the user it is not available as a live cancel command in the local TUI
  - updated the CLI help bar in `internal/chatcli/chat_view.go` to surface `/help`, `/new`, `/model`, `/quit`
  - strengthened tests in `internal/channel/commands_test.go` to assert Chinese examples and unclear-phrase fallback guidance are present in channel help output
  - updated CLI tests in `internal/chatcli/cli_test.go` to assert capability-aware CLI help output and that `/abort` is not advertised in CLI completions
- Files touched:
  - `pkg/channel/util.go`
  - `internal/chatcli/command.go`
  - `internal/chatcli/chat_input.go`
  - `internal/chatcli/chat_view.go`
  - `internal/channel/commands_test.go`
  - `internal/chatcli/cli_test.go`
- Tests run:
  - `mise run format` ✅
  - `mise run test -- ./pkg/channel/... ./internal/channel/... ./internal/chatcli/...` ✅
  - reviewer re-run of `mise run test -- ./pkg/channel/... ./internal/channel/... ./internal/chatcli/...` ✅
  - reran `mise run format` after CLI help/discovery corrections ✅
  - reran `mise run test -- ./pkg/channel/... ./internal/channel/... ./internal/chatcli/...` after CLI help/discovery corrections ✅
  - final re-review reran `mise run format` ✅
  - final re-review reran `mise run test -- ./pkg/channel/... ./internal/channel/... ./internal/chatcli/...` ✅
- Review outcome:
  - initial review flagged misleading CLI `/abort` advertising, misleading shared help reuse in CLI, and weak behavior coverage
  - follow-up fixes now confirmed resolved:
    - `/abort` is no longer advertised in CLI completions/help chrome, and typed `/abort` clearly reports that live abort is unavailable locally
    - CLI `/help` is capability-aware and no longer reuses the shared channel help text verbatim
    - `/agent` and `/whoami` completion descriptions now explain their CLI limitations instead of over-promising remote-channel behavior
    - CLI completion tests now assert those limitation-aware descriptions directly
  - final reviewer verdict: no remaining Phase 2 blockers; clean enough to commit
- Open issues:
  - live in-flight abort still does not exist in the local CLI; only channel backends have the coordinator queue path needed for true `/abort`
- Next-step context:
  - commit the Phase 2 help/discovery unit
  - proceed to Phase 3 final validation and broader regression checking

### Phase 3
- Status: completed, re-reviewed, no further code changes needed
- Validation performed:
  - `mise run format` - rerun during Phase 3 review, 0 issues
  - `mise run test` - rerun during Phase 3 review, repository-wide tests passed
  - `mise run build` - rerun during Phase 3 review, anna binary built successfully in 4.69s
  - `mise run db:validate` - rerun during Phase 3 review, passed
- Review outcome:
  - reran the recorded Phase 3 validation locally on 2026-04-15
  - found no correctness, help-copy, or branch-readiness blockers
  - no additional code changes are needed beyond recording this handoff outcome
- Remaining follow-ups: commit this handoff update with the branch; otherwise none

## Final summary
- Status: Phase 1, Phase 2, and Phase 3 are complete; the code is ready to merge, and the only remaining repo-state mismatch is this handoff note being uncommitted
- Validation performed:
  - Phase 1 & 2 review cycle: targeted tests for `internal/channel/...`, `cmd/anna/...`, `pkg/channel/...`, `internal/chatcli/...` ✅
  - Phase 3 review rerun: `mise run format`, `mise run test`, `mise run build`, `mise run db:validate` ✅
- Implementation highlights:
  - Intent classifier for short text-only messages (help/new/abort/compact) with safe fallback
  - Coordinator integration before normal chat routing, preserving existing command handling
  - Help UX improvements with English + Chinese natural-language examples
  - CLI-specific help that accurately reflects local vs channel-only capabilities
- Known limitations (documented in help):
  - Live in-flight `/abort` is not available in local CLI; only channel backends have the coordinator queue path
- Follow-ups: commit this handoff note; no further code changes
