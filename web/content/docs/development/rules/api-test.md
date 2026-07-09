---
title: Backend API testing
description: Live backend HTTP API integration testing workflow for stellad against Postgres.
---

Drive the backend HTTP API directly against a running `stellad` and a real Postgres,
then assert in the database. This is API / integration testing, not end-to-end —
there is no browser in the loop. Use it when behavior only shows up with the live
server — background workers, the goal dispatcher, scheduler ticks, real model calls,
multi-request flows — that a Go unit test cannot reach. Keep deterministic logic in
Go tests (`mise run test`); this runbook is for the wired-up, stateful path those
tests cannot exercise.

The shape is always the same: **start the server against an external DB, get a
bearer token, drive the HTTP API with `curl`, assert against the DB, clean up.**
The goal lifecycle at the end is one worked example — the harness is generic.

Where this sits in the test pyramid:

- **Unit** — Go tests, isolated, no server (`mise run test`).
- **API / integration** — this doc: `curl` -> HTTP API -> DB assertions, no browser.
- **E2E** — full user path `browser -> API -> DB`. Drive the UI per `web-ui-test.md`
  and add the DB assertions from this doc when you need to confirm what landed.

## Environment

| Variable              | Purpose                              | Example                                                               |
| --------------------- | ------------------------------------ | --------------------------------------------------------------------- |
| `STELLA_HOME`         | Server data dir                      | `~/.stella-dev`                                                       |
| `STELLA_DATABASE_URL` | External Postgres (server + asserts) | `postgres://postgres:postgres@localhost:15433/stella?sslmode=disable` |
| `TOKEN`               | Bearer token for the HTTP client     | `stella_<base64url>`                                                  |

Base URL: `http://localhost:25678`. `psql` (Postgres.app): `/Applications/Postgres.app/Contents/Versions/18/bin/psql`.

## 1. Start the server against external Postgres

Run against the external cluster (not the embedded one) so your assertions read the
same database the server writes. Never build into the repo root.

```bash
mise run build   # -> dist/bin/stellad
STELLA_HOME=~/.stella-dev \
STELLA_DATABASE_URL="postgres://postgres:postgres@localhost:15433/stella?sslmode=disable" \
./dist/bin/stellad serve --port 25678
```

Confirm it is listening and watch the log:

```bash
lsof -iTCP:25678 -sTCP:LISTEN -n -P
```

## 2. Get a bearer token

The server HTTP API accepts a personal access token or OAuth access token via
`Authorization: Bearer`. For local-only test setup, you can mint a PAT row
directly — token = `"stella_" + base64url(32 random bytes)`, hash =
`hex(sha256(token))`, prefix = first 15 chars:

```go
// go run mint.go
b := make([]byte, 32); rand.Read(b)
tok := "stella_" + base64.RawURLEncoding.EncodeToString(b)
sum := sha256.Sum256([]byte(tok))
fmt.Printf("%s\t%s\n", tok, hex.EncodeToString(sum[:]))
```

Insert it for the user you want to act as:

```sql
insert into auth_user_token (user_id, name, token_hash, token_prefix)
values ('<user-uuid>', 'e2e', '<sha256-hex>', '<first-15-chars>');
```

Pick a user/agent that fits the flow. For anything that calls a model, the agent
must have one configured (otherwise execution fails with `runner: api is required`):

```sql
select id, scope, creator_id, model from agent where model <> '' limit 5;
-- user-scoped agents are owned via agent.creator_id
```

## 3. Drive the API with curl

Hit the same routes the CLI/UI use. Capture IDs from responses for the next call.

```bash
URL=http://localhost:25678
curl -s -X POST "$URL/api/<resource>" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{ ... }'
```

For async features (workers, dispatcher, scheduler), poll the read endpoint until a
terminal state or a timeout — do not assume the first GET reflects the write.

## 4. Assert against the DB

API responses cover the happy path; reach into Postgres for invariants the API does
not surface (orphan rows, archived flags, counts, FK integrity). Take a baseline
before the run and diff after:

```bash
psql "$STELLA_DATABASE_URL" -c "select ... from <table> where user_id = '<uid>' and created_at > '<run-start-utc>'"
```

Report expected-vs-actual on any miss — do not silently continue.

## 5. Clean up

Cancel/delete the entities you created and drop the test token; stop the server if
it was not your usual `mise run dev`.

```bash
curl -s -X POST -H "Authorization: Bearer $TOKEN" "$URL/api/<resource>/<id>/cancel"
psql "$STELLA_DATABASE_URL" -c "delete from auth_user_token where name = 'e2e'"
```

## Gotchas

- **`GID` is an integer variable in some shells** — `GID="019f..."` fails with
  `bad math expression`. Name goal/UUID vars something else (`GOAL`).
- **Sessions live in `ctx_conversation`** (`session_id`, `kind`, `archived`), not a
  `session` table. Many features FK to it with `ON DELETE RESTRICT`, so a
  rolled-back write can leave an orphan row rather than cascading.
- **Store/serialize UTC.** Compare `created_at` against a UTC `run-start` timestamp.
- Background work is driven by River queues (`stella_goal_tick`, etc.); give the
  dispatcher a few ticks (~2s each) before asserting.

## Worked example: goal lifecycle + agent review

Drives decomposition -> execution -> agent auto-review of a `needs_verdict`
judgment item -> acceptance, and checks the session-leak fix does not over-fire.

```bash
# Create a goal whose judgment item (authority=agent) parks it at needs_verdict.
# Put the agent-judgment contract on a leaf (or let a composite push it to its
# child) — never on a composite itself, which has no output to review.
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
# Capture id, session_id, user_id.

# Poll children until the leaf reaches acceptance_state=passed (~30s).
curl -s -H "Authorization: Bearer $TOKEN" "$URL/api/goals/<goal-id>/children"
```

Assertions:

1. Leaf reaches `acceptance_state: passed`; the log shows `tool=goal_control`
   traces `decompose` -> `submit` -> `verdict` with `pass=true`.
2. **No spurious session disposal**: every session the run minted stays
   `archived = false` — a successful commit must never trigger compensating cleanup.

   ```sql
   select left(session_id,18) sid, kind, archived
   from ctx_conversation
   where user_id = '<user-id>' and created_at > '<run-start-utc>' order by created_at;
   -- expect root / decompose / execute / review rows, all archived = f
   ```

3. Disposal _on rollback_ is covered deterministically by
   `TestReview_DisposesSessionOnRollback` / `TestDisposeOnRollback`; forcing the
   live `uniq_agent_goal_active_attempt` race is brittle and adds little.
