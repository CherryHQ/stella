---
title: Agent Plugin package reference
---

This page records the Phase 1 package boundary for Stella's Agent Plugin reader.
It is a data reader only. Loading a package does not install binaries, enable a
native capability, create an OAuth connection, or launch a process.

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

| Field         | Meaning                                                                                             |
| ------------- | --------------------------------------------------------------------------------------------------- |
| `binaries`    | Public command, installer/tool, optional version, and installer options.                            |
| `session_env` | Runtime variable, public source identifier, and whether the binding is required.                    |
| `oauth`       | Public provider identifier, requested scopes, and credential-to-environment or connection bindings. |

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
