---
title: Agent Plugin package reference
---

The Agent Plugin reader loads portable package declarations. Runtime discovery
consumes the declaration as one package with independent resource kinds.
Reading a package alone does not install binaries, enable a Native capability,
create an OAuth connection, or launch a process.

## Portable layout

The package root contains a required `plugin.json`. Skills are discovered only
from immediate child directories of `skills/` whose `SKILL.md` is a valid Agent
Skills document. MCP servers are read only from root `mcp.json`.

Stella supports Agent Plugins specification 1.0.0 locally. A loader never
fetches a schema while reading a package. Unknown top-level fields in
`plugin.json` produce a diagnostic and are ignored. Unknown extension
namespaces are opaque and produce no diagnostic; their contents are not
interpreted.

Package paths are resolved under the filesystem-resolved package root. Internal
symlinks are allowed. A symlink that resolves outside the root is rejected at
the narrowest component boundary. The reader preserves `SKILL.md` bytes and
file mode for the later asset layer.

## `com.cherryhq.stella` extension

Stella-specific declarations live under
`plugin.json.extensions["com.cherryhq.stella"]` and require an explicit
extension `version`. The supported declaration groups are:

| Field          | Meaning                                                                                                                                                                               |
| -------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `display_name` | Optional public label shown in the Web UI.                                                                                                                                            |
| `prompt`       | Optional public guidance included for a selected package.                                                                                                                             |
| `binaries`     | Public command, installer/tool, optional version, and installer options.                                                                                                              |
| `session_env`  | Runtime variable, public source identifier, and whether the binding is required.                                                                                                      |
| `oauth`        | Public provider identifier, requested scopes, and credential-to-environment bindings. Connection bindings are not supported; configure MCP authentication on each server declaration. |

The extension `version` is currently exactly `"1"`. A Skill's standard
`compatibility` field can describe an environment or native-capability need in
human-readable form; runtime policy still decides whether it is available.

Minimal declaration accepted by strict authoring validation:

```json
{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "example.tools",
  "extensions": {
    "com.cherryhq.stella": {
      "version": "1",
      "binaries": [{ "name": "gh", "tool": "github:cli/cli" }],
      "session_env": [{ "env_var": "GH_TOKEN", "source": "oauth.access_token" }],
      "oauth": [
        {
          "provider": "github",
          "scopes": ["repo"],
          "bindings": [{ "credential": "access_token", "env_var": "GH_TOKEN" }]
        }
      ]
    }
  }
}
```

These are declarations, not implementations. Packages must not contain tokens,
client secrets, database configuration IDs, vault locators, installation state,
or native tool/channel/provider/hook implementations. The extension reader has
no process or network execution path.

## MCP transport boundary

Streamable HTTP (`streamable-http`) and legacy HTTP+SSE (`sse`) entries are
retained independently, including their own URL and visible headers. A bad
entry does not hide another valid server. `stdio` entries are recognized and
reported as unsupported, then skipped. Stella never starts a package process
as a compatibility fallback. Unsupported transports and malformed entries have
component-local diagnostics; the package's valid skills and other servers stay
loadable.

Authoring validation is stricter than client loading: unknown manifest fields,
unsupported stdio, invalid components, and malformed Stella declarations are
authoring errors. Tolerant loading keeps independent valid components and
returns diagnostics for issues it can safely explain.

## Files, copies, and session constraints

Release authoring accepts only `plugin.json`; root `plugin.yaml` and `assets.yaml`
are rejected. Generated YAML is an internal embedded representation. A package
is installed by publishing its complete file tree into one of the four typed
resource scopes. Editing a package replaces that complete tree; a copied package
has no parent link and does not follow later source edits. Standalone Skill and
MCP files remain independent resources.

A package declaration never contains tokens, client secrets, database IDs, Vault
locators, or installation state. Credential references are public locators only;
OAuth grants and connection state live in the credential store. Deleting an MCP
file does not disconnect its OAuth grant. Disconnect is an explicit local action
that revokes the matching stored grant, closes its connection, and blocks late
callbacks or refreshes; remote provider revocation is not guaranteed.

Selected packages must use distinct environment variable names. If two packages
declare the same variable, session preparation fails before credentials are
injected, even if both declarations name the same provider. Runtime captures the
complete package for a turn; edits apply on the next turn. Existing CLI artifacts
and remote connections may be reused when their captured identity still matches.
The local backend enforces its configured sandbox policy but cannot prove that
detached descendants stopped; the `none` backend provides no reliable process
isolation. Retained package bytes and installation caches therefore require
verified process termination before cleanup.
