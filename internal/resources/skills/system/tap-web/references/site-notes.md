# Site Notes — Remember How Sites Work

Maintain per-site knowledge so you do not rediscover access patterns every session.

## Location

```text
$XDG_CONFIG_HOME/tap/site-notes/{domain}.md
~/.config/tap/site-notes/{domain}.md
```

## What to log

- whether the site works with `tap site`, `tap fetch`, or requires `tap browser`
- whether auth needs `tap attach chrome` + `tap browser open <login-url> --show`
- working API endpoints and params
- WAF / anti-bot behavior
- reliable selectors
- cheapest successful extraction path

## Example

```markdown
# example.com
Last updated: 2026-04-07

## Access pattern
- Needs browser cookies (`tap site -b ...`)
- Visible auth flow: `tap attach chrome` then `tap browser open https://example.com/login --show`

## Working endpoints
- `GET /api/v1/data?q=foo`
- Best extraction: `tap browser network wait --url-pattern "*/api/v1/*" --body`

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
