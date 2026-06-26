---
name: tap-web
metadata:
  author: vaayne/tap
  version: "v0.4.10"
description: >
  Access websites, search the web, and extract clean content using the `tap` CLI.
  Supports structured site scripts, readable page extraction, and browser automation
  for tabs, screenshots, forms, cookies, JavaScript evaluation, and network capture.
  Use for web lookup, page reading, content extraction, browser interaction,
  authenticated sessions, request interception, or CDP-connected desktop apps.
---

# tap-web

## Pick the right tool

| Tier | Tool          | Use for                                         |
| ---- | ------------- | ----------------------------------------------- |
| 1    | `tap site`    | Structured data from known sites                |
| 2    | `tap fetch`   | Clean readable content from a URL               |
| 3    | `tap browser` | Interaction, auth, screenshots, network capture |

Escalate only when the cheaper tier fails: search site scripts, run a matching script, fetch readable content, then use browser for interaction/auth/network. For X/Twitter, prefer `twitter/fxembed-*` site scripts before browser/fetch.

## Recipes

Structured data:

```bash
tap site search github
tap site info github/repo
tap site run github/repo repo=vaayne/tap
```

Readable content:

```bash
tap fetch https://example.com/article
tap fetch -b https://example.com/js-rendered-article
```

Snapshot-driven interaction:

```bash
tap browser snapshot --interactive -f json
tap browser click @e3
tap browser fill @e1 "me@example.com" @e2 "secret" --submit @e4
```

Auth with your real browser; never use `tap login`:

```bash
tap attach chrome
tap browser open https://example.com/login --show
tap fetch -b https://example.com/account
```

Network capture:

```bash
tap browser open https://example.com/dashboard
tap browser network wait --url-pattern "*/api/*" --body
```

`--show` opens a visible browser for manual auth; `--lp` is the fast Lightpanda path when Chrome auth/profile state is irrelevant.

## Hard rules

- Check `$XDG_CONFIG_HOME/tap/site-notes/{domain}.md` before accessing a site; default is `~/.config/tap/site-notes/`. Update it after learning. See [references/site-notes.md](references/site-notes.md).
- Efficiency order: site script > `tap fetch` > `tap browser network wait --body` > `tap browser snapshot` > `tap browser text` > targeted `tap browser evaluate` > screenshot.
- Never dump full HTML unless no cheaper path can answer the question.
- Snapshot refs (`@eN`) are invalidated by navigation, reload, or major DOM changes; re-snapshot before reuse.
- Stale attached context fails explicitly. Repair it with `tap attach chrome`; do not silently switch account/browser.

## Full references

Full command syntax lives in `tap browser --help`, `tap docs`, and: [browser.md](references/browser.md), [network.md](references/network.md), [script-development.md](references/script-development.md), [site-notes.md](references/site-notes.md).

## Install fallback

If `tap` is missing:

```bash
curl -fsSL https://raw.githubusercontent.com/vaayne/tap/main/scripts/install.sh | sh
tap skill install
```
