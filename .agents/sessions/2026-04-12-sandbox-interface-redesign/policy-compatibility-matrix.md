# Phase 1: Policy Compatibility Matrix

This document defines the compatibility between `sandbox.Policy` requirements and available backend implementations. Used for fail-closed validation during session creation.

## Matrix Format

Rows = Policy requirements  
Columns = Backends  
Cells = Support level + notes

## Compatibility Levels

| Level | Symbol | Description |
|-------|--------|-------------|
| Full | ✅ | Backend fully enforces this policy |
| Partial | ⚠️ | Backend enforces with limitations |
| Advisory | 🔶 | Backend logs/observes but doesn't enforce |
| None | ❌ | Backend cannot enforce; will fail closed |
| N/A | — | Not applicable to this backend |

---

## Filesystem Policy Compatibility

| Policy Feature | boxsh | local | Notes |
|----------------|-------|-------|-------|
| **Working directory isolation** | ✅ Full | ✅ Full | boxsh: COW overlay; local: directory restriction |
| **Read-only path mounts** | ✅ Full | ⚠️ Partial | local: advisory validation only |
| **Read-write path restrictions** | ✅ Full | ⚠️ Partial | local: advisory validation only |
| **Path escape prevention** | ✅ Full | 🔶 Advisory | local: validates but doesn't block escapes |
| **Symlink traversal protection** | ✅ Full | 🔶 Advisory | boxsh: kernel-enforced; local: best-effort |
| **File size limits** | ❌ None | ❌ None | Future extension |
| **Disk quota** | ❌ None | ❌ None | Future extension |

## Network Policy Compatibility

| Policy Feature | boxsh | local | Notes |
|----------------|-------|-------|-------|
| **Mode: disabled** | ✅ Full | 🔶 Advisory | local: can observe but not block |
| **Mode: allow_all** | ✅ Full | ✅ Full | Both allow unrestricted |
| **Mode: whitelist** | ❌ None | 🔶 Advisory | boxsh 2.0.1 doesn't support whitelist |
| **CIDR-based rules** | ❌ None | 🔶 Advisory | Future for boxsh |
| **Hostname-based rules** | ❌ None | 🔶 Advisory | Future for boxsh |
| **TLS/certificate policy** | ❌ None | ❌ None | Future extension |
| **Request rate limiting** | ❌ None | ❌ None | Future extension |
| **Timeout enforcement** | ✅ Full | ✅ Full | Both support |

## Process Policy Compatibility

| Policy Feature | boxsh | local | Notes |
|----------------|-------|-------|-------|
| **Max concurrent processes** | ✅ Full | ✅ Full | Configurable in both |
| **Execution timeout** | ✅ Full | ✅ Full | Context cancellation |
| **Environment variable control** | ✅ Full | ✅ Full | Both support |
| **Process isolation (namespaces)** | ✅ Full | — N/A | boxsh: namespace isolation; local: native processes |
| **Resource limits (CPU/memory)** | ❌ None | ❌ None | Future extension |
| **UID/GID isolation** | ✅ Full | — N/A | boxsh: user namespace |
| **Seccomp filtering** | ✅ Full | — N/A | boxsh: syscall filtering |

---

## Fail-Closed Behavior by Backend

### boxsh Backend

```go
func (f *boxshFactory) Supported(policy Policy) error {
    // Whitelist mode not supported in boxsh 2.0.1
    if policy.Network.Mode == NetworkWhitelist {
        return fmt.Errorf("boxsh: whitelist mode requires boxsh >= 2.1")
    }
    
    // All other policies can be enforced
    return nil
}
```

**Behavior**: Returns error for unsupported policies. Caller must either:
1. Use a different backend
2. Explicitly relax the policy
3. Upgrade boxsh

### local Backend

```go
func (f *localFactory) Supported(policy Policy) error {
    // Local backend cannot truly enforce network restrictions
    if policy.Network.Mode == NetworkDisabled && !policy.Relaxed {
        return fmt.Errorf("local: cannot enforce network disabled without relaxed=true")
    }
    
    // Advisory-only for path restrictions
    if !policy.Filesystem.AllowEscapes && !policy.Relaxed {
        return fmt.Errorf("local: path escape prevention is advisory only; set relaxed=true to accept")
    }
    
    return nil
}
```

**Behavior**: Returns error unless `policy.Relaxed=true` for unenforceable policies.

---

## Relaxed Mode Compatibility

When `Policy.Relaxed = true`, backends downgrade requirements:

| Original Policy | boxsh (relaxed) | local (relaxed) |
|---------------|-----------------|-----------------|
| Network whitelist | ✅ Enforce as allow_all | ✅ Allow all |
| Network disabled | ✅ Enforce disabled | 🔶 Log only |
| No path escapes | ✅ Enforce | 🔶 Validate, log violations |

**Relaxed mode is opt-in**: Must be explicitly set; never implicit fallback.

---

## Backend Selection Algorithm

```go
func (r *Registry) CreateSession(ctx context.Context, policy Policy) (Session, error) {
    // 1. Try requested backend if specified
    if policy.Backend != "" {
        factory, ok := r.factories[policy.Backend]
        if !ok {
            return nil, fmt.Errorf("unknown backend: %s", policy.Backend)
        }
        if err := factory.Supported(policy); err != nil {
            return nil, fmt.Errorf("backend %s cannot satisfy policy: %w", policy.Backend, err)
        }
        return factory.CreateSession(ctx, policy)
    }
    
    // 2. Auto-select: prefer strongest backend
    for _, name := range []string{"boxsh", "local"} {
        factory := r.factories[name]
        if factory.Supported(policy) == nil {
            return factory.CreateSession(ctx, policy)
        }
    }
    
    // 3. Fail closed - no backend can satisfy
    return nil, fmt.Errorf("no backend can satisfy policy requirements")
}
```

---

## Testing Requirements

| Test | Description |
|------|-------------|
| `TestBoxsh_UnsupportedWhitelist` | Verify boxsh rejects whitelist mode |
| `TestLocal_RequiresRelaxedForNetworkDisabled` | Verify local requires relaxed for network=disabled |
| `TestLocal_RequiresRelaxedForPathRestrictions` | Verify local requires relaxed for strict path policy |
| `TestAutoSelect_PrefersBoxsh` | Verify auto-selection prefers boxsh when available |
| `TestAutoSelect_FallsBackToLocal` | Verify fallback to local with relaxed |
| `TestFailClosed_NoBackend` | Verify error when no backend supports policy |

---

## Version-Specific Notes

### boxsh 2.0.1 (Current)

- ✅ Full filesystem COW overlay
- ✅ Full process isolation
- ✅ Full network disabled/allow_all
- ❌ Whitelist mode not supported
- ❌ Per-request network filtering not supported

### boxsh 2.x (Future)

- Expected: Whitelist mode support
- Expected: Per-socket filtering

### local (Any Version)

- Advisory enforcement only
- Requires `Relaxed=true` for most security policies
- Suitable for development, testing, Windows

---

## Migration Path for New Policies

When adding new policy features:

1. Add to `Policy` struct with backwards compatibility
2. Define compatibility level for each backend
3. Update this matrix
4. Add `Supported()` check to relevant factories
5. Add tests for fail-closed behavior
6. Document observability events
