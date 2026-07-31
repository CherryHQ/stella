---
title: Web UI testing
description: Release Playwright E2E, local Tap exploration, and performance-test boundaries for Stella's Web UI.
---

Stella has two browser workflows with different evidence:

- the release Browser E2E suite uses Playwright with headless Chromium and
  produces release Scenario Results;
- local exploratory UI work uses an interactive browser through `tap` and does
  not produce release evidence.

Performance measurements are a third, separate workflow described in
`web-perf-test.md`; they measure speed rather than functional correctness.

## Run the suite

From the repository root:

```bash
mise run browser-test
```

The task downloads the fixed Stella PostgreSQL runtime, installs the locked web
dependencies and Chromium, builds `dist/bin/stellad` when no candidate is
provided, and runs all 17 deterministic journeys. Their release policies remain
defined per Scenario in `test/capabilities.yaml`.

A release job must inject the exact extracted candidate instead of rebuilding:

```bash
STELLA_SYSTEM_BINARY=/absolute/path/to/stellad mise run browser-test
```

`STELLA_SYSTEM_BINARY` must be an absolute, executable, non-symlink file. The
runner starts it with a fresh temporary `STELLA_HOME`, its real embedded
PostgreSQL lifecycle, local registration enabled, and an explicit environment
allowlist.

## Release journeys

| Scenario | Browser behavior proved                                                                                   |
| -------- | --------------------------------------------------------------------------------------------------------- |
| C02-S02  | Registration validation, registration, failed and successful login, and logout                            |
| C03-S02  | User promotion, demotion, activation, restoration, and fail-closed normal-user authorization              |
| C05-S02  | Restricted agent creation/editing, user assignment, and role-limited actions                              |
| C06-S02  | Provider creation, model-fetch errors/success, enable/save, UI password masking, and deletion             |
| C07-S03  | New thread, real partial SSE rendering, completion, reload, and persisted history                         |
| C10-S03  | Goal creation, inspection, cancellation, archive, and restore                                             |
| C11-S02  | Accepted-goal workflow creation, inspection, instantiation, and recorded run                              |
| C12-S03  | Schedule creation, immediate run, history, editing, disabling, and deletion                               |
| C13-S03  | Soul, user-memory, and agent-memory editing with changelog inspection                                     |
| C14-S02  | Skill archive upload, content editing, persistence, and removal                                           |
| C16-S02  | Actionable human-review inbox item navigation and goal context                                            |
| C17-S02  | Workspace share creation, cookie-free public rendering, revocation, and expired-link UI                   |
| C18-S02  | Vault secret creation, masked editing, persistence, deletion, and diagnostic redaction                    |
| X10-S02  | Built-in plugin discovery, inspection, enable/disable, and reload persistence                             |
| X11-S02  | Loopback RSS polling plus Recally article, feed, tag, and digest surfaces                                 |
| X13-S03  | OAuth provider credentials, scope removal/addition, reload persistence, and restore-default behavior      |
| X02-S02  | Webhook creation/configuration, disable/enable behavior, authenticated ingress, persistence, and deletion |

The provider journey proves only that the Web UI renders the API key in a
password field and does not expose it as visible page text. API response
redaction remains a separate Integration scenario.

The webhook journey proves the UI controls and the published ingress. The
current channel page has no separate runtime-status surface, so the test does
not claim one.

## Deterministic fixtures

The Go runner creates all fixtures through Stella's public registration and API
surfaces. It does not seed the database or call private test backdoors.

Model discovery and chat use a loopback Anthropic-compatible fake. The chat
journey deliberately gates the stream after the first text delta, waits for the
browser to render that partial reply, then releases the remainder. It also
injects one provider failure and proves that the inline error clears on the next
successful turn. No real provider account, external network, or release secret
is needed.

The OAuth scope journey edits only a temporary Lark provider override in the
fresh release database and removes it before finishing. It does not call an
external identity provider; the separate Manual Scenario owns real login.

Every invocation generates unique users and object IDs. The temporary Stella
home and embedded database are removed after the candidate and its process
group stop.

## Assertions and diagnostics

Tests use role, label, placeholder, and repository-owned data-attribute
locators. Use Playwright's web-first assertions and response waits; do not add
fixed sleeps for UI synchronization.

Each Scenario emits an independent release Result. Diagnostics live under:

```text
dist/test-results/release/<run-id>/
├── results/
└── artifacts/browser/
```

The artifact set includes the raw Playwright report, candidate and runner logs,
browser console entries, a network summary without headers, bodies, or query
strings, and failure-only screenshots and traces. Before Results are written,
the runner scans the complete browser artifact directory for explicitly named
release secrets. Unsafe diagnostics are removed if that scan fails.

## Optional local exploration with Tap

For exploratory UI work, run a development server and drive an interactive
browser through `tap`:

```bash
mise run dev

URL=${URL:-http://localhost:25678}
tap browser open "$URL/login" --show
```

The development server and `tap` CLI must already be available. The browser
session is reused by later commands.

Use the cheapest inspection tool that proves the current state:

| Need                          | Command                                                   |
| ----------------------------- | --------------------------------------------------------- |
| Check visible text            | `tap browser text [selector]`                             |
| Discover interactive elements | `tap browser snapshot --interactive -f json`              |
| Fill forms or click controls  | `tap browser fill` / `tap browser click`                  |
| Inspect application state     | `tap browser evaluate <js>`                               |
| Inspect an API response       | `tap browser network wait --url-pattern "*/api/*" --body` |
| Check visual layout           | `tap browser screenshot`                                  |

Prefer text and interactive snapshots over screenshots unless layout itself is
under investigation.

### Login or registration

Take a fresh snapshot before using element references:

```bash
tap browser open "$URL/login"
tap browser snapshot --interactive -f json
tap browser fill @e3 "$USERNAME" @e4 "$PASSWORD" --submit @e1
tap browser text | head -20
```

If the local account does not exist, open the registration form, take another
snapshot, and fill the fields reported by that snapshot:

```bash
tap browser click @e2
tap browser snapshot --interactive -f json
tap browser fill @e3 "$USERNAME" @e4 "$PASSWORD" @e5 "$PASSWORD" --submit @e1
tap browser text | head -20
```

Snapshot references such as `@e1` are invalidated after navigation. Always take
a new snapshot before the next referenced action.

### Exploratory assertion pattern

After each action:

1. inspect visible text for the expected result or an error banner;
2. inspect `tap status --json` when navigation matters;
3. inspect the relevant API response when visible UI state is insufficient.

This workflow is useful for investigation, but it must not be cited as an
automated release Scenario result.

## Performance measurement

Frame pacing, keystroke cost, long-history loading, and attachment-transfer
measurements use the deterministic harness in `test/perf/`; follow
`web-perf-test.md` for the required same-machine before/after workflow.

The performance harness uses a scratch Stella instance, a fake Anthropic
provider, and a visible browser. Its JSON measurements are performance evidence,
not functional Browser Scenario Results. Functional behavior discovered while
measuring must be covered by Playwright or the appropriate API/System test.

## Related

- `api-test.md` covers backend behavior without a browser.
- `system-test.md` defines the broader test-layer boundaries.
- `web-perf-test.md` defines reproducible Web performance measurement.

Password fields require values that satisfy the current local-auth validation,
and successful login normally redirects to `/agents`.
