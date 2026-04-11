# Tasks: Version And Self-Upgrade Commands

## Status Legend

- [ ] Pending
- [x] Completed

**Task States:** `PENDING` | `IMPLEMENTING` | `VALIDATING` | `REVIEWING` | `APPROVED`

## Phase 1: Version Plumbing

- [ ] Task 1: Add build version metadata and `version` command
  - **Files:** `main.go`, `version_cmd.go`, `mise.toml`, `.goreleaser.yaml`
  - **State:** APPROVED
  - **Iterations:** 2
  - **Approach:** Added package-level build version metadata, wired top-level CLI commands, and configured local/release builds to inject version values.
  - **Gotchas:** The first install strategy was unsafe on rename failure, so the final version uses backup-and-restore semantics.
  - **Commit:** `fa9cfa9`

- [x] Task 2: Implement self-upgrade flow and tests
  - **Files:** `version_cmd.go`, `version_cmd_test.go`
  - **State:** APPROVED
  - **Iterations:** 2
  - **Approach:** Implemented GitHub release lookup, platform asset selection, archive extraction, safe install swapping, and upgrade flow tests including no-op and restore-on-failure cases.
  - **Gotchas:** Current published releases do not yet include the new commands, so the live smoke test validated download/install behavior rather than post-install command availability.
  - **Commit:** `fa9cfa9`

- [x] Task 3: Document new commands
  - **Files:** `README.md`, `docs/deployment.md`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** Added concise usage examples and explained the default install directory for `anna upgrade`.
  - **Gotchas:** None.
  - **Commit:** `d07ca62`

## Completion Summary

**Total Tasks:** 3
**Completed:** 3
**Remaining:** 0

### Final Notes

Implementation complete, validated with `go test ./...`, CLI smoke tests, and reviewer approval.
