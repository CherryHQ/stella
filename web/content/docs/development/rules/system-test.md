---
title: System testing
description: Subprocess system-test suite that boots the real stellad over TCP against embedded PostgreSQL.
---

The system suite boots the real `stellad` binary as a subprocess and drives it
over TCP — HTTP and SSE — against an embedded PostgreSQL cluster, with a scripted
fake Anthropic provider standing in for the model. It proves the seams a
single-process Go test cannot reach: process startup and migration, real HTTP
authentication, SSE transport to a client, cross-request flows, and asynchronous
workers (the goal dispatcher and its River jobs). The whole suite lives under the
`system` build tag in `test/system/`, so a plain `go test ./...` never discovers
it.

## Test taxonomy

Test at the lowest layer that can prove the behavior. Each layer up costs more to
run and to keep deterministic, so climb only when the layer below cannot reach
the seam.

| Layer                      | What runs                                                                            | Command                | Browser |
| -------------------------- | ------------------------------------------------------------------------------------ | ---------------------- | ------- |
| **In-process integration** | Go tests, in-process, against a live Postgres — no server subprocess                 | `mise run test`        | no      |
| **System**                 | The real `stellad` subprocess over TCP + embedded PostgreSQL, scripted fake provider | `mise run system-test` | no      |
| **Browser E2E**            | Full user path `browser → API → DB`                                                  | see `web-ui-test.md`   | yes     |

For manual, exploratory driving of a running server with `curl` and DB
assertions, see `api-test.md`; the system suite is the automated, repeatable form
of that same subprocess coverage.

## Running the suite

```bash
mise run system-test
```

The task depends on `pg:runtime:download` and `build`, then runs
`go test -tags system -count=1 -timeout 15m ./test/system/...`. It:

- downloads the embedded PostgreSQL runtime if it is not already present;
- builds `dist/bin/stellad` (the suite execs this binary, never a `go run`);
- boots one server subprocess bound to a real loopback TCP port, backed by an
  embedded PostgreSQL cluster the subprocess migrates itself;
- runs the ordered journeys, then tears the subprocess and cluster down.

## Supported platforms

The suite runs only where the embedded PostgreSQL runtime is published; that
platform set is owned by `internal/pgruntime`, and the suite must never duplicate
it. On any other host `skipUnsupportedHost` skips the suite before it acquires a
resource — it does not fail. Published platforms:

- **linux/amd64** and **linux/arm64** for the Debian/Ubuntu runtime sources
  `bookworm`, `noble`, and `trixie`;
- **macOS arm64**.

On an unsupported development host, point `STELLA_DATABASE_URL` at an external
PostgreSQL with `pg_search` and `pgvector` to run the server manually, or file an
issue for the platform. The tag-triggered release workflow pins a supported
Ubuntu runner and invokes `mise run system-test`; its runtime-download dependency
fails before the suite if that runner ever becomes unsupported, so publication
cannot treat an unsupported-platform skip as a pass.

## Suite architecture

`TestSystem` owns the single server subprocess and its database for the whole
run. Journeys are **ordered subtests**, never `t.Parallel()`, so one shared server
and one shared database serve them all in sequence:

- `readiness` — the subprocess migrated the handed-over database, bound a TCP
  listener, and reports ready.
- `startup_and_auth` — bootstrap registration and session-authenticated access.
- `chat_sse` — one chat turn end to end, consumed as a live SSE stream.
- `chat_disconnect_resume` — disconnect the initiating message stream mid-turn,
  reconnect through the read-only events stream, replay the first half, and
  finish without a second model request.
- `agent_provider_credentials` — three Agents share one global fake Provider;
  two send distinct encrypted overrides, one sends the global key, and live
  rotation/delete changes the next request without changing Agent model state.
- `image_history` — an uploaded image reaches the fake provider for baseline rendering and the active answer turn, persists as canonical media plus that exact baseline, projects as text with no pixels on the next answer request, and reloads byte-identically through the authenticated history endpoint.
- `view_image_tool_history` — the fake answer model calls the production `view_image` tool on an uploaded PNG; the resulting tool image passes through the fake baseline VLM, remains pixel-active for the tool-loop follow-up, persists as canonical tool history, and becomes baseline-only on the next user turn.
- `tool_smoke_canary` — one Code Mode call chains three builtin tools over HTTP
  and SSE, each child reporting its own settled result frame, and the daemon's
  assembled tool catalog is paged out through `tools.search`. Coverage of the
  builtin tool surface is an in-process gate (`TestToolSmoke` in `cmd/stellad`),
  closed by strict equality with three documented protocol exceptions; this
  journey proves only the transport around it and must never grow into a second
  coverage list.
