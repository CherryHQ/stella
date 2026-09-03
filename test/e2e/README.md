# End-to-end tests

Playwright specs that drive a real `stellad` through the disposable testbed: embedded PostgreSQL, HTTP API, embedded web UI, and direct database assertions. Bun is the package manager and launcher.

```bash
mise run build
mise run test:e2e                         # all functional specs
mise run test:e2e -- --grep-invert @model  # omit real-model turns
mise run perf -- smoke                    # render and load measurements
cd test/e2e && bun run playwright test 1-catalog.spec.ts
```

The global setup starts one testbed on `STELLA_E2E_PORT` (default `25777`) with `STELLA_MCP_ALLOW_PRIVATE_ENDPOINTS=1` so loopback fixtures can be registered; global teardown stops it and cleans the testbed port. Specs run serially on one worker because they share that instance.

Agent turns use a real model. Put `OPENAI_API_KEY`, `OPENAI_BASE_URL`, and `OPENAI_MODEL` in the repo root `.env` (gitignored) or the environment. Perf setup requests the testbed's embedded fake model instead.

## Layout

- `lib/testbed.ts` starts/stops the testbed and reads credentials.
- `lib/api.ts` is a bearer-authenticated JSON and SSE client.
- `lib/db.ts` connects to testbed PostgreSQL for row-level assertions.
- `lib/provider.ts` registers real `OPENAI_*` credentials as a provider.
- `lib/agent.ts` creates model-bound agents, sends turns, and reads transcripts.
- `lib/mcp-fixture.ts`, `oauth-fixture.ts`, and `registry-fixture.ts` provide local remote-service fixtures.
- `lib/fixtures.ts` extends Playwright `test` with `admin`, `user`, `db`, and `loginAsAdmin`.
- `1-catalog.spec.ts` through `5-web.spec.ts` are the functional MCP journeys.

Failures keep traces under `test-results/`; open one with `bun run playwright show-trace <trace.zip>`.

## Performance

`perf/` contains Playwright render and load measurements. `mise run perf -- <label>` runs both specs, starts the testbed with `--fake-model`, and writes ignored JSON under `test/e2e/perf/results/`. Render covers long history, streaming, and typing; load covers huge history and file-heavy history. Set `REPS`, `HUGE_TURNS`, `IMG_COUNT`, `PDF_COUNT`, `PERF_STREAM_CHUNKS`, and `PERF_STREAM_INTERVAL_MS` to control a run.
