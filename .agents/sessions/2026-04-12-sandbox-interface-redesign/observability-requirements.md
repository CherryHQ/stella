# Phase 1: Observability Requirements

This document defines the observability events, logs, and metrics required for the sandbox interface redesign. Ensures visibility into policy enforcement, relaxed mode, exceptions, and failures.

## Event Types

### 1. Policy Denied Events

Emitted when a policy violation is blocked.

```go
type PolicyDeniedEvent struct {
    Timestamp   time.Time     `json:"timestamp"`
    SessionID   string        `json:"session_id"`
    Policy      Policy        `json:"policy"`
    Operation   string        `json:"operation"`   // "exec", "read", "write", "http", etc.
    Resource    string        `json:"resource"`    // path, URL, command attempted
    Reason      string        `json:"reason"`      // why it was denied
    Backend     string        `json:"backend"`     // which backend enforced
}
```

**Log Level**: ERROR  
**When**: Every time a policy check fails and blocks an operation  
**Example**:
```json
{
  "timestamp": "2026-04-12T10:30:00Z",
  "session_id": "sess_abc123",
  "policy": {"network": {"mode": "disabled"}},
  "operation": "http_request",
  "resource": "https://example.com/api",
  "reason": "network disabled by policy",
  "backend": "boxsh"
}
```

### 2. Relaxed Mode Events

Emitted when a session runs with reduced policy enforcement.

```go
type RelaxedModeEvent struct {
    Timestamp   time.Time     `json:"timestamp"`
    SessionID   string        `json:"session_id"`
    Policy      Policy        `json:"requested_policy"`
    Backend     string        `json:"backend"`
    Reason      string        `json:"reason"`      // why relaxed was selected
    Warnings    []string      `json:"warnings"`    // specific policy relaxations
}
```

**Log Level**: WARN  
**When**: Session created with `Policy.Relaxed=true` or backend downgrades policy  
**Example**:
```json
{
  "timestamp": "2026-04-12T10:30:00Z",
  "session_id": "sess_def456",
  "requested_policy": {"network": {"mode": "whitelist"}},
  "backend": "local",
  "reason": "local backend cannot enforce whitelist",
  "warnings": ["network whitelist treated as advisory only"]
}
```

### 3. Unsupported Backend Events

Emitted when no backend can satisfy policy requirements.

```go
type UnsupportedBackendEvent struct {
    Timestamp   time.Time     `json:"timestamp"`
    Policy      Policy        `json:"policy"`
    Attempted   []string      `json:"attempted_backends"`
    Error       string        `json:"error"`
}
```

**Log Level**: ERROR  
**When**: `Factory.CreateSession` fails to find compatible backend  
**Example**:
```json
{
  "timestamp": "2026-04-12T10:30:00Z",
  "policy": {"network": {"mode": "whitelist", "allowlist": ["10.0.0.0/8"]}},
  "attempted_backends": ["boxsh", "local"],
  "error": "no backend supports network whitelist mode"
}
```

### 4. Exception Used Events

Emitted when a known exception path (bypass) is used.

```go
type ExceptionUsedEvent struct {
    Timestamp   time.Time     `json:"timestamp"`
    ExceptionID string        `json:"exception_id"`  // e.g., "EX-001"
    Path        string        `json:"path"`          // file:line
    Operation   string        `json:"operation"`     // what was done
    Owner       string        `json:"owner"`
    Reason      string        `json:"reason"`
}
```

**Log Level**: WARN  
**When**: Code in exceptions register executes  
**Example**:
```json
{
  "timestamp": "2026-04-12T10:30:00Z",
  "exception_id": "EX-001",
  "path": "internal/agent/runner/builtin/embed.go:16",
  "operation": "os.RemoveAll",
  "owner": "internal/agent/runner",
  "reason": "One-time builtin skills extraction at startup"
}
```

### 5. Session Lifecycle Events

```go
type SessionCreatedEvent struct {
    Timestamp   time.Time     `json:"timestamp"`
    SessionID   string        `json:"session_id"`
    Backend     string        `json:"backend"`
    Policy      PolicySummary `json:"policy"`      // redacted/summary version
}

type SessionClosedEvent struct {
    Timestamp   time.Time     `json:"timestamp"`
    SessionID   string        `json:"session_id"`
    Duration    time.Duration `json:"duration"`
    Reason      string        `json:"reason"`      // "explicit_close", "liveness_lost", etc.
}
```

