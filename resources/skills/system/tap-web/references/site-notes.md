# Site Notes — Remember How Sites Work

Maintain per-site knowledge so you do not rediscover access patterns every session.

## Location

```text
$XDG_CONFIG_HOME/tap/site-notes/{domain}.md
~/.config/tap/site-notes/{domain}.md
```

## What to log

- whether the site works with `tap site`, `tap fetch`, or requires `tap browser`
- whether authentication works in the active Lightpanda session or requires an
  unsupported Chrome-only capability
- working API endpoints and params
- WAF / anti-bot behavior
- reliable selectors
- cheapest successful extraction path

## Example

```markdown
# example.com

Last updated: 2026-04-07

## Access pattern

- Needs cookies from the current `AGENT_BROWSER_SESSION`
- Login works through Lightpanda with `tap browser open`, `fill`, and `click`

## Working endpoints

- `GET /api/v1/data?q=foo`
- Best inspection: `tap browser network requests --filter /api/v1/`

## Broken

- `GET /old/api` returns 403 since 2026-04-01

## Selectors

- `.result-item h3 a`
```

## Principles

- be specific
- date important changes
- record the cheapest working path
- replace stale notes instead of endlessly appending
