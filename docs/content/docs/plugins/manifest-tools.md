---
title: Manifest Tool Plugins
description: File-driven CLI tool integrations loaded from $ANNA_HOME/plugins.yaml.
---

## Overview

Manifest tool plugins are a lightweight alternative to full Go-compiled plugins for simple CLI tool integrations. Instead of writing a Go package, you declare the tool in a YAML file or add it from the Plugins admin UI, and Anna reconciles the binary download automatically.

Anna ships with a built-in manifest that declares the default manifest-managed CLI integrations (`tap-web`, `gh`, `lark-cli`, `rtk`). They appear in their semantic tabs, such as **Tools** or **Hooks**, with a `manifest` badge. You can override or extend them in `$ANNA_HOME/plugins.yaml` or from the admin UI.

## How It Works

At startup, Anna:

1. Loads the embedded built-in manifest (`builtin_plugins.yaml`)
2. Loads your user manifest (`$ANNA_HOME/plugins.yaml`) if it exists
3. Merges them: user entries override built-in entries per plugin ID
4. Registers enabled manifest plugins into the plugin host
5. Starts binary reconciliation in the background: downloads missing binaries into `$ANNA_HOME/bin`

Startup is not blocked by binary downloads. A newly added or updated manifest binary becomes available on `PATH` inside agent sandbox sessions after the background sync completes.

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
| `oauth_provider` | No | Static OAuth provider ID used by `oauth.*` session env sources, such as `github` |
| `oauth_provider_config_field` | No | Plugin config field that dynamically selects the OAuth provider, such as `brand` |
| `oauth_provider_choices` | Conditional | Allowed provider IDs when `oauth_provider_config_field` is set |

## Binary fields

| Field | Required | Description |
|-------|----------|-------------|
| `name` | Yes | Binary filename (without extension) |
| `repo` | Yes | GitHub repository in `owner/repo` format |
| `version` | No | Version tag to install (e.g. `"1.2.3"`, `"nightly"`). Defaults to `latest`. For repos that don't publish a `latest` release, you must set this explicitly. |
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
| `oauth.access_token` | Injects the connected provider's OAuth access token |
| `oauth.client_id` | Injects the connected provider bundle's client/app ID |
| `oauth.brand` | Injects the connected provider bundle's brand, when present |

`oauth.*` sources resolve through the plugin's `oauth_provider`. GitHub uses Anna's built-in GitHub CLI device-flow app and needs no admin-side plugin configuration. Feishu/Lark sources require the Lark CLI plugin credentials to be configured in the admin panel.

## State and caching

Anna tracks installed binary versions in `$ANNA_HOME/plugin-manifest-state.json`. On subsequent startups, binaries at the correct version are skipped. Change the `version` field in `plugins.yaml` to trigger a re-download. Startup reconciliation runs in the background and is cancelled on shutdown; Anna also terminates any child processes spawned by the installer.

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

Built-in plugin overrides are full-entry replacements. If you override a built-in
plugin to change one field, include the rest of the fields you still need.

### Switching `lark-cli` between Feishu and Lark

The built-in `tool/lark-cli` manifest does not hard-code Feishu or Lark twice.
Instead, it resolves the OAuth provider from the plugin's `brand` config field and
injects the brand from the saved OAuth bundle:

```yaml
oauth_provider_config_field: brand
oauth_provider_choices: [lark, feishu]
session_env:
  - env_var: LARKSUITE_CLI_BRAND
    source: oauth.brand
```

Set `brand` in the Lark CLI plugin settings:

- `feishu` uses Feishu OAuth and injects `LARKSUITE_CLI_BRAND=feishu`.
- `lark` uses international Lark OAuth and injects `LARKSUITE_CLI_BRAND=lark`.

Do not override the manifest just to switch between Feishu and Lark. A manifest
override is only needed when changing the binary or env declarations themselves.

## Admin UI

Manifest-backed plugins are shown once, in the tab that matches their kind:

- `tool/gh`, `tool/lark-cli`, and `tool/tap-web` appear in **Tools**.
- `hook/rtk` appears in **Hooks**.

Rows with manifest backing show a `manifest` badge and an **Edit definition** action for the YAML-backed plugin definition. Binaries and session environment variables are edited as form rows. If the same plugin also exposes runtime config, such as Lark CLI credentials and `brand`, the row also shows **Configure**.

The **Tools** tab includes **Add Tool** for creating a new manifest-backed CLI from a GitHub release binary. Saving writes `$ANNA_HOME/plugins.yaml`, registers the plugin, and syncs binaries automatically without a restart. The embedded built-in manifest is never modified.

## Limitations in v1

- System prompts and skill registration are not supported in the manifest. Plugins that need these capabilities still use Go registration.
- Custom install scripts are not supported. Binaries must be available as GitHub release assets.
- Only GitHub release assets are supported as a binary source.