**Log Level**: INFO  
**When**: Session start/end  

### 6. Execution Events (Debug)

```go
type ExecStartedEvent struct {
    Timestamp   time.Time     `json:"timestamp"`
    SessionID   string        `json:"session_id"`
    Command     string        `json:"command"`     // truncated
    Timeout     time.Duration `json:"timeout_ms"`
}

type ExecFinishedEvent struct {
    Timestamp   time.Time     `json:"timestamp"`
    SessionID   string        `json:"session_id"`
    ExitCode    int           `json:"exit_code"`
    Duration    time.Duration `json:"duration_ms"`
    Truncated   bool          `json:"output_truncated"`
}
```

**Log Level**: DEBUG  
**When**: Command execution start/end  

## Metrics

### Counter Metrics

| Metric Name | Labels | Description |
|-------------|--------|-------------|
| `sandbox_sessions_created_total` | `backend` | Sessions created by backend |
| `sandbox_sessions_closed_total` | `backend`, `reason` | Sessions closed |
| `sandbox_policy_denied_total` | `operation`, `backend` | Denied operations |
| `sandbox_relaxed_mode_total` | `backend`, `reason` | Relaxed mode selections |
| `sandbox_exceptions_used_total` | `exception_id` | Exception path usage |
| `sandbox_exec_total` | `backend` | Commands executed |
| `sandbox_http_total` | `backend` | HTTP requests |
| `sandbox_fs_ops_total` | `operation`, `backend` | Filesystem operations |

### Gauge Metrics

| Metric Name | Labels | Description |
|-------------|--------|-------------|
| `sandbox_active_sessions` | `backend` | Currently active sessions |
| `sandbox_backend_healthy` | `backend` | Backend health (1=healthy, 0=unhealthy) |

### Histogram Metrics

| Metric Name | Labels | Description |
|-------------|--------|-------------|
| `sandbox_exec_duration_seconds` | `backend` | Command execution duration |
| `sandbox_http_duration_seconds` | `backend` | HTTP request duration |
| `sandbox_session_duration_seconds` | `backend` | Total session lifetime |

## Logging Configuration

### Log Levels by Event

| Event Type | Level | Production | Development |
|------------|-------|------------|-------------|
| Policy Denied | ERROR | Always | Always |
| Unsupported Backend | ERROR | Always | Always |
| Relaxed Mode | WARN | Always | Always |
| Exception Used | WARN | Always | Always |
| Session Created | INFO | Yes | Yes |
| Session Closed | INFO | Yes | Yes |
| Exec Started | DEBUG | No | Yes |
| Exec Finished | DEBUG | No | Yes |

### Structured Log Format

All events use structured logging:

```go
// Error events
slog.Error("sandbox.policy_denied",
    "session_id", sessID,
    "operation", op,
    "resource", resource,
    "reason", reason,
    "backend", backend,
)

// Warning events  
slog.Warn("sandbox.relaxed_mode",
    "session_id", sessID,
    "backend", backend,
    "warnings", warnings,
)

// Info events
slog.Info("sandbox.session_created",
    "session_id", sessID,
    "backend", backend,
    "policy_mode", policy.Network.Mode,
)

// Debug events
slog.Debug("sandbox.exec_started",
    "session_id", sessID,
    "command", truncate(cmd, 100),
)
```

## Observability Implementation

### Observer Interface

The observer contract covers lifecycle, policy, exception, and execution events. Filesystem and HTTP metrics are emitted directly inside `Host` implementations using the metric names defined above; they do not require separate observer callbacks.

```go
// Observer receives sandbox observability events
type Observer interface {
    // Policy enforcement
    PolicyDenied(ctx context.Context, e PolicyDeniedEvent)
    
    // Mode selection
    RelaxedModeSelected(ctx context.Context, e RelaxedModeEvent)
    UnsupportedBackend(ctx context.Context, e UnsupportedBackendEvent)
    
    // Exceptions
    ExceptionUsed(ctx context.Context, e ExceptionUsedEvent)
    
    // Lifecycle
    SessionCreated(ctx context.Context, e SessionCreatedEvent)
    SessionClosed(ctx context.Context, e SessionClosedEvent)
    
    // Execution (debug)
    ExecStarted(ctx context.Context, e ExecStartedEvent)
    ExecFinished(ctx context.Context, e ExecFinishedEvent)
}
```

