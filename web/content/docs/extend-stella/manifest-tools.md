---
title: Manifest Tool Plugins
description: File-driven CLI tool integrations loaded from $STELLA_HOME/plugins.yaml.
---

## Overview

Manifest tool plugins are a lightweight alternative to full Go-compiled plugins for simple CLI tool integrations. Instead of writing a Go package, you declare the tool in a YAML file or add it from the Plugins admin UI, and Stella reconciles the binary download automatically.

Stella ships with a built-in manifest that declares the default manifest-managed CLI integrations (`tap-web`, `gh`, `lark-cli`, `rtk`). They appear in their semantic tabs, such as **Tools** or **Hooks**, with a `manifest` badge. You can override or extend them in `$STELLA_HOME/plugins.yaml` or from the admin UI.

## How It Works

At startup, Stella:

1. Loads the embedded built-in manifests (`resources/oauth.yaml` and `resources/tools.yaml`)
2. Loads your user manifest (`$STELLA_HOME/plugins.yaml`) if it exists
3. Merges them: user entries override built-in entries per plugin ID
4. Registers enabled manifest plugins into the plugin host
5. Starts binary reconciliation in the background: downloads missing binaries into `$STELLA_HOME/bin`

Startup is not blocked by binary downloads. A newly added or updated manifest binary becomes available on `PATH` inside agent sandbox sessions after the background sync completes. For local sandbox sessions the binary is available from `$STELLA_HOME/bin`. Docker sandbox sessions need separate handling because host binaries may target the host OS/architecture rather than Linux.

## Docker sandbox CLI availability

Do not treat host `$STELLA_HOME/bin` as the source of Docker sandbox executables. On macOS and Windows, manifest sync can install host-platform binaries, which cannot run in a Linux container. Binding that directory into Docker also blurs the boundary between host-side tool management and the container runtime.

For Docker:

- Built-in CLI plugins that must work out of the box are pre-installed in the versioned sandbox image. The sandbox image tag is tied to the Stella release, so one release image can contain the built-in tool set for that Stella version. The image build runs `stellad mise reconcile-builtins` — the same reconcile path the daemon uses — so it installs the exact identifiers and versions declared in `resources/tools.yaml`. There is no separate Docker tool list to keep in sync.
- `$STELLA_HOME/plugins.yaml` remains the source of plugin metadata, enablement, session environment, OAuth injection, and local-sandbox binary installation.
- User-configured CLI binaries need a container-native provisioning path. They should be installed for Linux inside the Docker environment, not copied from the host's `$STELLA_HOME/bin`.

A safe Docker loading design for user-configured CLIs is:

1. Build a container tool manifest from enabled user manifest plugins' `binaries` entries, excluding built-in tools already present in the release image.
2. Use a short-lived helper container based on the same sandbox image to run `mise install` in a Linux context.
3. Store the resulting tools in a Docker-managed tool cache or volume keyed by the sandbox image tag plus user manifest hash.
4. Mount that cache into sandbox sessions at a container-only path and prepend it to the in-container `PATH`.
5. Rebuild or refresh the cache when the enabled user plugin set or binary versions change.

This keeps the release sandbox image stable while still allowing user-added CLIs. The installed user binaries are Linux container binaries, and the host `$STELLA_HOME/bin` is not part of Docker executable resolution.

## The manifest file format

`$STELLA_HOME/plugins.yaml`:

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
        tool: github:owner/my-cli
        version: "1.2.3" # omit for latest
    session_env:
      - env_var: MY_TOKEN
        source: static
        value: "abc123"
        required: true
```

## Plugin fields

| Field            | Required | Description                                                                        |
| ---------------- | -------- | ---------------------------------------------------------------------------------- |
| `id`             | Yes      | Unique plugin ID in `kind/name` form, e.g. `tool/my-cli`                           |
| `kind`           | Yes      | Plugin kind, typically `tool`                                                      |
| `name`           | Yes      | Short machine-readable name                                                        |
| `display_name`   | No       | Human-readable label shown in the admin UI                                         |
| `description`    | No       | Short description shown in the admin UI                                            |
| `enabled`        | No       | Whether the plugin is active. Defaults to false. Built-in plugins default to true. |
| `binaries`       | No       | CLI binaries to download and place in `$STELLA_HOME/bin`                           |
| `session_env`    | No       | Environment variables to inject into sandbox sessions                              |
| `oauth_provider` | No       | Static OAuth provider ID used by `oauth.*` session env sources, such as `github`   |

## Binary fields

Each binary requires a `name` and a `tool` field. The `tool` field uses mise's tool key format: `backend:identifier`.

### Common fields

| Field              | Required | Description                                                                                   |
| ------------------ | -------- | --------------------------------------------------------------------------------------------- |
| `name`             | Yes      | Binary filename placed in `$STELLA_HOME/bin` (without extension)                              |
| `tool`             | Yes      | Mise tool key in `backend:identifier` format (e.g. `github:cli/cli`)                          |
| `version`          | No       | Version to install. Defaults to `latest` for all backends.                                    |
| `strip_components` | No       | Leading directory levels to strip when extracting an archive. Auto-detected for most layouts. |
| `bin_path`         | No       | Subdirectory inside the archive containing the binary (e.g. `"bin"`).                         |
| `bin`              | No       | Rename the downloaded file when the asset is a single binary (non-archive).                   |
| `rename_exe`       | No       | Rename the executable after extraction from an archive.                                       |
| `checksum`         | No       | Verify the asset with a checksum in `algo:hex` format (e.g. `"sha256:abc123..."`).            |

### GitHub backend (`github:owner/repo`)

```yaml
binaries:
  - name: gh
    tool: github:cli/cli
    version: "2.40.1"
    bin_path: bin
