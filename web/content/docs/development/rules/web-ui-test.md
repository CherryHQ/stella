---
title: Web UI testing
description: Release Browser E2E workflow for Stella's Web UI.
---

Stella's release Browser E2E suite uses Playwright with headless Chromium. It is
a release-only gate, not part of ordinary pull-request CI.

## Run the suite

From the repository root:

```bash
mise run browser-test
```

The task downloads the fixed Stella PostgreSQL runtime, installs the locked web
dependencies and Chromium, builds `dist/bin/stellad` when no candidate is
provided, and runs all six blocking journeys.

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
| C05-S02  | Restricted agent creation/editing, user assignment, and role-limited actions                              |
| C06-S02  | Provider creation, model-fetch errors/success, enable/save, UI password masking, and deletion             |
| C07-S03  | New thread, real partial SSE rendering, completion, reload, and persisted history                         |
| C17-S02  | Workspace share creation, cookie-free public rendering, revocation, and expired-link UI                   |
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
browser to render that partial reply, and then releases the remainder. No real
provider account, external network, or release secret is needed.

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

## Optional local exploration

For exploratory UI work, `mise run dev` and an interactive browser are still
useful. That workflow is not release evidence and must not be cited as an
automated Scenario result.
