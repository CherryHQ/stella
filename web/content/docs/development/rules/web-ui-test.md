# Web UI Test

Automate Stella web UI verification using `tap` browser commands.

## Environment

| Variable         | Purpose        | Default      |
| ---------------- | -------------- | ------------ |
| `DEV_ADMIN_USER` | Admin username | `admin`      |
| `DEV_ADMIN_PASS` | Admin password | `i-am-admin` |
| `DEV_USER`       | Normal user    | `user`       |
| `DEV_PASS`       | Normal pass    | `i-am-user`  |

Base URL: `http://localhost:25678`

```bash
URL=${URL:-http://localhost:25678}
```

## Prerequisites

1. Stella server must be running (`mise run dev` or similar).
2. `tap` CLI must be installed and on `$PATH`.

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
tap browser fill @e3 "$USERNAME" @e4 "$PASSWORD" --submit @e1
sleep 1
tap browser text | head -20
```

### Register (if login fails with "Invalid username or password")

```bash
# From login page, click Register
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
directly (no browser).

## Notes

- Snapshot refs (`@e1`, `@e2`, ...) are invalidated after navigation — always re-snapshot.
- Password minimum length is 8 characters.
- The login form uses `placeholder` attributes: `username`, `password`.
- After login, the app redirects to `/agents`.
