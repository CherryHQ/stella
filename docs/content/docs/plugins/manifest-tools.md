---
title: Manifest Tool Plugins
description: File-driven CLI tool integrations loaded from $ANNA_HOME/plugins.yaml.
---

## Overview

Manifest tool plugins are a lightweight alternative to full Go-compiled plugins for simple CLI tool integrations. Instead of writing a Go package, you declare the tool in a YAML file and Anna reconciles the binary download at startup.

Anna ships with a built-in manifest that declares the default manifest-managed CLI tool integrations (`tap-web`, `gh`, `lark-cli`, `rtk`). You can override or extend these in `$ANNA_HOME/plugins.yaml`.

## How It Works

At startup, Anna:

1. Loads the embedded built-in manifest (`builtin_plugins.yaml`)
2. Loads your user manifest (`$ANNA_HOME/plugins.yaml`) if it exists
3. Merges them: user entries override built-in entries per plugin ID
4. Reconciles enabled plugins: downloads missing binaries into `$ANNA_HOME/bin`
5. Registers enabled manifest plugins into the plugin host

The binary is then available on `PATH` inside agent sandbox sessions.

## The manifest file format

`$ANNA_HOME/plugins.yaml`:

```yaml
plugins:
  - id: tool/my-cli
    kind: tool
    name: my-cli
    display_name: My CLI
    description: Does something useful.
    enabled: true
    binaries:
      - name: my-cli
        repo: owner/my-cli
        version: "1.2.3"   # omit for latest
    session_env:
      - env_var: MY_TOKEN
        source: static
        value: "abc123"
        required: true
```

## Plugin fields

| Field | Required | Description |
|-------|----------|-------------|
| `id` | Yes | Unique plugin ID in `kind/name` form, e.g. `tool/my-cli` |
| `kind` | Yes | Plugin kind, typically `tool` |
| `name` | Yes | Short machine-readable name |
| `display_name` | No | Human-readable label shown in the admin UI |
| `description` | No | Short description shown in the admin UI |
| `enabled` | No | Whether the plugin is active. Defaults to false. Built-in plugins default to true. |
| `binaries` | No | CLI binaries to download and place in `$ANNA_HOME/bin` |
| `session_env` | No | Environment variables to inject into sandbox sessions |

## Binary fields

| Field | Required | Description |
|-------|----------|-------------|
| `name` | Yes | Binary filename (without extension) |
| `repo` | Yes | GitHub repository in `owner/repo` format |
| `version` | No | Version tag to install (e.g. `"1.2.3"`, `"nightly"`). Null by default — mise resolves it. For repos that don't publish a `latest` release, you must set this explicitly. |
| `bin_path` | No | Subdirectory inside the archive that contains the binary (e.g. `"bin"`). |
| `exe` | No | Binary name inside the archive when it differs from `name`. |

Mise auto-detects the correct release asset based on OS and architecture keywords in the filename. `bin_path` and `exe` are only needed when the archive layout or binary name is non-standard.

## Session env fields

| Field | Required | Description |
|-------|----------|-------------|
| `env_var` | Yes | Environment variable name |
| `source` | Yes | How the value is resolved (see below) |
| `value` | Conditional | Value when `source: static` |
| `required` | No | If true, session creation fails when the value cannot be resolved |

### Env sources

| Source | Description |
|--------|-------------|
| `static` | Uses the literal `value` from the manifest |
| `github_token` | Injects the user's GitHub OAuth token |
| `lark_access_token` | Injects the Lark user access token |
| `lark_app_id` | Injects the Lark app ID |
| `lark_brand` | Injects the Lark brand identifier |

`github_token` uses Anna's built-in GitHub CLI device-flow app and needs no admin-side plugin configuration. `lark_*` sources require the Lark CLI plugin credentials to be configured in the admin panel.

## State and caching

Anna tracks installed binary versions in `$ANNA_HOME/plugin-manifest-state.json`. On subsequent startups, binaries at the correct version are skipped. Change the `version` field in `plugins.yaml` to trigger a re-download.

## Overriding built-in plugins

To disable a built-in plugin, add an entry with `enabled: false`:

```yaml
plugins:
  - id: tool/tap-web
    enabled: false
```

To pin a built-in binary to a specific version:

```yaml
plugins:
  - id: tool/tap-web
    enabled: true
    binaries:
      - name: tap
        repo: vaayne/tap
        version: "0.5.0"
```

## Admin UI

The Plugins page in the admin panel shows a **Manifest Tools** tab. From there you can:

- Toggle plugins on and off
- Trigger an immediate sync to download or update binaries
- View per-plugin sync results

Toggling in the UI writes the override to `$ANNA_HOME/plugins.yaml`. The embedded built-in manifest is never modified.

## Limitations in v1

- System prompts and skill registration are not supported in the manifest. Plugins that need these capabilities still use Go registration.
- Custom install scripts are not supported. Binaries must be available as GitHub release assets.
- Only GitHub release assets are supported as a binary source.
