---
created-at: "2026-04-17T11:04:44Z"
description: Set up and use tap CLI with Lightpanda in this Docker container environment.
name: tap-lightpanda-setup
status: draft
---
# Tap + Lightpanda Setup

## Environment

This environment has `tap` pre-installed at `/workspace/bin/tap`. No `curl`, `wget`, or `python3` available. No `sudo`.

## Install Lightpanda

```bash
/workspace/bin/tap doctor --install
```

Installs Lightpanda to `/home/nonroot/.cache/tap/lightpanda/lightpanda`.

## Usage

```bash
# Clean content extraction (works)
/workspace/bin/tap fetch <url> --lp
/workspace/bin/tap site <script> --lp

# Browser automation (does NOT work — needs Chrome/Chromium)
/workspace/bin/tap browser open <url> --lp  # fails: chrome binary not found
```

## Key constraints

- `--lp` / `--lightpanda` flag enables Lightpanda rendering for `fetch` and `site` commands
- `tap browser` commands (open, click, screenshot, etc.) require a real Chrome/Chromium binary — Lightpanda does not replace this
- Use full path `/workspace/bin/tap` since it's not in `$PATH`
