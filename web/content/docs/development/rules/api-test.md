---
title: Backend API testing
description: Live backend HTTP API integration testing workflow for stellad against the disposable testbed.
---

Drive the backend HTTP API against the running testbed and assert in its PostgreSQL database. This is API/integration testing, not browser E2E. Use it when behavior appears only with the live server: background workers, the goal dispatcher, scheduler ticks, real model calls, or multi-request flows that a Go test cannot reach. Keep deterministic logic in Go tests (`mise run test`).

The automated shape is **testbed -> bearer token -> HTTP API -> DB assertions -> cleanup**. For repeatable subprocess journeys use `system-test.md`; for browser coverage use `web-ui-test.md`.

## Environment

The managed testbed uses `http://127.0.0.1:25777`, embedded PostgreSQL, and a `0600` credentials file. Start it with:

```bash
mise run build
mise run testbed:start
STATE=test/e2e/test-results/testbed.json
CREDS=$(jq -r .credentialsPath "$STATE")
URL=$(jq -r .baseURL "$STATE")
TOKEN=$(jq -r .admin.token "$CREDS")
DATABASE_URL=$(jq -r .database_url "$CREDS")
```

In checked-in tests, prefer `lib/fixtures.ts` and `lib/api.ts` rather than parsing credentials. Manual checks may use:

```bash
curl -H "Authorization: Bearer $TOKEN" "$URL/api/auth/me"
psql "$DATABASE_URL" -c 'select 1'
```

Do not use `~/.stella-dev`, port `25678`, hand-created accounts, or an external PostgreSQL server for this workflow. Never print credentials or `.env` values.

## 1. Drive the API

Hit the same routes the UI and fixtures use. Capture IDs from responses for subsequent calls:

```bash
curl -s -X POST "$URL/api/<resource>" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{ ... }'
```

In TypeScript specs, use the typed helpers from `test/e2e/lib/api.ts` and the `admin`/`user` fixtures. For asynchronous features, poll the read endpoint until a terminal state or a timeout. Never assume the first GET reflects the write.

## 2. Assert against the DB

API responses cover the happy path; query Postgres for invariants the API does not surface, such as orphan rows, archived flags, counts, and FK integrity. Take a UTC run-start baseline and compare only rows created by that run:

```bash
psql "$DATABASE_URL" -c "select ... from <table> where user_id = '<uid>' and created_at > '<run-start-utc>'"
```

Report expected versus actual on any miss. Do not silently continue.

## 3. Clean up

Cancel or delete entities created by a manual run and stop the testbed from the same checkout:

```bash
curl -s -X POST -H "Authorization: Bearer $TOKEN" "$URL/api/<resource>/<id>/cancel"
mise run testbed:stop
```

Fixtures must clean up their fake servers, agents, MCP servers, and other resources even when a test fails.

## Gotchas

- **`GID` is an integer variable in some shells.** `GID="019f..."` can fail with `bad math expression`; use `GOAL` or another UUID variable name.
- **Sessions live in `ctx_conversation`.** The columns are `session_id`, `kind`, and `archived`, not a `session` table. Many features use `ON DELETE RESTRICT`, so a rolled-back write can leave an orphan row instead of cascading.
- **Store and serialize UTC.** Compare `created_at` against a UTC run-start timestamp.
- **Background work uses River queues** such as `stella_goal_tick`; give the dispatcher a few ticks, about 2 seconds each, before asserting.

## Worked example: goal lifecycle + agent review

This testbed example drives decomposition -> execution -> agent auto-review of a `needs_verdict` judgment item -> acceptance. The steps and polling semantics are deliberately explicit:

```bash
# Create a goal whose judgment item parks at needs_verdict.
# Put the agent-judgment contract on a leaf, never on a composite.
curl -s -X POST "$URL/api/goals" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{
    "agent_id": "<model-configured-agent>",
    "title": "E2E review smoke",
    "intent": "Produce a short friendly greeting in English. Done means a one-sentence greeting exists.",
    "acceptance_contract": {"policy":"deterministic_then_judgment","items":[
      {"id":"quality","kind":"judgment","authority":"agent","required":true,
       "rubric":"Does the output contain a clear, friendly one-sentence greeting in English?"}]}
  }'
# Capture id, session_id, and user_id from the response.

# Poll until the leaf reaches acceptance_state=passed, typically about 30s.
curl -s -H "Authorization: Bearer $TOKEN" "$URL/api/goals/<goal-id>/children"
```

The equivalent checked-in TypeScript uses `admin.post` and then repeatedly calls `admin.get('/api/goals/<id>/children')` with a bounded poll until the terminal state. Do not replace polling with a fixed sleep.

Assertions:

1. The leaf reaches `acceptance_state: passed`; logs show `tool=goal_control` traces `decompose` -> `submit` -> `verdict` with `pass=true`.
2. **No spurious session disposal:** every session minted by this run remains `archived = false`; a successful commit must not trigger compensating cleanup.

   ```sql
   select left(session_id,18) sid, kind, archived
   from ctx_conversation
   where user_id = '<user-id>' and created_at > '<run-start-utc>' order by created_at;
   -- expect root / decompose / execute / review rows, all archived = f
   ```

3. Disposal on rollback is covered deterministically by `TestReview_DisposesSessionOnRollback` and `TestDisposeOnRollback`; forcing the live `uniq_agent_goal_active_attempt` race is brittle and adds little.
