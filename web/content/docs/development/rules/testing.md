---
title: Testing
description: How Stella runs, places, and layers its Go, system, browser, API, and performance tests.
---

Stella keeps coverage in three code locations and exposes two primary test commands, one performance command, and the eval commands. Choose the lowest layer that reaches the seam; higher layers cost more to run and keep deterministic.

## How to run

```bash
mise run test
mise run test:e2e
mise run test:e2e -- --grep-invert @model  # optional fast browser run
mise run perf -- <label>                  # render + load measurements
mise run eval:loop                        # Harbor behavior evaluation
```

`mise run test` runs package Go tests, frontend unit tests, and the subprocess system suite. The system portion downloads the supported embedded PostgreSQL runtime, builds `dist/bin/stellad`, and skips before acquiring resources on an unsupported host. `mise run test:e2e` runs all functional Playwright specs against a disposable testbed. The optional grep-invert command omits titles tagged `@model`; retries are normally zero, and a genuinely unstable spec must declare its own retry policy.

`mise run perf` runs both `test/e2e/perf/render.spec.ts` and `load.spec.ts`, with the testbed's embedded fake model. `mise run testbed:start` and `mise run testbed:stop` remain available for manual API and browser exploration. `test:web`, coverage, race, and `eval:*` tasks remain specialized commands rather than additional functional test layers.

The required PR `Test` check joins two parallel jobs. `test:coverage:race`
runs package race/coverage and frontend tests; `test:system` builds fresh embedded
assets and runs the subprocess journeys without race instrumentation. Failure,
cancellation, or skipping either job fails the required check. Both jobs require
working Linux sandbox namespaces and packaged resources. System logs are uploaded
on failure. `test:packages` runs the non-race package/Web lane in release CI;
`mise run test` remains the complete local suite.

Go build caches are shared by compilation mode (plain, race, Windows). Successful
main and maintained-release branch jobs save a new commit-keyed snapshot; PR and
tag jobs restore compatible snapshots without saving duplicate object caches.
The system job writes the plain cache, the package job writes race, and Windows
writes its own cache. Dependency downloads retain their immutable keys. PR caches
cannot warm other PRs, so latency should be compared separately for cache hits and
misses. A new Go version still requires a cold compilation.

The Docker check builds directly from a clean checkout, with generation owned by
the Dockerfile. main warms the registry builder cache used by PRs and release
amd64 builds. PRs export intermediate layers to their GHA cache, and superseded PR
image runs are cancelled. Cache mounts inside Docker remain local to the builder;
the layer cache does not promise persistent Go or pnpm mount contents.

## Where to write tests

Every behavior has three possible homes:

- **Package Go tests**: deterministic logic, a single handler, DB invariants, and tool behavior that does not require a real server process. Run them with `mise run test`.
- **`test/system`**: a system journey only when a lower layer cannot reach process startup, real HTTP authentication, SSE/streaming transport, a cross-request flow, or an asynchronous worker. These tests run as ordered subtests against one real `stellad` subprocess and embedded PostgreSQL, inside `mise run test`.
- **`test/e2e`**: browser/UI, live API, DB assertions, remote MCP fixtures, and functional user paths. Specs live directly under `test/e2e/`; performance specs are the exception and live under `test/e2e/perf/`.

Do not add a system journey merely to duplicate a unit or browser assertion. Do not turn a performance measurement into a pass/fail functional test.

## Testbed usage

The testbed is the one process launcher for every layer above package tests: the system suite imports it as a Go library, browser and perf runs start it through its CLI. It owns embedded PostgreSQL, a real `stellad` subprocess, fixture identities, and cleanup. It listens on `http://127.0.0.1:25777` by default.

```bash
mise run testbed:start
# use the credentials path printed by the command, without printing its contents
mise run testbed:stop
```

The CLI supports `testbed start --fake-model`. The library equivalent is:

```go
instance, err := testbed.Start(ctx, testbed.Options{
    RepoRoot: repoRoot,
    Port: 25777,
    Bootstrap: true,
    FakeModel: true,
})
defer instance.Stop()
```

`Instance` provides `BaseURL()`, `Credentials()`, `DatabaseURL()`, `Fake()`, `Stop()`, `Kill()`, `Restart()`, and `LogTail(n)`. `Kill` addresses the entire owned process group; `Restart` keeps the same temporary home, database DSN, and vault identity. System tests use `Fake()` to enqueue scripted responses in process. Perf setup uses the fake model URL written to the credentials artifact and does not start another provider process.

The credentials artifact is mode `0600` and contains the admin/user identities, PATs, database DSN, and, when requested, the fake model provider ID and base URL. Keep it in memory, never print it, commit it, or expose `.env` values. Use `admin`, `user`, `db`, and `loginAsAdmin` from `test/e2e/lib/fixtures.ts` in checked-in browser specs. Use `test/e2e/lib/api.ts` for authenticated JSON/SSE calls and `db` for row-level assertions.

