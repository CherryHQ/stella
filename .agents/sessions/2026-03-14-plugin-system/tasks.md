# Tasks: Dual Plugin System (Go + JS)

## Status Legend

- [ ] Pending
- [x] Completed

**Task States:** `PENDING` | `IMPLEMENTING` | `VALIDATING` | `REVIEWING` | `APPROVED`

## Phase 1: Core Plugin Framework

- [ ] Task 1: Create `pkg/plugin/` public contract
  - **Files:** `pkg/plugin/types.go`, `pkg/plugin/register.go`
  - **State:** PENDING
  - **Iterations:** 0
  - **Approach:**
  - **Gotchas:**
  - **Commit:**

- [ ] Task 2: Create `internal/plugin/` registry, manager, adapt
  - **Files:** `internal/plugin/registry.go`, `internal/plugin/manager.go`, `internal/plugin/adapt.go`
  - **State:** PENDING
  - **Iterations:** 0
  - **Approach:**
  - **Gotchas:**
  - **Commit:**

- [ ] Task 3: Add plugin config to Config struct
  - **Files:** `internal/config/config.go`
  - **State:** PENDING
  - **Iterations:** 0
  - **Approach:**
  - **Gotchas:**
  - **Commit:**

- [ ] Task 4: Add PluginHookRunner interface + PluginHooks to LoopConfig
  - **Files:** `internal/agent/engine/types.go`
  - **State:** PENDING
  - **Iterations:** 0
  - **Approach:**
  - **Gotchas:**
  - **Commit:**

- [ ] Task 5: Wire hooks into ExecuteToolCalls (before/after)
  - **Files:** `internal/agent/engine/tool_execution.go`
  - **State:** PENDING
  - **Iterations:** 0
  - **Approach:**
  - **Gotchas:**
  - **Commit:**

- [ ] Task 6: Thread hooks through GoRunner
  - **Files:** `internal/agent/runner/gorunner.go`
  - **State:** PENDING
  - **Iterations:** 0
  - **Approach:**
  - **Gotchas:**
  - **Commit:**

- [ ] Task 7: Thread hooks through DelegateTool
  - **Files:** `internal/agent/tool/delegate.go`
  - **State:** PENDING
  - **Iterations:** 0
  - **Approach:**
  - **Gotchas:**
  - **Commit:**

- [ ] Task 8: Wire plugin manager into setup()
  - **Files:** `cmd/anna/commands.go`
  - **State:** PENDING
  - **Iterations:** 0
  - **Approach:**
  - **Gotchas:**
  - **Commit:**

- [ ] Task 9: Add session hooks to Pool
  - **Files:** `internal/agent/pool.go`, `internal/agent/pool_options.go`
  - **State:** PENDING
  - **Iterations:** 0
  - **Approach:**
  - **Gotchas:**
  - **Commit:**

## Phase 2: JS Extension Runtime

- [ ] Task 10: Add fastschema/qjs dependency
  - **Files:** `go.mod`, `go.sum`
  - **State:** PENDING
  - **Iterations:** 0
  - **Approach:**
  - **Gotchas:**
  - **Commit:**

- [ ] Task 11: Create JS runtime wrapper with per-plugin mutex
  - **Files:** `internal/plugin/jsrt/runtime.go`
  - **State:** PENDING
  - **Iterations:** 0
  - **Approach:**
  - **Gotchas:**
  - **Commit:**

- [ ] Task 12: Create JS host API bindings
  - **Files:** `internal/plugin/jsrt/hostapi.go`
  - **State:** PENDING
  - **Iterations:** 0
  - **Approach:**
  - **Gotchas:**
  - **Commit:**

- [ ] Task 13: Wire JS loading into Manager.LoadAll
  - **Files:** `internal/plugin/manager.go`
  - **State:** PENDING
  - **Iterations:** 0
  - **Approach:**
  - **Gotchas:**
  - **Commit:**

## Phase 3: Go Plugin Builder

- [ ] Task 14: Create Go plugin builder
  - **Files:** `internal/plugin/goplugin/builder.go`
  - **State:** PENDING
  - **Iterations:** 0
  - **Approach:**
  - **Gotchas:**
  - **Commit:**

- [ ] Task 15: Create Go templates for generated files
  - **Files:** `internal/plugin/goplugin/templates.go`
  - **State:** PENDING
  - **Iterations:** 0
  - **Approach:**
  - **Gotchas:**
  - **Commit:**

## Phase 4: CLI Commands

- [ ] Task 16: Create `anna plugin` CLI subcommand
  - **Files:** `cmd/anna/plugin.go`
  - **State:** PENDING
  - **Iterations:** 0
  - **Approach:**
  - **Gotchas:**
  - **Commit:**

## Phase 5: Tests & Polish

- [ ] Task 17: Unit tests for pkg/plugin + internal/plugin
  - **Files:** `pkg/plugin/register_test.go`, `internal/plugin/registry_test.go`, `internal/plugin/manager_test.go`, `internal/plugin/adapt_test.go`
  - **State:** PENDING
  - **Iterations:** 0
  - **Approach:**
  - **Gotchas:**
  - **Commit:**

- [ ] Task 18: Unit tests for JS runtime
  - **Files:** `internal/plugin/jsrt/runtime_test.go`, `internal/plugin/jsrt/hostapi_test.go`
  - **State:** PENDING
  - **Iterations:** 0
  - **Approach:**
  - **Gotchas:**
  - **Commit:**

- [ ] Task 19: Unit tests for Go plugin builder
  - **Files:** `internal/plugin/goplugin/builder_test.go`
  - **State:** PENDING
  - **Iterations:** 0
  - **Approach:**
  - **Gotchas:**
  - **Commit:**

- [ ] Task 20: Integration tests
  - **Files:** `internal/plugin/integration_test.go`
  - **State:** PENDING
  - **Iterations:** 0
  - **Approach:**
  - **Gotchas:**
  - **Commit:**

- [ ] Task 21: Lint, format, docs, builtin skill update
  - **Files:** `README.md`, `docs/content/docs/features/`, `internal/agent/runner/builtin/anna/`
  - **State:** PENDING
  - **Iterations:** 0
  - **Approach:**
  - **Gotchas:**
  - **Commit:**

## Completion Summary

**Total Tasks:** 21
**Completed:** 0
**Remaining:** 21

### Final Notes

(Added at completion)
