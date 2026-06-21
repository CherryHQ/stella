---
title: Session Compaction
---

> This section is for developers contributing to Stella.

## Status

Implemented — `internal/agent/service.go` and `internal/agent/runtime/chat.go` orchestrate compaction through the active `memory.Provider`; channels expose `/compact`.

## Problem

LLM runners have finite context windows. As a session accumulates messages, the
history grows unbounded. Eventually the runner's context fills up, leading to
degraded responses or hard failures. Long-lived sessions (Telegram chats,
multi-day coding sessions) hit this wall quickly.

We need a way to compress old history without losing critical context, and
without requiring the user to manually start a new session.

## Design: Handoff-Style Compaction

The core idea is borrowed from the "handoff" pattern used by coding agents: when
context gets too large, ask the LLM itself to produce a self-contained summary,
then replace the old history with that summary plus a small tail of recent user
turns.

```
Before compaction:
┌──────────────────────────────────┐
│ session header                   │
│ message 1                        │
│ message 2                        │
│ ...                              │
│ older turn                       │
│ fresh-tail turn 1                │  ← kept verbatim
│ ...                              │
│ fresh-tail turn 6                │
└──────────────────────────────────┘

After compaction:
┌──────────────────────────────────┐
│ session header                   │
│ compaction { summary }           │  ← LLM-generated summary
│ fresh-tail turn 1                │  ← last 6 user turns kept
│ ...                              │
│ fresh-tail turn 6                │
└──────────────────────────────────┘
```

The summary is structured so the runner can continue the conversation without
the original messages — it contains the goal, progress, decisions, files
changed, current state, blockers, and next steps.

## Architecture

```
Channel (/compact or auto)
    |
    v
agent.Service.CompactSession(ctx, validatedSessionInfo)
    |
    ├─ Runtime.Memory()          use the active memory provider
    │
    ├─ memory.Compactor.Compact  create summaries and preserve the fresh tail
    │
    └─ Runtime.CloseSession      next Chat() recreates runner with compacted context
```

### Token Estimation

Token estimation sums the byte length of stored messages and divides by 4
(rough heuristic: ~4 bytes per token for English text with JSON overhead).

### Compaction Prompt

The prompt asks the runner to produce a structured summary:

- **Goal** — original session objective
- **Progress** — what was completed or partially done
- **Key Decisions** — decisions and rationale
- **Files Changed** — paths with context
- **Current State** — what works, what doesn't
- **Blockers / Gotchas** — issues or edge cases
- **Next Steps** — concrete, actionable tasks

Guidelines enforce self-containment: the summary must make sense to a reader
with zero access to the prior conversation.

### Storage Format

Compaction summaries are stored as messages in PostgreSQL via
`memory.Engine`. On `Load()`, the engine converts the compaction entry into a
pair of `ai.Message`s — a user message containing the summary and an assistant
acknowledgment — so the runner sees it as normal conversation history.

## Triggers

### Manual — `/compact` command

Available in both CLI and Telegram:

```
/compact
```

Resolves the current session through the channel/session policy and calls `agent.Service.CompactSession()`. Returns the summary text to the user.

### Automatic — token threshold

`runtime.Runtime.Chat()` checks whether the validated session needs compaction before each message. If the estimated token count exceeds the threshold, compaction runs automatically before the user's message is sent.

If auto-compaction fails, the system logs a warning and continues with the full
history — it never blocks the user's message.

## Configuration

Compaction settings are stored in the database settings table under the
`compaction` key as JSON. They can be configured through the Web UI or
the settings API.

Fields:

- `max_tokens`: auto-compact threshold as an absolute token count (default: 80,000; set to `-1` to disable)
- `keep_tail`: recent user turns to preserve verbatim (default: 6)

Both fields have defaults applied via `CompactionConfig.WithDefaults()`.

Setting `max_tokens` to `-1` disables automatic compaction; `/compact`
still works manually.

## Stateful Runners

Some runners (like the Pi subprocess) maintain their own context in-process and
ignore the `history` parameter passed to `Chat()`. For these runners, killing
the process after compaction would destroy live context for no benefit — the new
process can't replay the compacted history anyway.

Runners signal this by implementing the optional `runner.Stateful` interface:

```go
type Stateful interface {
    Stateful() bool
}
```

When a runner is stateful, `CompactSession()` skips the runner kill. The
compacted history is still written to disk (for crash recovery and session
restore), but the live runner keeps its in-process context intact.

For stateless runners that rebuild context from history, the runner is killed as
before so the next `Chat()` call starts fresh with the compacted history.

## Session Loading

`CompactSession()` uses `getOrCreateRunner()` — the same path as `Chat()` — so
it works even when the session exists only on disk (e.g., after process restart
or for Telegram sessions that haven't been accessed in this process lifetime).
This ensures `/compact` is always available for any persisted session.

## Failure Modes

| Scenario                  | Behavior                                  |
| ------------------------- | ----------------------------------------- |
| Runner fails to summarize | Returns error, session untouched          |
| Database write fails      | Returns error, original data preserved    |
| Auto-compaction fails     | Logs warning, continues with full history |
| Empty summary from runner | Returns error: "empty summary response"   |

All writes go through PostgreSQL transactions for atomicity.