### Default Observer (Logs + Metrics)

```go
type DefaultObserver struct {
    // Prometheus metrics
    sessionsCreated prometheus.Counter
    policyDenied    prometheus.Counter
    // ... other metrics
}

func (o *DefaultObserver) PolicyDenied(ctx context.Context, e PolicyDeniedEvent) {
    // Log
    slog.ErrorContext(ctx, "sandbox.policy_denied",
        "session_id", e.SessionID,
        "operation", e.Operation,
        "reason", e.Reason,
    )
    
    // Metric
    o.policyDenied.WithLabelValues(e.Operation, e.Backend).Inc()
}
```

### Null Observer (Testing)

```go
type NullObserver struct{}

func (NullObserver) PolicyDenied(context.Context, PolicyDeniedEvent) {}
func (NullObserver) RelaxedModeSelected(context.Context, RelaxedModeEvent) {}
// ... all no-ops
```

## Alerting Rules

### Critical Alerts (Page)

```yaml
# High rate of policy denials
- alert: SandboxHighDenialRate
  expr: rate(sandbox_policy_denied_total[5m]) > 10
  for: 5m
  labels:
    severity: critical
  annotations:
    summary: "High sandbox policy denial rate"

# Backend health failure
- alert: SandboxBackendUnhealthy
  expr: sandbox_backend_healthy == 0
  for: 1m
  labels:
    severity: critical
  annotations:
    summary: "Sandbox backend unhealthy"
```

### Warning Alerts (Notify)

```yaml
# Relaxed mode usage
- alert: SandboxRelaxedMode
  expr: increase(sandbox_relaxed_mode_total[1h]) > 0
  labels:
    severity: warning
  annotations:
    summary: "Sandbox running in relaxed mode"
    
# Exception path usage
- alert: SandboxExceptionUsed
  expr: increase(sandbox_exceptions_used_total[1h]) > 0
  labels:
    severity: warning
  annotations:
    summary: "Sandbox exception path used"
```

## Audit Requirements

For security-sensitive deployments:

1. **Log Retention**: Policy denied events retained for 90 days minimum
2. **Access Control**: Observability events include session/user context where available
3. **Tamper Resistance**: Critical events (denied, relaxed) should be structured for log aggregation
4. **Privacy**: Command arguments in debug logs truncated to 100 chars; full args never logged

## Testing Observability

### Event Capture (Tests)

```go
type captureObserver struct {
    events []any
}

func (c *captureObserver) PolicyDenied(_ context.Context, e PolicyDeniedEvent) {
    c.events = append(c.events, e)
}

// Test verification
func TestPolicyDenialObserved(t *testing.T) {
    cap := &captureObserver{}
    host := newTestHost(withObserver(cap))
    
    // Attempt denied operation
    _, err := host.Exec(ctx, "curl https://example.com", opts)
    require.Error(t, err)
    
    // Verify event captured
    require.Len(t, cap.events, 1)
    denied := cap.events[0].(PolicyDeniedEvent)
    assert.Equal(t, "exec", denied.Operation)
}
```

### Metric Verification (Tests)

```go
func TestMetricsRecorded(t *testing.T) {
    registry := prometheus.NewRegistry()
    obs := NewMetricsObserver(registry)
    
    // Emit events
    obs.SessionCreated(ctx, SessionCreatedEvent{Backend: "boxsh"})
    obs.SessionCreated(ctx, SessionCreatedEvent{Backend: "local"})
    
    // Verify metrics
    families, err := registry.Gather()
    // Assert counter values
}
```

## Dashboards

### Sandbox Overview Dashboard

| Panel | Metric | Description |
|-------|--------|-------------|
| Active Sessions | `sandbox_active_sessions` | Current sessions by backend |
| Session Rate | `rate(sandbox_sessions_created_total[5m])` | Session creation rate |
| Denial Rate | `rate(sandbox_policy_denied_total[5m])` | Policy enforcement rate |
| Relaxed Mode % | `sandbox_relaxed_mode_total / sandbox_sessions_created_total` | % sessions in relaxed mode |
| Backend Health | `sandbox_backend_healthy` | Backend up/down status |

### Drill-Down Dashboard

- Top denied operations
- Top exception paths used
- Session duration distribution
- Command execution latency
- HTTP request latency
