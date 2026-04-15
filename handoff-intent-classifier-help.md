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
- Status: blocked on Phase 1

### Phase 3
- Status: blocked on Phase 1/2

## Final summary
- Status: Phase 1 approved; clean enough to commit
- Validation: `mise run test -- ./internal/channel/... ./cmd/anna/...` passed during the final re-review
- Follow-ups: commit Phase 1, then move to Phase 2 help UX updates while keeping help text and classifier behavior aligned
