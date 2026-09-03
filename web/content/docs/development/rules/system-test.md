---
title: System testing
description: Subprocess system-test suite that boots the real stellad over TCP against embedded PostgreSQL.
---

The system suite boots the real `stellad` binary as a subprocess and drives it over TCP, HTTP, and SSE, against embedded PostgreSQL, with the shared scripted fake Anthropic provider in `test/fakeanthropic/`. It proves seams a single-process Go test cannot reach: process startup and migration, real HTTP authentication, SSE transport, cross-request flows, and asynchronous workers. The suite lives under the `system` build tag in `test/system/`, so plain `go test ./...` does not discover it.

## Test taxonomy

Test at the lowest layer that can prove the behavior. Each layer costs more to run and keep deterministic, so climb only when the layer below cannot reach the seam.

| Layer                      | What runs                                              | Launcher       | Command                            | Browser |
| -------------------------- | ------------------------------------------------------ | -------------- | ---------------------------------- | ------- |
| **In-process integration** | Go tests and live DB                                   | Go test        | `mise run test`                    | no      |
| **System**                 | Real subprocess, TCP, SSE, restart and workers         | system harness | `mise run system-test`             | no      |
| **Browser E2E**            | UI, API, DB and functional flows                       | `test/testbed` | `mise run test:e2e`                | yes     |
| **Perf**                   | Playwright measurements against testbed and fake model | perf project   | `mise run perf:measure -- <label>` | yes     |
| **Eval**                   | Agent behavior benchmark                               | Harbor         | `mise run eval:loop`               | no      |

For manual API exploration, see `api-test.md`. For browser behavior, see `web-ui-test.md`; for measurements, see `web-perf-test.md`.

## Running the suite

```bash
mise run system-test
```

The task depends on `pg:runtime:download` and `build`, then runs `go test -tags system -count=1 -timeout 15m ./test/system/...`. It downloads the embedded PostgreSQL runtime when needed, builds `dist/bin/stellad` (never `go run`), boots one subprocess on a loopback TCP port, lets it migrate its handed-over database, runs ordered journeys, then tears down the subprocess and cluster.

The suite runs only where the embedded PostgreSQL runtime is published: linux/amd64, linux/arm64 on the supported Debian/Ubuntu sources, and macOS arm64. Unsupported hosts skip before acquiring resources. Release validation uses a supported Ubuntu runner; do not expand the platform list in the suite independently of `internal/db`.

## Suite architecture

`TestSystem` owns one server subprocess and database for the whole run. Journeys are ordered subtests, never `t.Parallel()`, so the shared server and database are used sequentially:

- `readiness`: migration, TCP binding, and readiness.
- `startup_and_auth`: registration, session authentication, PAT minting and revocation.
- `chat_sse`: one chat turn consumed as a live SSE stream.
- `chat_disconnect_resume`: disconnect and replay through the read-only events stream without a second model request.
- `agent_provider_credentials`: agents exercise encrypted provider overrides and live rotation/delete.
- `image_history` and `view_image_tool_history`: uploaded media, canonical history, baseline VLM behavior, and the production `view_image` tool loop.
- `tool_smoke_canary`: Code Mode calls builtin tools and pages the catalog through `tools.search`; builtin surface coverage remains an in-process gate.
- `chat_provider_error`: an in-band provider error finishes with `[DONE]` instead of hanging.
- `webhook_sync_persistent` and `github_webhook_compatibility`: capability calls, durable sessions, and GitHub-shaped async delivery.
- `goal_lifecycle`: asynchronous decomposition, execution, and acceptance through the dispatcher and River workers.
- `scheduler_one_time_job_survives_forced_restart`: a persisted job runs exactly once after a forced restart on the same database.
- `graceful_drain`: SIGTERM drains an in-flight turn, flips readiness, persists accepted work, and exits cleanly. It runs last because it consumes the shared server.

Every fixture (provider, agent, user, goal) is scoped by the harness `runID`; only bootstrap identity and its cookie jar are shared. The HTTP client has no timeout, so every request, including SSE, must carry a context deadline. `TestHarnessEarlyExit` separately proves that a subprocess dying during startup is detected quickly without PostgreSQL.

The subprocess environment is an explicit allowlist. Do not let local `STELLA_*`, `OTEL_*`, or `AUTH_*` settings leak into a run and make it nondeterministic. The system harness keeps process-group ownership, forced kills, restart identity, and startup-failure detection separate from testbed, which is a disposable application fixture rather than a lifecycle oracle.

## The fake Anthropic provider

No model traffic leaves the host. `test/fakeanthropic` is an in-test-process `httptest.Server`; the test-created provider points at its loopback `base_url`, and every recorded request is therefore a model request made by the system.

The fake branches only on stable request fields, never prompt prose. FIFO scripts (`enqueueText`) replay ordered turns for chat and media journeys. `enqueueGoalControl` matches the advertised `goal_control` action enum for `decompose` and `submit`, rather than relying on arrival order. Unscripted requests fail immediately, and cleanup fails if a scripted response remains unconsumed. The command wrapper at `test/fakeanthropic/cmd` is used by browser perf runs; the plain package is used directly by system tests.

### Reaching goal_control under Code Mode

Code Mode is the only tool path, so `goal_control` is cold: it is absent from the provider-facing tool list. The discriminator moved into the per-attempt Code catalog. The fake reaches it in two steps:

1. **Probe.** Answer a fresh Goal turn with a `code` call that invokes `tools.describe("goal_control")` and returns `inputSchema.properties.action.enum`. Nothing terminal runs, so the attempt must ask again.
2. **Stage.** The next request carries that enum. The fake selects the non-`fail` action and serves the corresponding `tools.invoke("goal_control", ...)` script, recording the stage when it serves it.

The fake reads only markers it planted in its own scripted return values, never prompt prose. Prompt edits therefore do not silently change the matching rule.

### Goal trailing-turn gotcha

A Goal tool loop may make a racy follow-up `/v1/messages` call after the terminal `goal_control` invocation. Any attempt may or may not take that turn, so exact call counts are invalid. The fake keys on the action enum, serves a benign `end_turn` response for a trailing call, and records a stage when its script is served, not when a marker returns, because the attempt can end before the marker arrives.

Assert that all scripts were consumed and no unscripted request occurred. Do not assert an exact number of provider calls.

## Diagnostics

Logs survive under `dist/logs/system-test/server-<runid>-g<generation>-a<attempt>.log`. Restart journeys retain every process generation. Failure output includes the relevant log tail; goal and scheduler-restart journeys also dump durable rows and fake request logs, so stuck asynchronous work is diagnosable without a rerun.

## When to add a system-test journey

Add a journey only for a seam a lower layer cannot reach: process startup, real HTTP authentication, SSE or another streaming transport to a client, a flow spanning multiple requests, or an asynchronous worker such as the dispatcher, scheduler, or River jobs. Pure logic, single-handler behavior, and DB invariants reachable in-process belong in Go tests. Functional browser coverage belongs in `test/e2e`, and performance scenarios belong in `test/e2e/perf`. A journey requiring production-code changes, an unsupported-host expansion, or external network access needs design review before landing.
