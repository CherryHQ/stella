---
title: Plugin Org Isolation
description: How Stella scopes plugin runtimes, manifest overrides, and channel callbacks to a single org.
---

## Overview

Stella is multi-tenant. Every plugin runtime, manifest override, OAuth provider configuration, and channel callback must resolve to exactly one org. This page documents the contracts that enforce that.

## The org context contract

Org identity travels with `context.Context`. The helpers live in `internal/orgctx`:

```go
ctx = orgctx.WithOrgID(ctx, orgID)
got := orgctx.OrgIDFromContext(ctx) // returns "" when absent
```

Two layers enforce the contract:

- **HTTP middleware** injects the authenticated user's `orgID` into every `/api/*` handler's `r.Context()`.
- **Channel runtime** (one per `(org, channel.ID, runtimeName)`) wraps the shared coordinator handler with `pkgchannel.HandlerWithOrgID(orgID, inner)` at build time. Every SDK callback (`onMessage`, `onReaction`, telegram poller, etc.) is dispatched through that wrapper, so the coordinator sees the right org even when the SDK provides a bare context.

Downstream code uses `requireOrgID(ctx)` (`internal/store/dbstore.go`, `internal/pluginhost/runtime.go`, `internal/channel/coordinator.go`) to fail loud if context is missing the org. Read paths fall back to `(nil, false)`; write paths return an error.

## RuntimeHost is keyed by org

`internal/pluginhost/runtime.go` stores managed runtimes in a two-level map:

```go
rt map[string]map[runtimeKey]*runtimeEntry  // orgID -> {RuntimeID, RuntimeName} -> entry
```

This means two orgs that each configure a channel named `feishu-main` get **two independent runtime instances**, each with its own SDK client. The `runtimeKey` is a struct, not a string concat, so there is no collision via separator escaping.

`Host.Shutdown(ctx)` tears down only the current org's bucket; `Host.Stop(ctx)` is reserved for process-exit and walks every bucket.

## Manifest plugin defaults + per-org overrides

The manifest baked into the binary (`resources.BuiltinPluginsYAML()`) is the **single source of truth** for plugin defaults. Per-tenant overrides live in `settings_manifest_plugin_override` (PK = `(plugin_id, org_id)`):

| Column                  | Semantics                                                                             |
| ----------------------- | ------------------------------------------------------------------------------------- |
| `plugin_id`             | manifest plugin ID                                                                    |
| `org_id`                | tenant                                                                                |
| `enabled`               | nullable; `NULL` = use manifest default, non-`NULL` = explicit override               |
| `session_env_vault_key` | empty = fallback default, non-empty = vault blob holding the session_env override map |

Override semantics are sparse: only fields that diverge from the default are persisted. `SaveManifestPlugins` drops the row only when nothing is left to override — the requested `enabled` matches the manifest default **and** no `session_env_vault_key` binding is set. When a session env binding exists, the row is kept and `enabled` is stored as `NULL` so it still falls back to the default. This keeps the DB minimal-diff without clobbering the session env dimension.

Reads go through `Server.resolveManifestPlugins(r)` which overlays `ListManifestPluginOverrides` onto the builtin manifest before returning. The builtin manifest itself is never mutated.

`Reconcile` (binary / skill installation) installs into `$STELLA_HOME/bin` and the system skill directory — **system-wide resources with no org dimension**. `SyncManifestPlugins` does pass the override-applied manifest to `Reconcile`, so per-org enable state reaches it, but the install layer (`StellaHome`, the bin directory, the reconcile state file) is shared across all orgs. Per-org enable/disable only governs whether the agent runtime invokes the plugin, not which binaries are installed. Making the install layer org-aware is tracked separately (see issue #244).

## OAuth provider per-org overrides

A separate table, `plugin_oauth_provider`, holds per-org OAuth client overrides:

- `client_id`, `client_secret_enc` (vault-encrypted), `redirect_url`, `org_id`
- Updated via `PUT /api/admin/oauth-providers/{id}/config`
- Read by `credentials.Service.GetEffectiveOAuthConfig(ctx, providerID)` which merges DB override over manifest YAML defaults
- `GET` responses redact the secret (returns `"***"` when set)

The OAuth flow's `state` parameter binds the `orgID` (`oauth.FlowStatus.OrgID`); `CompleteAuthCodeFlowWithOrigin` injects it into ctx before the token exchange, so callbacks always run under the right org.

## What was removed

- `~/.stella/plugins.yaml` and the `manifestplugins.LoadUser` / `Merge` / `MergeRaw` loaders. Per-tenant tunables now go through the REST API → DB.
- The MCP plugin (`internal/tools/mcp`). The runtime model and `RegisterBuiltinTools` it relied on are gone. Any legacy `settings_plugin` row with `id='tool/mcp'` is inert — no plugin is registered under that ID, so it never resolves to a runtime.

## Migrating from the old plugins.yaml

If you previously ran a fork that wrote `~/.stella/plugins.yaml`:

1. The file is now ignored at startup; no error, no migration.
2. Re-apply any per-tenant overrides via the Plugins page in the Web UI (one org at a time), or programmatically via `PUT /api/manifest-plugins`.
3. Delete the stale file once you've finished migrating.
