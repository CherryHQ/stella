# Phase 1 Design Specification: Sandbox Interface Redesign

## Overview

This document specifies the redesigned sandbox interface that will serve as the single execution boundary for all tool execution in Anna. The design follows the plan outlined in `plan.md` and addresses the need to remove boxsh-specific leakage while establishing a backend-agnostic abstraction.

## Top-Level Concepts

### sandbox.Policy

An immutable, backend-agnostic session policy describing requested limits for:

- **Filesystem limits**: Working directory, allowed read/write paths, read-only mounts
- **Network limits**: Mode (disabled/allow_all/whitelist), allowlist entries, timeout behavior
- **Process limits**: Execution timeout, max concurrent processes, environment variables
- **Future resource limits**: CPU, memory, I/O bandwidth (defined extension points)

```go
// Policy is an immutable session policy independent of any backend.
type Policy struct {
    // Filesystem policy
    Filesystem FilesystemPolicy
    
    // Network policy  
    Network NetworkPolicy
    
    // Process policy
    Process ProcessPolicy
    
    // Immutable marker - prevents accidental mutation
    _immutable struct{}
}

// FilesystemPolicy defines filesystem constraints
type FilesystemPolicy struct {
    WorkingDir     string   // Base working directory
    ReadOnlyPaths  []string // Paths mounted read-only
    ReadWritePaths []string // Paths allowed for read-write
    AllowEscapes   bool     // Whether to allow paths outside WorkingDir
}

// NetworkPolicy defines network constraints
type NetworkPolicy struct {
    Mode      NetworkMode // disabled, allow_all, whitelist
    Allowlist []string    // CIDRs/hosts for whitelist mode
    Timeout   time.Duration
}

// ProcessPolicy defines process constraints
type ProcessPolicy struct {
    MaxConcurrent   int
    Timeout         time.Duration
    Environment     map[string]string
    InheritEnv      bool
}
```

### sandbox.Session

Per-agent / per-run sandbox boundary and lifecycle owner.

```go
// Session is a sandbox-managed execution boundary.
type Session interface {
    // Host returns the constrained host surface for this session.
    // All tool execution must use this Host for mediated operations.
    Host() Host
    
    // Policy returns the session's effective policy.
    Policy() Policy
    
    // Close shuts down the session and cleans up resources.
    // Guarantees cleanup of all session resources.
    Close() error
    
    // Alive reports whether the session is healthy and usable.
    Alive() bool
    
    // Done returns a channel that closes when the session terminates.
    Done() <-chan struct{}
}
```

**Session Semantics (Shared Across Backends):**

1. **Concurrency**: Single Host per Session. Concurrent tool calls share the same Host but must not interfere.
2. **State Visibility**: Cross-call state (cwd, env vars, temp files) is visible within the session lifetime.
3. **Cancellation**: Context cancellation propagates to in-flight operations.
4. **Cleanup**: `Close()` guarantees resource cleanup regardless of session state.
5. **Liveness Loss**: Backend failures make the session unusable; `Alive()` returns false.

### sandbox.Host

The constrained host surface exposed to tool execution inside a session.

```go
// Host provides mediated access to host resources.
type Host interface {
    // Filesystem operations
    ReadFile(ctx context.Context, path string, offset, limit int) (ReadResult, error)
    WriteFile(ctx context.Context, path string, content []byte) (WriteResult, error)
    EditFile(ctx context.Context, path string, edits []Edit) (EditResult, error)
    Stat(ctx context.Context, path string) (StatResult, error)
    ListDir(ctx context.Context, path string) ([]DirEntry, error)
    
    // Process operations
    Exec(ctx context.Context, command string, opts ExecOptions) (ExecResult, error)
    
    // Network operations
    HTTPRequest(ctx context.Context, opts HTTPOptions) (HTTPResult, error)
    
    // Session-relative path resolution
    ResolvePath(path string) (string, error)
    WorkingDir() string
}
```

## Package Layout

```
internal/sandbox/
├── policy.go           # Policy types and validation
├── session.go          # Session and Host interfaces
├── factory.go          # Backend resolution and session creation
├── local_*.go          # Local/unsandboxed implementation
└── boxsh_*.go          # Boxsh-backed implementation (hides boxshclient)
```

## Backend Resolution

```go
// Factory creates sessions from policies.
type Factory interface {
    // CreateSession validates policy against backend capabilities
    // and returns a Session. Fails closed if policy unsupported.
    CreateSession(ctx context.Context, policy Policy) (Session, error)
    
    // Supported returns whether this factory can handle the policy.
    Supported(policy Policy) bool
    
    // Name returns the backend identifier.
    Name() string
}

// Registry manages available backends.
type Registry struct {
    factories map[string]Factory
}

func (r *Registry) CreateSession(ctx context.Context, policy Policy) (Session, error) {
    // 1. Find compatible factory
    // 2. Fail closed if no factory supports the policy
    // 3. Create session with selected backend
}
```

