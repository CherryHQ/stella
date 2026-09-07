---
title: Agent Plugin manifests
description: Package CLI requirements and Stella runtime metadata in plugin.json.
---

## Overview

An Agent Plugin package is a directory with a standard `plugin.json`. Stella
reads that manifest, the package's skills, and the optional
`com.cherryhq.stella` extension. The package reader only turns files into
declarations. It does not run a process, install a binary, create an OAuth
connection, or enable a Native capability.

The source layout for a packaged plugin is:

```text
plugins/agent/<name>/
  plugin.json
  skills/<skill-name>/SKILL.md
  mcp.json                         # optional, HTTP transports only
```

`<name>` is the canonical package name. It uses lowercase letters, digits,
hyphens, and periods, with no leading or trailing separator. A skill is found
only when its `SKILL.md` is in an immediate child directory of `skills/`.
Stella keeps the package's skill bytes and file mode for the later asset layer.

Native capabilities have a separate compiled registration and lifecycle. A
package manifest does not create or replace a channel, provider, hook, or
other Native capability.

## Standard manifest

The standard manifest requires `$schema` and `name`. `version`, `description`,
`author`, `homepage`, `repository`, `license`, and `keywords` are optional
standard metadata. Unknown top-level fields produce a diagnostic and are
ignored by tolerant loading. Strict authoring validation rejects them.

Stella declarations belong under the `com.cherryhq.stella` namespace. The
extension currently uses version `"1"`:

```json
{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "example-cli",
  "version": "1.0.0",
  "description": "An example command line integration.",
  "extensions": {
    "com.cherryhq.stella": {
      "version": "1",
      "display_name": "Example CLI",
      "prompt": "Use example-cli for example operations.",
      "binaries": [
        {
          "name": "example",
          "tool": "github:owner/example",
          "version": "1.2.3",
          "options": {
            "asset_pattern": "example_*_linux_x86_64.tar.gz",
            "bin_path": "bin"
          }
        }
      ],
      "session_env": [
        {
          "env_var": "EXAMPLE_ACCESS_TOKEN",
          "source": "oauth.access_token",
          "required": true
        }
      ],
      "oauth": [
        {
          "provider": "example",
          "scopes": ["read"],
          "bindings": [{ "credential": "access_token", "env_var": "EXAMPLE_ACCESS_TOKEN" }]
        }
      ]
    }
  }
}
```

`display_name` and `prompt` are public presentation and guidance fields. They
contain no credentials and do not execute code.

## Stella extension fields

| Field          | Required | Meaning                                                                       |
| -------------- | -------- | ----------------------------------------------------------------------------- |
| `version`      | Yes      | Stella extension version, currently `"1"`.                                    |
| `display_name` | No       | Public label shown by Stella.                                                 |
| `prompt`       | No       | Public guidance associated with the package. It is data, not executable code. |
| `binaries`     | No       | CLI requirements to prepare for a selected session.                           |
| `session_env`  | No       | Public environment bindings for a selected session.                           |
| `oauth`        | No       | Public provider, scope, and credential-to-environment declarations.           |

### Binaries and installer options

Each binary has a `name`, a mise `tool` key, an optional `version`, and an
optional JSON `options` object. When `version` is omitted, the installer uses
`latest`. The options object is passed to the installer and is part of the
binary identity. Put installer settings inside `options`, not beside it.

```json
{
  "name": "gh",
  "tool": "github:cli/cli",
  "version": "2.40.1",
  "options": {
    "bin_path": "bin",
    "strip_components": 1,
    "checksum": "sha256:..."
  }
}
```

Useful installer option families include:

| Installer or purpose           | Options                                                                                           |
| ------------------------------ | ------------------------------------------------------------------------------------------------- |
| Archive and single-file layout | `strip_components`, `bin_path`, `bin`, `rename_exe`, `checksum`                                   |
| GitHub releases                | `asset_pattern`, `version_prefix`, `no_app`, `filter_bins`, `prerelease`, `api_url`               |
| Direct HTTP                    | `url`, `size`, `format`, `version_list_url`, `version_regex`, `version_json_path`, `version_expr` |
| pipx and uvx                   | `extras`, `pipx_args`, `uvx`, `uvx_args`                                                          |

The exact option set is owned by the selected installer backend. An option
mismatch produces a different binary identity and does not reuse an unrelated
installation. Never put tokens, passwords, client secrets, or vault locators
in `plugin.json` or in `options`.

The supported binary tool keys include GitHub releases (`github:`), direct HTTP
downloads (`http:`), pipx (`pipx:`), npm (`npm:`), and the tool keys provided by
the managed installer. Platform-specific `platforms` maps are not supported by
the manifest integration.

### Session environment

Each `session_env` entry declares `env_var`, `source`, and optional `required`.
The standard entry has no `value` field. Sources name a resolver, such as
`oauth.access_token` or `oauth.client_id`; credentials are resolved at runtime.

OAuth declarations list a public `provider`, optional `scopes`, and `bindings`.
Each binding names a `credential` and its target `env_var`:

```json
{
  "oauth": [
    {
      "provider": "github",
      "bindings": [{ "credential": "access_token", "env_var": "GH_TOKEN" }]
    }
  ]
}
```

OAuth connection bindings are reserved for a later implementation. Configure
MCP authentication on the individual MCP child instead. The package reader
does not create connections or fetch credentials.

## Release-owned builtins and runtime placement

Built-in `plugin.json` definitions are immutable release resources. The
administrator can change a selected scope's enablement and can override CLI
`binary_versions`; the shipped binary tool, installer options, public metadata,
and prompt remain release-owned. Built-in skills are also release-owned and
their membership cannot be replaced by a scope configuration.

Only `mise` and `xberg` are embedded release runtimes. Stella synchronizes and
extracts those embedded binaries with the release. Other CLI artifacts are
prepared by the server in the background and exposed to sessions through the
four plugin scopes: `system`, `system_agent`, `user`, and `user_agent`. An
artifact being prepared does not grant a session access to it; scope resolution
still decides what the session sees.

The `web` package is independent. It carries the Bun runtime and Lightpanda
binary needed by the Web skill. The standalone `bun` plugin switch does not
enable or disable `web`; configure the `web` package and its scope instead.

## Configuration and installation

The package definition and scope configuration are separate. A scope can
enable or disable a plugin and can override CLI binary versions. The selected
scope is resolved for the trusted user and Agent before a session starts.

When a selected binary is missing from the prepared cache, Stella installs it
for the target runtime through the managed installer. A matching prepared
artifact can be reused. Stella never copies a host installation into a Docker
Linux sandbox. Native managed sessions and sandbox sessions use their own
runtime trees.

Packages do not run arbitrary install hooks or custom shell scripts. The
installer handles declared binaries only. Package reading itself has no
process or network execution path.

## Limits

- The Stella extension version must be exactly `"1"`.
- `stdio` MCP entries are unsupported and skipped with a diagnostic. Use the
  supported HTTP transports instead.
- OAuth connection bindings are not implemented yet.
- Arbitrary install hooks and custom install scripts are unsupported.
- Manifests and installer options must not contain secrets or credential
  locators.
- A malformed component is isolated where possible, so valid skills and other
  resources can remain loadable. Strict authoring validation reports unknown or
  unsupported declarations as errors.
