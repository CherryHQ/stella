# Browser Reference

Tap's public browser model is:
- one default browser context
- one current tab
- explicit attach flow for existing Chrome targets

## Core commands

### Context and status

```bash
tap status [--json]
tap attach chrome
tap attach chrome --browser-url http://localhost:9222
tap attach chrome --port-file ~/Library/Application\ Support/Google/Chrome/DevToolsActivePort
tap attach status [--json]
tap attach clear
```

### Tabs

```bash
tap browser open <url>
tap browser open <url> --new-tab
tap browser open <url> --show
tap browser tabs [--json]
tap browser switch <tab-id>
tap browser close-tab [tab-id]
tap browser status [--json]
```

### Actions

```bash
tap browser navigate <url>
tap browser back
tap browser forward
tap browser reload
tap browser text [selector]
tap browser evaluate <javascript>
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
tap browser forms
tap browser cookies get|set|clear
```

## Common workflows

### Reuse existing Chrome

```bash
tap attach chrome
tap browser open https://example.com
tap browser click '#submit'
```

### Snapshot-driven interaction

```bash
tap browser snapshot --interactive -f json
tap browser click @e3
tap browser type @e1 "search terms"
tap browser fill @e1 "me@example.com" @e2 "secret" --submit @e4
tap browser select @e5 "us"
```

Use this when selectors are brittle, generated, or unknown. Refs come from the latest snapshot for the current tab and document.

### Visible browser for auth

```bash
tap attach chrome
tap browser open https://github.com/login --show
tap browser open https://github.com
```

### Multi-tab flow

```bash
tap browser open https://news.ycombinator.com
tap browser open https://github.com --new-tab
tap browser tabs
tap browser switch tab-2
tap browser screenshot --output github.png
```

### Attached Chrome lifecycle

```bash
tap attach chrome
tap attach status --json
tap browser open https://example.com --show
```

`tap attach chrome` persists an internal proxy-backed attached context. If that context becomes stale, normal browser-backed commands fail explicitly and should be repaired with `tap attach chrome`.

## Resolution rules

When browser-specific overrides are omitted, tap resolves:
1. explicit one-shot override (`--browser-url`, `--profile-dir`)
2. persisted default context from `tap attach ...`
3. managed local default context

Tab resolution is:
1. explicit hidden `--tab` override
2. current tab
3. only live tab
4. error with guidance

## Notes

- `tap browser open <url>` navigates the current tab by default.
- `--new-tab` creates a fresh tracked tab with the next stable ID (`tab-1`, `tab-2`, ...).
- `tap browser tabs` is the common tab-management entrypoint.
- `tap browser status --json` and `tap browser tabs --json` are the machine-readable contracts.
- `tap browser snapshot` captures a semantic tree and assigns stable refs like `@e1` for interactive nodes.
- Snapshot refs are validated against the current page document. After navigation, reload, or substantial DOM changes, re-run `tap browser snapshot` before reusing refs.
- In mixed `tap browser fill` flows, selector-based fills complete before a ref-based `--submit @eN` click is fired.
- If an attached context goes stale, tap fails explicitly instead of silently switching browser state.