## Policy Compatibility Matrix

| Policy Feature | boxsh | local | Notes |
|---------------|-------|-------|-------|
| Filesystem COW overlay | ✅ | N/A | boxsh provides overlay isolation |
| Working dir restriction | ✅ | ✅ | Both enforce cwd boundaries |
| Read-only mounts | ✅ | ⚠️ | local: advisory only |
| Network disabled | ✅ | ⚠️ | local: advisory/observability only |
| Network allow_all | ✅ | ✅ | local: unrestricted host network |
| Network whitelist | ❌ | ⚠️ | boxsh 2.0.1 doesn't support; local: advisory |
| Process timeouts | ✅ | ✅ | Both support execution timeouts |
| Process env vars | ✅ | ✅ | Both support env customization |

**Legend:** ✅ Full support | ⚠️ Partial/Advisory | ❌ Not supported | N/A Not applicable

## Lifecycle Rules

### Build-Time vs Execution-Time

- **Build-Time (Tool Construction)**: Sandbox-agnostic. Tools receive configuration but no Host.
- **Execution-Time (Tool Execution)**: Host-injected. Execution context provides the Host.

```go
// BuildContext - no sandbox handle
type BuildContext struct {
    WorkDir     string
    AnnaHome    string
    // ... other config, NO sandbox handles
}

// ExecutionContext - Host provided
type ExecutionContext struct {
    Context context.Context
    Host    sandbox.Host  // Injected at execution time
    Args    map[string]any
}
```

### Relaxed Mode Creation

Local/unsandboxed sessions are created ONLY through explicit opt-in:

```go
// Explicit relaxed policy - never implicit fallback
relaxedPolicy := sandbox.Policy{
    Filesystem: sandbox.FilesystemPolicy{
        AllowEscapes: true, // Explicit opt-in
    },
    Network: sandbox.NetworkPolicy{
        Mode: sandbox.NetworkAllowAll, // Explicit
    },
}

// Config-driven relaxed mode
if cfg.Sandbox.Backend == "noop" || cfg.Sandbox.Backend == "local" {
    // Explicit opt-in via configuration
    factory = localFactory
}
```

## Fail-Closed Behavior

Unsupported policy requests fail by default:

```go
func (f *boxshFactory) CreateSession(ctx context.Context, policy Policy) (Session, error) {
    if policy.Network.Mode == NetworkWhitelist {
        return nil, fmt.Errorf("boxsh: whitelist mode not supported (requires boxsh >= 2.1)")
    }
    // ... create session
}
```

To override (explicit relaxed mode):
```go
// User explicitly accepts reduced guarantees
policy := Policy{
    Network: NetworkPolicy{Mode: NetworkWhitelist},
    Relaxed: true, // Acknowledge partial enforcement
}
```

## Transport Classes for Plugin/MCP

First-cut transport requirements:

| Transport | MCP Support | Sandbox Mediation | Notes |
|-----------|-------------|-------------------|-------|
| stdio (local process) | ✅ | Process spawning via Host.Exec | Local MCP servers |
| SSE (Server-Sent Events) | ✅ | HTTP via Host.HTTPRequest | Remote streaming |
| Streamable HTTP | ✅ | HTTP via Host.HTTPRequest | MCP 2024-11 spec |
| HTTP (legacy) | ✅ | HTTP via Host.HTTPRequest | Legacy MCP |
| WebSocket | ⚠️ | Future extension | Not required in first cut |

All local-side transport operations (process spawning, HTTP requests) are mediated through Host.
Remote MCP servers remain separate trust boundaries per plan.md assumptions.

## Observability Requirements

Events/logs emitted by sandbox layer:

| Event | Level | Context |
|-------|-------|---------|
| `sandbox.policy_denied` | Error | Policy violation blocked |
| `sandbox.relaxed_mode` | Warn | Relaxed policy in effect |
| `sandbox.unsupported_backend` | Error | Backend can't enforce policy |
| `sandbox.exception_used` | Warn | Non-mediated path used (bypass) |
| `sandbox.session_created` | Info | Session start with policy summary |
| `sandbox.session_closed` | Info | Session cleanup |
| `sandbox.exec.started` | Debug | Command execution |
| `sandbox.exec.finished` | Debug | Command completion |

```go
// Observability interface (injected into Host implementations)
type Observer interface {
    PolicyDenied(policy Policy, operation string, reason string)
    RelaxedModeSelected(policy Policy, reason string)
    UnsupportedBackend(policy Policy, backend string, reason string)
    ExceptionUsed(path string, owner string, reason string)
}
```
