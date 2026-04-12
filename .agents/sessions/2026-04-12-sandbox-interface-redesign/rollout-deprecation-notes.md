# Phase 1: Rollout and Deprecation Notes

This document outlines the migration path from current boxsh-specific implementation to the new `sandbox` abstraction layer. It covers compatibility preservation, deprecation timeline, and rollout phases.

## Current State (Baseline)

### Existing Components

| Component | Location | Role in Current System |
|-----------|----------|------------------------|
| `boxshclient.Client` | `internal/sandbox/boxshclient/client.go` | Direct JSON-RPC client |
| `boxshclient.SharedBackend` | `internal/sandbox/boxshclient/backend.go` | Runner-managed backend |
| `boxshclient.*Adapter` | `internal/sandbox/boxshclient/tool_adapters.go` | Boxsh-backed tool implementations |
| `sandboxBackend` interface | `internal/agent/runner/sandbox_backend.go` | Runner abstraction (leaks boxsh) |
| `BuildContext.Backend` | `plugins/tools/registry.go` | Build-time boxsh leakage |
| Direct tool impls | `plugins/tools/{bash,read,write,edit}/` | Unsandboxed native implementations |

### Current Execution Flow

```
Runner creates SharedBackend (boxsh)
  ↓
BuildContext gets Backend *boxshclient.SharedBackend (leak!)
  ↓
CoreToolsBuilderWithSandbox()
  ↓
if backend.Boxsh() != nil → use boxsh adapters
if backend.Boxsh() == nil → use native implementations (dual path!)
```

## Deprecation Timeline

### Phase 2 (Immediate - Types Introduction)

**New API Added:**
- `internal/sandbox/policy.go` - `Policy` types
- `internal/sandbox/session.go` - `Session`, `Host` interfaces
- `internal/sandbox/factory.go` - `Factory`, `Registry`

**Preserved (No Breaking Changes):**
- All existing `boxshclient` types remain functional
- `sandboxBackend` interface continues to work
- `BuildContext.Backend` remains available (marked deprecated)

**Deprecation Markers:**
```go
// Deprecated: Use sandbox.Session instead. Will be removed in Phase 4.
type BuildContext struct {
    Backend *boxshclient.SharedBackend // Deprecated
}
```

### Phase 3 (Runner Refactor)

**Breaking Changes:**
- `BuildContext.Backend` removed
- `sandboxBackend` replaced with `sandbox.Session`
- Runner creates `sandbox.Session` via `Factory`

**Migration:**
```go
// Before
bc := plugintools.BuildContext{
    Backend: backend.Boxsh(), // Removed!
}

// After
bc := plugintools.BuildContext{
    // Sandbox-agnostic; Host injected at execution time
}
```

**Preserved:**
- `boxshclient` types for backend implementation
- All existing tool registrations

### Phase 4 (Core Tool Unification)

**Breaking Changes:**
- `boxshclient.*Adapter` types removed
- Native tool implementations refactored to use `sandbox.Host`
- Single implementation path (no more dual path)

**Migration for Tool Builders:**
```go
// Before - two implementations existed
func buildTools(bc BuildContext) []tools.Tool {
    if backend := bc.Backend; backend != nil {
        return []tools.Tool{
            boxshclient.NewBashAdapter(backend), // Removed!
        }
    }
    return []tools.Tool{bash.NewBashTool(...)}
}

// After - single Host-based implementation
func buildTools(bc BuildContext) []tools.Tool {
    return []tools.Tool{
        bash.NewBashTool(), // Host injected at execution time
    }
}
```

### Phase 5 (Plugin/MCP Mediation)

**Breaking Changes:**
- MCP stdio transport uses `Host.StartProcess`
- `webfetch` uses `Host.HTTPRequest`
- Skills tools use `Host` methods

**Preserved:**
- Tool definitions (schema) unchanged
- External behavior unchanged

### Phase 6 (Cleanup)

**Removed:**
- `internal/sandbox/boxshclient/tool_adapters.go`
- `boxshclient.*Adapter` types
- Deprecated `BuildContext.Backend` field (already removed in Phase 3)
- Dual-path logic in `CoreToolsBuilderWithSandbox`

## Compatibility Layer

### Temporary Adapter (Phase 2-3)

```go
// sandboxBackendBridge adapts new Session to old sandboxBackend interface
// Used during transition to minimize runner changes
type sandboxBackendBridge struct {
    session sandbox.Session
}

func (b *sandboxBackendBridge) Runtime() pkgplugins.SandboxRuntime {
    // Adapt from session
}

func (b *sandboxBackendBridge) Boxsh() *boxshclient.SharedBackend {
    // Extract from boxsh-backed session
    // Returns nil for local sessions
}
```

### Feature Flags

```go
// internal/config/config.go

const (
    // UseLegacySandbox forces old boxshclient-only path
    // Deprecated: will be removed in Phase 4
    UseLegacySandbox = "sandbox.legacy"
)
```

## Rollout Phases

### Phase 1 (Complete) ✅

- [x] Design spec finalized
- [x] Policy compatibility matrix
- [x] Exceptions register
- [x] Rollout/deprecation notes (this document)

