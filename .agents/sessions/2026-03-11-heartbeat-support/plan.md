# Plan: Heartbeat Support

## Overview

Add proactive heartbeat support for `anna gateway` by reusing the existing scheduler infrastructure. A lightweight decision phase runs on the fast model and only promotes to the main heartbeat execution session when the model returns `run`.

### Goals

- Add configurable heartbeat support for gateway mode.
- Reuse existing scheduling and notification plumbing.
- Use the fast model for heartbeat gating and the strong/default model for actual execution.

### Success Criteria

- [ ] Heartbeat can be enabled with config and runs on a configured interval.
- [ ] Heartbeat reads a `HEARTBEAT.md` file and skips cleanly when no action is needed.
- [ ] Heartbeat only enters the execution session when the decision phase returns `run`.
- [ ] Execution results are delivered through the existing notifier path.
- [ ] Tests cover config defaults, service behavior, and integration wiring.

### Out of Scope

- User-managed heartbeat jobs through the cron tool.
- Multiple heartbeat definitions or per-channel heartbeat configs.

## Technical Approach

### Architecture

Introduce a `heartbeat` package with a service that reads a heartbeat file, asks the fast model for a strict JSON `skip`/`run` decision, and, on `run`, sends a prompt into a reserved heartbeat execution session through `agent.Pool`. The execution result is collected and broadcast through the existing `channel.Dispatcher`.

To avoid a second ticker loop, reuse `cron.Service`'s scheduler for the interval trigger while keeping heartbeat semantics separate from regular cron jobs.

### Components

- **`heartbeat.Service`**: Handles heartbeat polling, decision parsing, session execution, and notifications.
- **Config wiring**: Adds `heartbeat.enabled`, `heartbeat.every`, and `heartbeat.file`.
- **Gateway wiring**: Starts the heartbeat service only in gateway mode after notifier registration.

### Data Models

```go
type HeartbeatConfig struct {
	Enabled *bool  `yaml:"enabled" env:"ENABLED"`
	Every   string `yaml:"every"   env:"EVERY"`
	File    string `yaml:"file"    env:"FILE"`
}
```

```go
type Decision struct {
	Action string `json:"action"` // "skip" or "run"
	Reason string `json:"reason,omitempty"`
}
```

### APIs / Interfaces

```go
type chatRunner interface {
	Chat(ctx context.Context, sessionID string, message runner.MessageContent, opts ...agent.ChatOption) <-chan runner.Event
}

type notifier interface {
	Notify(ctx context.Context, n channel.Notification) error
}
```

## Implementation Steps

### Phase 1: Runtime Support

1. Add heartbeat config helpers and service package (files: `config.go`, `heartbeat/service.go`)
2. Wire heartbeat startup into gateway setup using the existing scheduler approach (files: `main.go`)

### Phase 2: Validation and Docs

1. Add heartbeat unit tests and config coverage (files: `heartbeat/service_test.go`, `config_test.go`)
2. Document the feature and configuration (files: `README.md`, `docs/configuration.md`)

## Testing Strategy

### Unit Tests

- Config defaults and path resolution.
- Missing heartbeat file, invalid JSON decision, skip decision, run decision.
- Notification behavior when execution returns text.

### Integration Tests

- Gateway wiring starts heartbeat only when enabled.
- Scheduler callback executes heartbeat poll logic.

### Edge Cases

- Heartbeat file missing or empty.
- Decision model returns malformed JSON.
- Execution produces no final text.
- Notifier has no enabled channels.

## Considerations

### Security

- Heartbeat execution uses the same toolset and permissions as normal gateway agent runs.
- The decision phase forbids tool use and expects JSON-only output.

### Performance

- The fast model is used for the cheap gating decision.
- Full execution is skipped entirely unless the decision is `run`.

### Risks & Mitigations

| Risk | Likelihood | Impact | Mitigation |
| ---- | ---------- | ------ | ---------- |
| Heartbeat repeatedly triggers the same action | Medium | Medium | Keep a dedicated heartbeat decision session so the model sees recent heartbeat history |
| Invalid model output breaks polling | Medium | Low | Parse strictly, log errors, and skip that tick |
| Duplicate notifications | Low | Medium | Deliver execution output centrally and instruct the heartbeat run not to use notify directly |

### Open Questions

- [x] Reuse existing scheduler instead of adding a new ticker loop.
- [x] Use the fast model for decision and the main execution session for `run`.

## Review Feedback

### Round 1

- Keep heartbeat as a distinct service even if it reuses cron scheduling internals.

## Implementation Progress

Implemented:

- Added `heartbeat` runtime support with a fast-model decision gate and reserved execution session.
- Reused the shared cron scheduler by adding a non-persisted `ScheduleEvery` hook.
- Wired heartbeat startup into gateway mode only.
- Added config coverage, service tests, and documentation updates.

Validation:

- `go test ./...`
