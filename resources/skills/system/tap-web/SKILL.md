---
description: Access websites, search the web, and extract clean content using the `tap` CLI. Supports structured site scripts, readable page extraction, and browser automation for tabs, screenshots, forms, cookies, JavaScript evaluation, and network capture. Use for web lookup, page reading, content extraction, browser interaction, authenticated sessions, request interception, or CDP-connected desktop apps.
metadata:
  author: vaayne/tap
  owner_plugin: tool/tap-web
  version: v0.4.4
name: tap-web
---

# tap-web

## Install

```bash
# Install tap CLI (includes embedded skill)
curl -fsSL https://raw.githubusercontent.com/vaayne/tap/main/scripts/install.sh | sh

# Install/update the skill to default location (~/.config/tap/skills/tap-web/)
tap skill install

# Or install to custom path (for agent use)
tap skill install --path /custom/path/to/skills/tap-web

# Or via environment variable
TAP_SKILL_DIR=/custom/path/to/skills/tap-web tap skill install
```

> **⚠️ Version Check**: The installed skill's `metadata.version` should match `tap --version`. If they differ after installing or upgrading `tap`, run `tap skill install` again. If you use a custom install location, rerun it with the same `--path` or `TAP_SKILL_DIR`.

**Before accessing any site, check `$XDG_CONFIG_HOME/tap/site-notes/{domain}.md` for saved knowledge. Update after learning.** See [references/site-notes.md](references/site-notes.md).

## Pick the right tool

Start simple and escalate only when needed:

| Tier | Tool          | Use for                                         |
| ---- | ------------- | ----------------------------------------------- |
| 1    | `tap site`    | Structured data from known sites                |
| 2    | `tap fetch`   | Clean readable content from a URL               |
| 3    | `tap browser` | Interaction, auth, screenshots, network capture |

Decision flow:

1. Check for a site script first: `tap site list` / `tap site search <query>`
2. If you just need readable content: `tap fetch <url>`
3. If you need interaction/auth/network: `tap browser ...`

## Quick reference

```bash
# Site scripts
tap site list
tap site search <query>
tap site info <script>
tap site sync
tap site run <site/action> [key=value]
tap site <site/action> [key=value]

# Content extraction
tap fetch <url>
tap fetch --json <url>
tap fetch -b <url>

# Browser context
tap status [--json]
tap attach chrome
tap attach chrome --browser-url http://localhost:9222
tap attach chrome --port-file ~/Library/Application\ Support/Google/Chrome/DevToolsActivePort
tap attach status [--json]
tap attach clear

# Browser workflow
tap browser open <url>
tap browser open <url> --new-tab
tap browser open <url> --show
tap browser tabs [--json]
tap browser switch <tab-id>
tap browser close-tab [tab-id]
tap browser status [--json]

# Page actions
tap browser text [selector]
tap browser evaluate <js>
tap browser screenshot [--output <path>]
tap browser pdf [--output <path>]
tap browser snapshot [--interactive] [-f json]
tap browser click <selector|@eN>
tap browser type <selector|@eN> <text>
tap browser fill <selector|@eN> <value> [<selector|@eN> <value> ...] [--submit <selector|@eN>]
tap browser hover <selector>
tap browser scroll [selector]
tap browser select <selector|@eN> <value>
tap browser wait <selector>
tap browser keypress <key>
tap browser dialog

# Page state / network
tap browser forms
tap browser cookies get|set|clear
tap browser network wait --url-pattern "*/api/*" --body
tap browser network log --resource-type XHR,Fetch --timeout 30s
tap browser network intercept --block --url-pattern "*.ads.*"
tap browser network clear
```

## Browser-backed flags

| Flag                    | Use when                                              |
| ----------------------- | ----------------------------------------------------- |
| `-b`, `--browser`       | Force browser execution for `site` / `fetch`          |
| `--show`                | Need a visible browser for auth or manual interaction |
| `--wait <dur>`          | Need a fixed post-navigation delay                    |
| `--wait-selector <sel>` | Wait for a specific element                           |
| `--wait-js <expr>`      | Wait for a JS condition                               |
| `--timeout <dur>`       | Limit execution time                                  |
| `--browser-url <url>`   | One-shot DevTools override                            |
| `--profile-dir <path>`  | One-shot profile override                             |
| `--lp`, `--lightpanda`  | Fast JS rendering without Chrome auth flows           |

Compatibility aliases exist, but prefer the names above.

## Preferred auth flow

Do **not** use `tap login`.

Use:

```bash
tap attach chrome
tap browser open https://example.com/login --show
```

Then reuse that same browser context with `tap site -b`, `tap fetch -b`, and later `tap browser ...` commands.

`tap attach chrome` owns an internal proxy daemon for the attached browser. If that attached state goes stale, normal commands fail explicitly and should be repaired with `tap attach chrome`, not by switching contexts implicitly.

## Context resolution

Browser-backed commands resolve context in this order:

1. explicit `--browser-url` / `--profile-dir`
2. persisted default context from `tap attach ...`
3. managed local default browser context

If an attached context becomes unreachable, tap marks it stale and fails explicitly. It does **not** silently switch to another browser/account context.

## Snapshot refs

When CSS selectors are unstable or unknown, capture a semantic snapshot and reuse its refs:

```bash
tap browser snapshot --interactive -f json
tap browser click @e3
tap browser type @e1 "hello"
tap browser fill @e1 "me@example.com" @e2 "secret" --submit @e4
tap browser select @e5 "us"
```

Rules:

- Refs like `@e1` come from the latest snapshot for the current tab.
- Refs are only valid for the same page document.
- After navigation, reload, or major page updates, re-run `tap browser snapshot` before using old refs.
- Mixed fills are safe: selector-based fills complete before a ref-based `--submit @eN` click is dispatched.

## Efficiency rules

Never dump full HTML unless there is no cheaper path.

Preferred order:

1. Site script
2. `tap fetch`
3. `tap browser network wait --body`
4. `tap browser snapshot` for semantic interactive discovery
5. `tap browser text`
6. targeted `tap browser evaluate`
7. screenshot

## References

- [browser.md](references/browser.md)
- [network.md](references/network.md)
- [script-development.md](references/script-development.md)
- [site-notes.md](references/site-notes.md)