### Phase 2 (Types)

1. Add `internal/sandbox/policy.go`
2. Add `internal/sandbox/session.go`
3. Add `internal/sandbox/factory.go`
4. Add local implementation (stub)
5. Add boxsh implementation (adapter)
6. Add tests for policy validation
7. **Checkpoint**: All new types present, no breaking changes

### Phase 3 (Runner)

1. Create `sandboxBackendBridge` adapter
2. Refactor `GoRunner` to create `sandbox.Session`
3. Remove `BuildContext.Backend`
4. Add execution-time `Host` injection
5. Define session semantics (concurrency, cancellation, cleanup)
6. **Checkpoint**: Runner uses new abstraction, tests pass

### Phase 4 (Core Tools)

1. Create parity matrix for bash/read/write/edit
2. Refactor native tools to use `Host` interface
3. Add shared normalization logic
4. Remove `boxshclient.*Adapter` implementations
5. **Checkpoint**: Single implementation path, parity tests pass

### Phase 5 (Plugin/MCP)

1. Migrate MCP stdio to `Host.StartProcess`
2. Migrate webfetch to `Host.HTTPRequest`
3. Migrate skills tools to `Host` filesystem methods
4. Add bypass detection/lint rules
5. **Checkpoint**: All execution paths mediated or in exceptions register

### Phase 6 (Cleanup)

1. Remove deprecated compatibility code
2. Simplify tests
3. Update documentation
4. **Checkpoint**: Clean codebase, full test coverage

## API Compatibility Matrix

| API | Phase 1 | Phase 2 | Phase 3 | Phase 4 | Phase 5 | Phase 6 |
|-----|---------|---------|---------|---------|---------|---------|
| `boxshclient.Client` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| `boxshclient.SharedBackend` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| `boxshclient.*Adapter` | ✅ | ✅ | ✅ | ❌ | ❌ | ❌ |
| `BuildContext.Backend` | ✅ | ⚠️ | ❌ | ❌ | ❌ | ❌ |
| `sandboxBackend` | ✅ | ✅ | ⚠️ | ❌ | ❌ | ❌ |
| `sandbox.Policy` | — | ✅ | ✅ | ✅ | ✅ | ✅ |
| `sandbox.Session` | — | ✅ | ✅ | ✅ | ✅ | ✅ |
| `sandbox.Host` | — | ✅ | ✅ | ✅ | ✅ | ✅ |

**Legend**: ✅ Available | ⚠️ Deprecated | ❌ Removed | — Not yet added

## Consumer Impact

### Plugin Tool Authors

| Phase | Impact | Action Required |
|-------|--------|-----------------|
| 1-2 | None | None |
| 3 | Minor | `BuildContext.Backend` removed; use execution-time Host |
| 4 | None | Tools automatically use unified implementation |
| 5 | None (if using Host) | None |

### Runner/Integration Authors

| Phase | Impact | Action Required |
|-------|--------|-----------------|
| 1-2 | None | None |
| 3 | Major | Migrate to `sandbox.Factory` for session creation |
| 4 | None | Core tools unified |
| 5 | None | All paths mediated |

### End Users

| Phase | Impact | Action Required |
|-------|--------|-----------------|
| 1-3 | None | Transparent migration |
| 4+ | Better consistency | Unified behavior across backends |

## Testing Strategy During Rollout

### Phase 2-3 (Parallel Implementation)

```go
// Test both paths work identically
func TestSandboxParity(t *testing.T) {
    boxshSession := createBoxshSession(policy)
    localSession := createLocalSession(policy)
    
    // Both should produce same results for same policy
    assertSameBehavior(t, boxshSession.Host(), localSession.Host())
}
```

### Phase 4 (Adapter Removal)

```go
// Verify no boxsh leakage
func TestNoBoxshLeakage(t *testing.T) {
    code := inspectCode("internal/agent/runner")
    assertNoReference(t, code, "boxshclient.SharedBackend")
    assertNoReference(t, code, "BuildContext.Backend")
}
```

## Rollback Plan

If issues arise during rollout:

| Phase | Rollback Strategy |
|-------|-------------------|
| 2 | Disable new code paths via feature flag |
| 3 | Restore `BuildContext.Backend` temporarily |
| 4 | Keep adapter code but don't use it |
| 5 | Revert to direct implementation for affected tools |

## Success Criteria

Rollout is complete when:

1. All code references `sandbox.Policy`, `sandbox.Session`, `sandbox.Host`
2. No code references `boxshclient` outside of `internal/sandbox/boxsh*.go`
3. Single implementation path for all core tools
4. All execution paths mediated or explicitly excepted
5. Tests pass for all backends
6. Exceptions register is empty (all closed or accepted as permanent)

## Communication Plan

| Phase | Audience | Message |
|-------|----------|---------|
| 1 | Core team | Design review complete |
| 2 | All developers | New sandbox types available |
| 3 | Plugin authors | BuildContext.Backend deprecation notice |
| 4 | All developers | Core tools unified, adapters removed |
| 5 | All developers | All paths mediated |
| 6 | Users | Release notes for improved sandbox consistency |
