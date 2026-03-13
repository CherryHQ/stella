---
title: Scheduler System
---
## Status

Implemented — `internal/scheduler/` package with gocron/v2 scheduler, SQLite persistence, and agent tool.

## Overview

Anna supports scheduled task execution so the agent can set reminders, run periodic tasks, and automate recurring work. The scheduler system delegates all scheduling to [gocron/v2](https://github.com/go-co-op/gocron) and adds persistence and an agent-facing tool on top.

## Architecture

```
Agent (via tool call)
    |
    |  add / list / remove
    v
+----------+       +-------------+
| SchedulerTool | ----> |   Service   |
+----------+       +------+------+
                          |
              +-----------+-----------+
              |                       |
     gocron/v2 Scheduler     scheduler_jobs (SQLite)
              |
              v
        OnJobFunc callback
              |
              v
      pool.Chat(ctx, "scheduler:{id}", message)
```

### Package: `internal/scheduler/`

Top-level package (under `internal/`). Five files:

| File | Purpose |
|------|---------|
| `internal/scheduler/job.go` | `Job` and `Schedule` types |
| `internal/scheduler/service.go` | `Service` — gocron wrapper, scheduling, job CRUD |
| `internal/scheduler/heartbeat.go` | Heartbeat polling — decide/execute/notify via LLM |
| `internal/scheduler/persistence.go` | Database persistence (load/save/migrate jobs) |
| `internal/scheduler/tool.go` | `SchedulerTool` — agent tool implementing `tool.Tool` |

### Key Types

**Schedule** defines when a job runs. Exactly one field must be set:

- `cron` — a cron expression (e.g. `"0 9 * * 1-5"` for weekdays at 9am)
- `every` — a Go duration (e.g. `"30m"`, `"2h"`, `"24h"`)
- `at` — an RFC3339 timestamp for a one-time job (e.g. `"2024-01-15T14:30:00+08:00"`)

**Job** is the persisted definition:

```go
type Job struct {
    ID          string    // short UUID
    Name        string    // human-readable name
    Schedule    Schedule  // cron, interval, or one-time
    Message     string    // prompt sent to agent
    SessionMode string    // "reuse" (default) or "new"
    Enabled     bool
    CreatedAt   time.Time
}
```

### Service Lifecycle

1. `scheduler.New(db)` or `scheduler.NewFromPath(dbPath)` — creates scheduler backed by SQLite
2. `service.SetOnJob(fn)` — sets callback (deferred wiring to resolve circular dependency)
3. `service.Start(ctx)` — loads jobs from DB, registers all with gocron, starts scheduler
4. `service.Stop()` — shuts down scheduler (and closes DB if opened via `NewFromPath`)

### Persistence

Jobs are stored in the `scheduler_jobs` table in the shared `memory.db` SQLite database (`~/.anna/workspace/memory.db`). Each mutation (add/remove) is an individual INSERT/DELETE — no full-file rewrites.

On first startup, if a legacy `jobs.json` file exists (from pre-DB versions), jobs are automatically migrated to the database and the file is removed.

### One-Time Jobs

Jobs scheduled with `at` run exactly once at the specified time and are automatically removed from both the scheduler and `jobs.json` after execution. This keeps the job list clean without stale entries.

Behavior details:
- The `at` field must be a valid RFC3339 timestamp with timezone offset
- Timestamps in the past are rejected at creation time
- If Anna restarts and a one-time job's timestamp has already passed, the job is silently skipped (not scheduled) but remains in the database until manually removed
- On successful execution, the cleanup runs asynchronously to avoid blocking the scheduler

### Session Model

Each scheduled job's session behavior is controlled by its `session_mode`:

- **`reuse`** (default) — the job gets a stable session ID `scheduler:{job.ID}`. The agent retains conversational memory across scheduled runs of the same job.
- **`new`** — each execution gets a unique session ID `scheduler:{job.ID}:{timestamp}`. The agent starts fresh every time with no prior context.

## Configuration

Add to `~/.anna/config.yaml`:

```yaml
scheduler:
  enabled: true
```

Scheduler is only active when:
- `scheduler.enabled` is `true`
- `runner.type` is `go` (the Pi runner doesn't support custom tools)

## Agent Tool

The `scheduler` tool is automatically registered with the Go runner when scheduler is enabled. The agent uses it via tool calls with three actions:

### `add` — Create a job

Parameters:
- `name` (required) — human-readable name
- `message` (required) — the instruction to execute on each run
- `cron` — cron expression (use this OR `every` OR `at`)
- `every` — Go duration (use this OR `cron` OR `at`)
- `at` — RFC3339 timestamp for a one-time job (use this OR `cron` OR `every`)
- `session_mode` — `"reuse"` (default) keeps conversation history; `"new"` starts fresh each execution

Example (recurring): _"Set a reminder every 30 minutes to check my email"_ triggers:
```json
{"action": "add", "name": "email check", "message": "Check my email and summarize new messages", "every": "30m"}
```

Example (one-time): _"Remind me at 2:40 PM to check Beijing weather"_ triggers:
```json
{"action": "add", "name": "weather reminder", "message": "Check Beijing weather and send me a summary", "at": "2024-01-15T14:40:00+08:00"}
```

### `list` — List all jobs

No parameters. Returns all scheduled jobs as JSON.

### `remove` — Delete a job

Parameters:
- `id` (required) — job ID from `add` or `list`

## Heartbeat

Heartbeat is a built-in periodic task managed by the scheduler service. It polls a `HEARTBEAT.md` file and uses the LLM to decide whether action is needed, executing instructions and sending results via the notification dispatcher.

### How It Works

1. `SetHeartbeat(cfg, chatFn, notifier)` configures heartbeat on the scheduler service
2. `StartHeartbeat(ctx, every)` schedules the poll loop via `ScheduleEvery`
3. Each tick:
   - Reads the heartbeat file (skips if missing or empty)
   - Sends the content to the fast model for a `skip`/`run` decision (no tools allowed)
   - On `run`, sends the content to the main session for execution
   - Delivers the result via the notification dispatcher

### Configuration

```yaml
heartbeat:
  enabled: false     # default: false
  every: 10m         # poll interval (Go duration)
  file: HEARTBEAT.md # relative to workspace unless absolute
```

Heartbeat only runs in `anna gateway` mode. The fast model is used for the gate decision to minimize cost.

## Wiring

The scheduler system resolves a circular dependency (service needs pool for the callback, runner needs the tool) via deferred wiring in `main.go`:

1. Create `scheduler.Service` with no callback
2. Create `scheduler.NewTool(service)` and pass to runner via `ExtraTools`
3. Create pool with the runner factory
4. Call `service.SetOnJob(...)` with a callback that calls `pool.Chat()`
5. If heartbeat is enabled, call `service.SetHeartbeat(...)` with the chat function and notifier
6. Call `service.Start(ctx)` (or `StartEphemeral` for heartbeat-only mode) in the gateway
7. Call `service.StartHeartbeat(ctx, every)` after channels are wired

## Testing

Tests are in `internal/scheduler/scheduler_test.go` and `internal/scheduler/heartbeat_test.go` covering:

- Add, list, remove lifecycle
- Input validation (empty name, missing schedule, invalid duration, conflicting schedule fields, invalid/past timestamps)
- Remove non-existent job
- Persistence across service restart
- Callback firing on schedule
- One-time job creation and validation
- One-time job fires exactly once and auto-removes
- One-time job with past timestamp skipped on restart
- Tool interface for one-time jobs
- Session mode default, reuse, new, and invalid validation
- Session mode via tool interface
- Full tool interface (add/list/remove via `Execute`)
- Error cases (invalid action, missing ID)
- Heartbeat: skip when file is missing
- Heartbeat: fast model used for decision
- Heartbeat: run decision executes and notifies
- Heartbeat: error when decision uses tools
- Heartbeat: notifier errors propagated

Run with:

```bash
go test -race ./internal/scheduler/
```
