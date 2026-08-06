---
title: Web UI testing
description: Browser automation workflow for verifying Stella's Web UI.
---

Automate Stella web UI verification using `tap` browser commands.

## Environment

Base URL: `http://localhost:25678`

```bash
URL=${URL:-http://localhost:25678}
```

## Fixture setup

Start the disposable test instance, which starts its own embedded PostgreSQL,
server, and fixture accounts through the live HTTP API. It is long-running, so
run it in a dedicated terminal or background task.

```bash
# Terminal 1: leave this running while testing.
mise run agent-test:start
```

Start prints the server URL and a temporary credentials path. The credentials
artifact is mode `0600`; it contains both identities and roles, the admin
email/password/PAT, and the passwordless user's PAT. Treat it as a secret: do
not commit it, print it, or paste it into shared logs.

When finished, stop it from the checkout that started it. Stop owns graceful
shutdown and removes the temporary server, database, and credentials.

```bash
mise run agent-test:stop
```

Do not use `~/.stella-dev`, manually create fixtures, or use browser/CDP
automation to register fixture accounts. Use browser registration only when the
registration UI itself is the subject under test.

Use the credentials without displaying them:

```bash
CREDS="<credentials path printed by agent-test:start>"
ADMIN_EMAIL="$(jq -r '.admin.email' "$CREDS")"
ADMIN_PASSWORD="$(jq -r '.admin.password' "$CREDS")"
ADMIN_PAT="$(jq -r '.admin.token' "$CREDS")"
USER_PAT="$(jq -r '.user.token' "$CREDS")"
```

`tap` CLI must be installed and on `$PATH`.

## Tool selection (cheapest first)

| Need                          | Tool                                                      |
| ----------------------------- | --------------------------------------------------------- |
| Check page text/state         | `tap browser text [selector]`                             |
| Discover interactive elements | `tap browser snapshot --interactive -f json`              |
| Fill forms / click buttons    | `tap browser fill` / `tap browser click`                  |
| Run JS assertions             | `tap browser evaluate <js>`                               |
| Check network responses       | `tap browser network wait --url-pattern "*/api/*" --body` |
| Visual verification only      | `tap browser screenshot`                                  |

Prefer `text` and `snapshot` over `screenshot`. Only screenshot when layout/visual matters.

## Browser lifecycle

```bash
# Check if browser is already running
tap status

# If stale or no session, start fresh (visible window)
tap browser open "$URL" --show

# Subsequent navigations reuse the session
tap browser open "$URL"
```

If `tap browser` commands fail with connection errors, the session is stale. Fix:

```bash
rm "$HOME/.cache/tap/browser/state.json"
tap browser open "$URL" --show
```

## Common workflows

### Login

```bash
tap browser open "$URL/login"
tap browser snapshot --interactive -f json
# Clear any pre-filled values
tap browser evaluate 'document.querySelectorAll("input").forEach(i => { i.value = ""; i.dispatchEvent(new Event("input", {bubbles:true})); })'
tap browser fill @e3 "$ADMIN_EMAIL" @e4 "$ADMIN_PASSWORD" --submit @e1
sleep 1
tap browser text | head -20
```

### Register (only when registration UI is under test)

```bash
# Do not use this to create fixtures. From login page, click Register only when
# the registration flow itself is the subject under test.
tap browser click @e2   # "Need an account? Register"
tap browser snapshot --interactive -f json
tap browser fill @e3 "$USERNAME" @e4 "$PASSWORD" @e5 "$PASSWORD" --submit @e1
sleep 1
tap browser text | head -20
```

### Verify page loaded

```bash
tap browser text | head -30          # Quick text check
tap browser snapshot --interactive   # See all interactive elements
```

### Navigate

```bash
tap browser open "$URL/agents"
tap browser open "$URL/settings"
```

## Assertion pattern

After each action, verify the result before moving on:

1. **Text check**: `tap browser text | head -N` — confirm expected heading or content.
2. **URL check**: `tap status --json` — confirm navigation landed on the right page.
3. **Error check**: look for error banners in `tap browser text` output.

If an assertion fails, report what was expected vs what was found. Do not silently continue.

## Related

This covers the browser layer. To assert what a UI action actually wrote, pair it
with the DB checks in `api-test.md` — browser drive here + DB assertions there is a
full `browser -> API -> DB` e2e. For backend behavior alone, use `api-test.md`
directly (no browser). For performance measurement (frame times, keystroke cost,
load/transfer cost) use the harness described in `web-perf-test.md` — functional
checks here prove behavior, never speed.

Use the browser only to exercise UI behavior. Drive API and role-based access
control checks with `ADMIN_PAT` or `USER_PAT`, never by creating fixture accounts
through browser automation.

## Notes

- Snapshot refs (`@e1`, `@e2`, ...) are invalidated after navigation — always re-snapshot.
- Password minimum length is 8 characters.
- The login form uses `placeholder` attributes: `username`, `password`.
- After login, the app redirects to `/agents`.