- `chat_provider_error` — a failed model call surfaced as an in-band error frame
  on the send stream, then finish and [DONE] — the turn never hangs.
- `webhook_sync_persistent` — two unauthenticated capability calls return
  synchronous fake-model output and reuse one durable Webhook session.
- `goal_lifecycle` — a Goal driven from creation to autonomous acceptance by the
  dispatcher's async workers.
- `github_webhook_compatibility` — a GitHub-shaped JSON push delivery, sent
  without a cookie jar to an ordinary personal Webhook, receives async `202` and
  reaches the fake model exactly once with its payload intact.
- `scheduler_one_time_job_survives_forced_restart` — a future one-time chat job
  is persisted, the server is force-killed before it is due, and a replacement
  process on the same database executes and retires that exact job once.
- `graceful_drain` — SIGTERM with a turn pinned in flight: `/readyz` flips away
  from ready, attach and send observers detach promptly, the server-owned turn
  persists its complete reply under accepted-work drain, and the process exits 0. Runs last, since it consumes the shared server.

`startup_and_auth` also covers the personal-access-token bearer lifecycle: a
session mints a PAT, the token alone authenticates an ordinary API route with
its owner's current authority, and revoking it makes the same bearer fail closed.

Every fixture (provider, agent, user, goal) is scoped by the harness `runID`, so
no journey depends on another's business data — a shared bootstrap user and cookie
jar are the only reuse. The shared HTTP client has **no timeout**; every request,
SSE included, must carry a `context` deadline instead. `TestHarnessEarlyExit` is a
separate top-level test that proves a subprocess dying during startup is detected
fast, and needs neither PostgreSQL nor the runtime.

The subprocess environment is an explicit allowlist, not the developer's
inherited environment, so local `STELLA_*`/`OTEL_*`/`AUTH_*` settings cannot leak
in and make a run nondeterministic.

## The fake Anthropic provider

No model traffic leaves the host: the fake is an in-test-process `httptest.Server`,
and the subprocess reaches it only because the test-created provider's `base_url`
is the fake's loopback address. Every request the fake records is therefore every
model request the system made.

The fake **never branches on prompt prose** — only stable request fields (model,
tool names, the `goal_control` action enum) select a response, so ordinary prompt
edits can never turn into a system-test failure. It has two scripting modes:

- **FIFO turns** (`enqueueText`) — an ordered queue replayed in arrival order;
  used by `chat_sse`, `image_history`, and `view_image_tool_history`. An unscripted request fails the test.
- **goal_control variant match** (`enqueueGoalControl`) — responses keyed by the
  `goal_control` action the server advertises in the request's tool schema
  (`decompose`, `submit`), matched on that stable field rather than arrival order;
  used by `goal_lifecycle`.

Cleanup fails the test if any scripted response went unconsumed, catching a system
that made fewer model calls than the journey assumed.

### Goal trailing-turn gotcha

A Goal attempt's agent tool loop may fire a **racy tool-result follow-up call**
after the terminal `goal_control` tool_use, so the number of `/v1/messages` calls
per attempt is nondeterministic (the measured `goal_lifecycle` sequence is
`decompose, decompose, submit, submit`). This is exactly why the goal mode keys on
the action enum instead of arrival order: each stage's tool_use is served once,
and a same-stage follow-up turn gets a benign `end_turn` text so the loop
terminates without consuming another stage's script. Assert "all scripts consumed
and no unscripted request," never an exact call count.

## Diagnostics

Server logs are written to
`dist/logs/system-test/server-<runid>-g<generation>-a<attempt>.log` in the repo
(they survive the run), so restart journeys retain every process generation and
a failure message can always point at a live file. Failures attach a tail of the
relevant log; the goal and scheduler-restart journeys additionally dump their
durable rows and fake request logs, so stuck async work is diagnosable without a
rerun.

## When to add a system-test journey

Add a journey only for a **seam a lower layer cannot reach**: process startup,
real HTTP authentication, SSE (or other streaming) transport to a client, a flow
that spans multiple requests, or an asynchronous worker (dispatcher, scheduler,
River jobs). Everything else — pure logic, single-handler behavior, DB invariants
reachable in-process — belongs in an in-process Go test at the lowest sufficient
layer. A new journey that needs production-code changes, a new unsupported-host
expansion, or any external network dependency reopens design review before it
lands.
