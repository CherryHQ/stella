# End-to-end tests

Playwright specs that drive a real `stellad` (the disposable testbed: embedded
PostgreSQL, HTTP API, embedded web UI) through the API, the browser, and direct
database assertions. Bun is the package manager and launcher; Playwright runs
its workers under node.

```bash
mise run build          # dist/bin/stellad with the embedded web UI
mise run test:e2e       # everything under test/e2e
cd test/e2e && bun run playwright test mcp   # one case directory
```

The global setup starts one testbed on `STELLA_E2E_PORT` (default `25777`)
with `STELLA_MCP_ALLOW_PRIVATE_ENDPOINTS=1` so loopback fixtures can be
registered, and the global teardown stops it. Specs run serially on a single
worker because they share that instance.

Agent turns use a real model: put `OPENAI_API_KEY`, `OPENAI_BASE_URL`, and
`OPENAI_MODEL` in the repo root `.env` (gitignored) or the environment.

## Layout

- `lib/testbed.ts` starts and stops the testbed and reads its credentials.
- `lib/api.ts` is a bearer-authenticated JSON and SSE client.
- `lib/db.ts` connects to the testbed's PostgreSQL for row-level assertions.
- `lib/provider.ts` registers the `OPENAI_*` credentials as a provider.
- `lib/agent.ts` creates a model-bound agent, sends a turn, and reads the transcript.
- `lib/mcp-fixture.ts` is a local Streamable HTTP MCP server with `add` and
  `echo` tools that records every JSON-RPC method and tool call.
- `lib/fixtures.ts` extends Playwright's `test` with `admin`, `user`, `db`, and
  `loginAsAdmin`.
- `mcp/` is one case per PR of the remote MCP overhaul.

Failures keep a trace under `test-results/`; open it with
`bun run playwright show-trace <trace.zip>`.