```

| Field            | Description                                                                                |
| ---------------- | ------------------------------------------------------------------------------------------ |
| `asset_pattern`  | Glob pattern to select the release asset (e.g. `"gh_*_linux_x64.tar.gz"`).                 |
| `version_prefix` | Custom tag prefix (e.g. `"release-"`).                                                     |
| `no_app`         | Skip macOS `.app` bundles; prefer standalone binaries.                                     |
| `filter_bins`    | Comma-separated list of binaries to expose when the archive contains multiple executables. |
| `prerelease`     | Include pre-release versions when resolving `latest`.                                      |
| `api_url`        | GitHub API base URL for GitHub Enterprise (e.g. `"https://github.example.com/api/v3"`).    |

### HTTP backend (`http:name`)

The identifier after `http:` is the tool name used internally by mise.

```yaml
binaries:
  - name: sentinel
    tool: http:sentinel
    url: "https://releases.hashicorp.com/sentinel/{{version}}/sentinel_{{version}}_{{os()}}_{{arch()}}.zip"
    version: "0.26.3"
```

| Field               | Description                                                                                          |
| ------------------- | ---------------------------------------------------------------------------------------------------- |
| `url`               | Download URL. Required for http backend. Supports `{{version}}`, `{{os()}}`, `{{arch()}}` templates. |
| `size`              | Expected file size in bytes for verification.                                                        |
| `format`            | Archive format override (e.g. `"tar.xz"`).                                                           |
| `version_list_url`  | URL to fetch available versions from.                                                                |
| `version_regex`     | Regex to extract versions from the version list.                                                     |
| `version_json_path` | jq-style path to extract versions from JSON (e.g. `".[].tag_name"`).                                 |
| `version_expr`      | expr-lang expression to extract versions.                                                            |

### Pipx backend (`pipx:package`)

The identifier is the PyPI package name, `org/repo` for a GitHub source, or a `git+https://...` URL.

```yaml
binaries:
  - name: mypy
    tool: pipx:mypy
    version: "1.8.0"
```

| Field       | Description                                   |
| ----------- | --------------------------------------------- |
| `extras`    | Pip extras to install alongside the package.  |
| `pipx_args` | Extra arguments to pass to pipx.              |
| `uvx`       | Use `uvx` (uv's tool runner) instead of pipx. |
| `uvx_args`  | Extra arguments for uvx.                      |

### NPM backend (`npm:package`)

```yaml
binaries:
  - name: serve
    tool: npm:serve
    version: "14.2.0"
```

Platform-specific asset patterns (`platforms:` map) are not supported in the manifest.

## Session env fields

| Field      | Required    | Description                                                       |
| ---------- | ----------- | ----------------------------------------------------------------- |
| `env_var`  | Yes         | Environment variable name                                         |
| `source`   | Yes         | How the value is resolved (see below)                             |
| `value`    | Conditional | Value when `source: static`                                       |
| `required` | No          | If true, session creation fails when the value cannot be resolved |

### Env sources

| Source               | Description                                           |
| -------------------- | ----------------------------------------------------- |
| `static`             | Uses the literal `value` from the manifest            |
| `oauth.access_token` | Injects the connected provider's OAuth access token   |
| `oauth.client_id`    | Injects the connected provider bundle's client/app ID |

`oauth.*` sources resolve through the plugin's `oauth_provider`. GitHub uses Stella's built-in GitHub CLI device-flow app and needs no admin-side plugin configuration. Other providers must be declared and configured separately.

## State and caching

Stella tracks installed binary versions in `$STELLA_HOME/plugin-manifest-state.json`. On subsequent startups, binaries at the correct version are skipped. Change the `version` field in `plugins.yaml` to trigger a re-download. Startup reconciliation runs in the background and is cancelled on shutdown; Stella also terminates any child processes spawned by the installer.

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
        tool: github:vaayne/tap
        version: "0.5.0"
```

Built-in plugin overrides are full-entry replacements. If you override a built-in
plugin to change one field, include the rest of the fields you still need.

## Admin UI

Manifest-backed plugins are shown once, in the tab that matches their kind:

- `tool/gh`, `tool/lark-cli`, and `tool/tap-web` appear in **Tools**.
- `hook/rtk` appears in **Hooks**.

Rows with manifest backing show a `manifest` badge and an **Edit definition** action for the YAML-backed plugin definition. Binaries and session environment variables are edited as form rows. If the same plugin also exposes runtime config, the row also shows **Configure**.

The **Tools** tab includes **Add Tool** for creating a new manifest-backed CLI from a GitHub release binary. Saving writes `$STELLA_HOME/plugins.yaml`, registers the plugin, and syncs binaries automatically without a restart. The embedded built-in manifest is never modified.

Editing a built-in from the admin UI stores only the fields you changed, so the rest keep following the definition shipped with the server and still improve when you upgrade. Such a plugin is marked **customized** and offers **Reset to default**, which drops the stored edits and leaves the enable switch as it is. Lists — binaries, skills, session environment variables — are stored whole: edit one binary and you own that list. A customization saved before this behaviour existed holds a whole definition and stays frozen at it; saving that plugin once rewrites the row and it starts following upgrades again.

## Limitations in v1

- System prompts and skill registration are not supported in the manifest. Plugins that need these capabilities still use Go registration.
- Custom install scripts are not supported.
- Platform-specific asset patterns (`platforms:` map) are not supported. Use `asset_pattern` instead.
- Supported binary sources: GitHub releases (`github`), direct HTTP download (`http`), pipx (`pipx`), npm (`npm`).
