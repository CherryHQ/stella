---
title: Web UI testing
description: Browser automation workflow for verifying Stella's Web UI.
---

Automate Stella UI verification with Playwright specs under `test/e2e/`. Every scenario worth keeping belongs there as a checked-in spec, so the verification is repeatable. The suite uses a disposable testbed on port `25777`; `mise run test:e2e` manages it automatically.

## Environment and fixture setup

For interactive exploration:

```bash
mise run testbed:start
cd test/e2e
bun run playwright test --ui
bun run playwright codegen http://127.0.0.1:25777
mise run testbed:stop
```

The testbed prints a temporary credentials path. Treat it as a secret, do not print or commit it. The checked-in fixtures in `lib/fixtures.ts` provide `admin`, `user`, `db`, and `loginAsAdmin`; use those in specs instead of creating accounts through the browser. Use browser registration only when registration itself is under test. Real-model tests are tagged `@model`; `mise run test:e2e:fast` excludes them.

Never use `~/.stella-dev`, port `25678`, hand-created fixture accounts, or browser/CDP registration for ordinary setup. `testbed:stop` owns cleanup. If using the explicit start/stop flow, stop the testbed on every exit path.

## Common workflows

Playwright runs the functional project by default. The model project contains only `@model` tests and allows one retry; non-model tests have zero retries. Run one file or one title from `test/e2e` with Bun's Playwright command, or open the UI runner when iterating:

```bash
cd test/e2e
bun run playwright test mcp/1-catalog.spec.ts
bun run playwright test -g 'catalog endpoint'
bun run playwright test --ui
bun run playwright codegen http://127.0.0.1:25777
```

`codegen` is for discovering selectors and interactions, not for manufacturing fixtures. Copy the stable interaction into a checked-in spec and assert the result. Re-snapshot after navigation because locator state and generated references are not durable.

## Assertion pattern

After each meaningful action, verify the result before moving on:

1. Assert visible text or role with Playwright `expect`, including the expected heading, row, alert, or empty state.
2. Assert the URL or route when navigation is part of the behavior.
3. Assert error banners and failed network responses when the action can fail.
4. Pair the browser assertion with `db` or `admin` assertions when the point is what the UI wrote.

Report expected versus actual on a failure; do not silently continue. Prefer semantic roles and labels over brittle CSS or screenshots. Use screenshots and traces when layout or a failure needs visual diagnosis, not as the only assertion.

## Seeding UI states

The testbed fixtures create accounts and expose authenticated API clients. In a spec, use `admin` or `user` from `lib/fixtures.ts`, `loginAsAdmin` for the browser session, and `db` for direct Postgres assertions:

```ts
const goal = await admin.post("/api/goals", {
  agent_id: agentID,
  title: "Visual goal",
  intent: "…",
});
await loginAsAdmin();
await page.goto("/agents");
await expect(page.getByText("Visual goal")).toBeVisible();
const row = (await db`select lifecycle from agent_goal where id = ${goal.body.id}`)[0];
```

Use the API for states it can create, such as draft goals, MCP servers, agents, provider configuration, and scheduler jobs. Fabricate lifecycle-dependent states directly in `db` only for visual verification; behavior tests must reach them through real HTTP/UI paths. For example, Work and Inbox states are keyed by `agent_goal.lifecycle`: `blocked` needs attention, `active` is active, and accepted history is `done` with `done_reason='accepted'`, `acceptance_state='passed'`, and non-null `accepted_output`. A repeatable workflow requires an `agent_workflow` row with `owner_kind='agent'`, `payload_format='frozen/v0'`, an empty children/edges payload, and `source_goal_id` pointing to the accepted goal. Cancellation has a real API route and should be tested through it.

Do not print the credentials artifact or `.env`. Keep fixture data scoped to the test's run and delete created resources in fixture teardown.

## Notes

- `testbed:start` owns embedded PostgreSQL and the server; it is not a substitute for the system harness's process-lifecycle coverage.
- Hidden Chrome tabs throttle rendering and rAF; perf specs use the visible browser and belong in `test/e2e/perf`.
- `textContent` is safer than `innerText` for virtualized or `content-visibility` history. Count matches when concatenated text has no separators.
- Snapshot and locator assumptions become stale after navigation or a rerender; locate again instead of reusing generated refs.
- The login form uses `username` and `password` placeholders, requires passwords of at least 8 characters, and normally redirects to `/agents`.
- Use the API clients for role-based access checks with `ADMIN_PAT`/`USER_PAT` semantics, not browser-created accounts.

For DB invariants, pair this browser workflow with `api-test.md`. For performance, use `web-perf-test.md`; functional assertions prove behavior, never speed.