Use API calls to create states the API can create, such as draft goals, agents, MCP servers, provider configuration, and scheduler jobs. Direct DB fabrication is for visual verification only. Behavior tests must reach lifecycle states through real paths. Always stop the testbed on every manual exit path. Do not use `~/.stella-dev`, port `25678`, hand-created fixture accounts, manual CDP registration, or an external PostgreSQL server for this workflow.

## Gotchas: system tests

- Journeys are ordered and must not use `t.Parallel()` because one server and DB are shared.
- The HTTP client has no global timeout. Every request, including SSE, needs a context deadline.
- Fixture names are scoped by `runID`; only bootstrap identity and its cookie jar are reused.
- The subprocess environment is an explicit allowlist, so local `STELLA_*`, `OTEL_*`, and `AUTH_*` settings cannot leak into a run.
- The shared fake branches on stable request fields, never prompt prose. FIFO scripts use `enqueueText`; Goal scripts match the `goal_control` action enum with `enqueueGoalControl`.
- Under Code Mode, `goal_control` is cold. The fake first probes `tools.describe("goal_control")`, then stages `tools.invoke("goal_control", ...)` from the returned action enum.
- A Goal attempt may make a racy trailing `/v1/messages` call after terminal `goal_control`. Assert consumed scripts and no unscripted request, never an exact provider call count.
- Server logs survive under `dist/logs/testbed/server-<port>.log`; a restart appends to the same file. A startup failure reports that path with the log tail.
- Add a system journey only for process startup, real auth, streaming transport, cross-request flows, or asynchronous workers. Everything else belongs lower.

## Gotchas: browser E2E

- `test:e2e` uses one worker and a disposable testbed on port `25777`; `@model` is a title tag, not a Playwright project.
- After every meaningful action, assert text/role, URL when relevant, and error state before continuing. Pair UI assertions with `admin` or `db` assertions when verifying writes.
- `codegen` discovers interactions; it does not create fixtures. Copy stable locators into checked-in specs.
- Snapshot refs and locator assumptions become stale after navigation or rerender. Locate again.
- Use API fixtures for accounts and ordinary setup. Use browser registration only when registration is the subject under test.
- Hidden Chrome tabs throttle rendering and rAF. `textContent` is safer than `innerText` for virtualized or `content-visibility` history; count matches when text has no separators.
- The login form uses `username` and `password` placeholders, requires at least 8 characters, and normally redirects to `/agents`.

## Gotchas: API and DB

- `GID` is an integer variable in some shells. `GID="019f..."` can produce `bad math expression`; use `GOAL` or another UUID variable name.
- Sessions live in `ctx_conversation` (`session_id`, `kind`, `archived`), not a `session` table. `ON DELETE RESTRICT` can leave an orphan after a rolled-back write.
- Store and serialize UTC. Compare `created_at` against a UTC run-start timestamp.
- Background work uses River queues such as `stella_goal_tick`; allow several dispatcher ticks, about 2 seconds each, before asserting.
- API responses cover the happy path. Query Postgres for orphan rows, archived flags, counts, and FK invariants, and report expected versus actual.
- For goal lifecycle + agent review, POST a model-configured goal with a leaf judgment contract, then poll `/api/goals/<id>/children` until `acceptance_state=passed`, typically around 30 seconds. Do not replace bounded polling with a fixed sleep.
- Assert `goal_control` traces `decompose` -> `submit` -> `verdict` with `pass=true`, and assert all sessions minted by the run remain `archived=false`. Rollback disposal is covered deterministically by `TestReview_DisposesSessionOnRollback` and `TestDisposeOnRollback`.

## Gotchas: performance

- Measure before an optimization, after each phase, and when a regression is suspected. Compare medians across repetitions on the same machine, never one run.
- Render output is `test/e2e/perf/results/<label>.json`; load output is `load-<label>.json`. Important fields include `longHistory.domNodes/jsHeapMB/fcpMs`, `streaming.durationMs/avgFrameMs/p95FrameMs/maxFrameMs/jankFramesPct`, `typing.avgKeyMs/p95KeyMs`, `hugeLoad.resCount/resTotalKB/resLastEndMs/domNodes/jsHeapMB/fullMountMs`, and `filesLoad.total/loaded/resTotalKB/settleMs`.
- Absolute numbers are machine-dependent. On a 120 Hz display average frame time bottoms out near 8.3 ms; max frame and typing cost may still improve at that floor.
- localhost hides network cost. Inspect resource timing and `filesLoad.resTotalKB` for transfer/cache claims.
- Hidden tabs can make `streaming.frames` zero. Check `document.visibilityState`.
- `performance.memory` is process-level and pre-GC. A leak verdict needs CDP `HeapProfiler.collectGarbage`, `WeakRef` checks, and flat post-GC heap across cycles.
- Seed turns sequentially. Concurrent POSTs to one session can silently drop turns.
- Load fixtures are intentionally reused across labels; render fixtures are reseeded because streaming appends to the session.
- The retired `test/perf` TAP output is not comparable. The checked-in `baseline.json` and `load-baseline.json` are the first Playwright baselines.
