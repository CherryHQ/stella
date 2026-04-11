# Tasks: Heartbeat Support

## Status Legend

- [ ] Pending
- [x] Completed

**Task States:** `PENDING` | `IMPLEMENTING` | `VALIDATING` | `REVIEWING` | `APPROVED`

## Phase 1: Runtime Support

- [ ] Task 1: Add heartbeat config and service
  - **Files:** `config.go`, `heartbeat/service.go`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** Added top-level heartbeat config plus a service that reads `HEARTBEAT.md`, runs a fast-model JSON gate, and only executes when the decision is `run`.
  - **Gotchas:** The decision path had to be tool-free, so the service rejects heartbeat checks that try to use tools.
  - **Commit:** pending

- [ ] Task 2: Wire heartbeat into gateway startup and scheduler flow
  - **Files:** `main.go`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** Reused the shared cron scheduler with a new non-persisted `ScheduleEvery` hook and only enabled heartbeat scheduling in gateway mode.
  - **Gotchas:** `setup()` needed to avoid creating the scheduler for chat mode when only heartbeat is enabled.
  - **Commit:** pending

## Phase 2: Validation and Docs

- [ ] Task 3: Add heartbeat tests
  - **Files:** `heartbeat/service_test.go`, `config_test.go`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** Added coverage for default config behavior, skip/run heartbeat flows, fast-model selection, tool-use rejection, and notifier failures.
  - **Gotchas:** The heartbeat service uses a string-based chat shim so tests can assert the selected model directly.
  - **Commit:** pending

- [ ] Task 4: Document heartbeat configuration and behavior
  - **Files:** `README.md`, `docs/configuration.md`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** Documented the new config block, workspace heartbeat file, env vars, defaults, and the gateway-only execution model.
  - **Gotchas:** Kept the docs aligned with the actual runtime behavior: fast-model gating first, notifier delivery after execution.
  - **Commit:** pending

## Completion Summary

**Total Tasks:** 4
**Completed:** 4
**Remaining:** 0

### Final Notes

All implementation tasks passed validation with `go test ./...`.
