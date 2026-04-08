# Anna Extension Platform

## Summary

The final state should be a **repo-level extension system**, not a nicer built-in plugin registry.

Use one top-level concept everywhere:

- **extension**: the ownership and packaging unit
- **contribution**: one thing an extension adds to Anna

An extension may contribute multiple things:

- tools
- providers
- hooks
- channels
- memory backends
- managed runtimes
- prompt inventory
- config schema
- status

All extensions live in this repository and ship in the Anna binary.

## Problems In The Previous Draft

The previous design was cleaner than the current codebase, but it was still not the final state.

The main problems were:

1. It still centered the design around built-in Go packages.
2. It still assumed extension code could depend on app-private implementation details.
3. It still treated kinds like `tool` and `channel` as too central.
4. It kept too much host logic coupled to Go registration patterns.

That is enough for internal modularity, but not enough for a real extension ecosystem.

## Final Design Principles

1. There is exactly one extension interface.
2. An extension can expose multiple contributions.
3. Extension config should be uniform and host-driven, not scattered across app code.
4. Extension execution is isolated from app internals.
5. App-private code stays private.

## Core Model

### Extension

An extension is the unit Anna installs, enables, disables, configures, upgrades, and trusts.

Each extension has:

- stable ID
- version
- display metadata
- config schema
- one or more contributions

### Contribution

A contribution is one capability entry exposed by an extension.

Examples:

- one extension contributes two tools and one hook
- one extension contributes one channel and one managed runtime
- one extension contributes one memory backend and one prompt-inventory source

The host manages extensions. The runtime uses contributions.

That is the clean split.

## One Extension Interface

The extension API should collapse to one interface:

```go
type Extension interface {
	Manifest() Manifest
	Contributions() []Contribution
}
```

`Manifest()` is pure metadata.

`Contributions()` returns typed contribution descriptors owned by the extension.

The point is not that every contribution has the same runtime behavior. The point is that every extension enters the system through one door.

## Manifest

The manifest must be stable and serializable inside the app.

```go
type Manifest struct {
	ID          string
	Version     string
	DisplayName string
	Description string
	Config      ConfigSchema
}
```

Required rules:

- `ID` is globally unique.
- `Version` is extension version.
- `Config` is schema data, not Go-only validation logic.

## Contribution Model

Use a tagged union style contribution descriptor:

```go
type Contribution struct {
	Kind ContributionKind
	Name string

	Tool            *ToolContribution
	Provider        *ProviderContribution
	Hook            *HookContribution
	Channel         *ChannelContribution
	Memory          *MemoryContribution
	Runtime         *RuntimeContribution
	PromptInventory *PromptInventoryContribution
	Status          *StatusContribution
}
```

This keeps the model uniform while still allowing typed in-process behavior.

Kinds are contribution kinds, not extension kinds.

That removes the old confusion where a "tool plugin" or "channel plugin" was both packaging and behavior.

## IDs

Use one ID scheme for extensions:

```text
{name}
```

Examples:

- `mcp`
- `telegram`
- `reflect`
- `openai`

Use separate contribution IDs derived from extension ID plus contribution name:

```text
{extension_id}:{contribution_name}
```

Examples:

- `mcp:mcp`
- `mcp:runtime`
- `telegram:channel`

This is cleaner than overloading `kind/name` as both persistence key and ownership key.

## Host Responsibilities

The host should be renamed conceptually from "plugin host" to **extension host**.

Its responsibilities are:

- discover extensions
- validate manifests
- persist desired extension state
- resolve extension config
- construct and cache managed runtime instances
- expose contributions to the rest of the app
- collect status
- support reload, enable, disable, and upgrade flows

The host owns orchestration. It does not own extension behavior.

## What Should Stay Internal

`internal/extensionhost` is still valid in the final state.

It should own only app composition:

- persistence bridge
- runtime instance cache
- process supervision
- enable/disable orchestration
- extension discovery/indexing
- admin enable/disable/update flows

What must not happen:

- extension packages importing `internal/...`
- extension-specific logic living in the host
- hardcoded per-extension registration paths

## What Extensions May Import

Final rule:

- extension code imports only `pkg/extensions` and other stable `pkg/...` contracts
- extension code never imports `internal/...`

If extension authors need a reusable helper, that helper belongs in a stable public package.

Good examples:

- `pkg/extensions`
- `pkg/channel`
- `pkg/tools`
- `pkg/providers`
- `pkg/memory`

Bad examples:

- `internal/channel`
- `internal/reflect`
- `internal/extensionhost`
- `internal/admin`

## Config

Config must be schema-based.

Do not make config validation depend on Go functions only.

The host should use a portable schema format such as JSON Schema for:

- defaults
- validation
- redaction hints
- field descriptions
- UI rendering

Typed in-process helpers are still fine inside an extension, but the host contract should remain schema-driven so config handling is consistent across all repo extensions.

## Runtime Contract

Managed runtimes still use declarative apply semantics:

```go
type ManagedRuntime interface {
	Apply(ctx context.Context, desired ExtensionState) error
	Stop(ctx context.Context) error
	Snapshot(ctx context.Context) (RuntimeSnapshot, error)
}
```

This is a host-side runtime contract used by repo extensions that own managed background behavior.

## Prompt Integration

Prompt contribution should stay inventory-based.

Extensions may contribute:

- prompt-visible tools
- prompt inventory metadata
- optional instructions scoped to their contribution

But prompt composition remains host-owned.

Extensions should not directly mutate the final system prompt string.

That keeps prompt assembly deterministic and reviewable.

## Admin Integration

The host should provide one generic admin surface:

- list available extensions
- show manifest
- upgrade with normal app code updates
- enable / disable
- edit config from schema
- show status
- inspect contributions

Custom UI should be optional, not required.

The system should work with schema-driven admin pages by default.

## Packaging Layout

Final naming should reflect the final model:

```text
pkg/extensions/
  api.go
  manifest.go
  contribution.go
  schema.go
  runtime.go

internal/extensionhost/
  host.go
  store.go
  runtime.go
  discovery.go
  supervisor.go
  adapters.go

extensions/
  mcp/
  telegram/
  reflect/
  openai/
```

If the repository keeps `plugins/` for migration, that is transitional. The final state should use `extensions/`.

## All Repo Extensions Should Share One Model

The strongest rule in the final design is:

**All repo extensions must use the same manifest, config, contribution, status, and lifecycle model.**

That means:

- same manifest shape
- same config schema model
- same contribution model
- same status model
- same enable/disable lifecycle

If one repo extension needs a fundamentally different host contract, the design is not finished.

## Migration End State

The final state should remove these assumptions entirely:

- built-in extension name lists in config
- hardcoded per-extension host registration functions
- extension-specific admin routes
- extension packages depending on `internal/...`
- Go `init()` registration as the only extension discovery mechanism
- `kind/name` as the primary extension identity

Some of these may exist during migration. None should remain in the finished architecture.

## Bottom Line

The final design should be:

1. one extension interface
2. multiple contributions per extension
3. one manifest and config model for all repo extensions
4. host-owned orchestration, extension-owned behavior
5. no extension package importing app-private code

That is the actual final state for a repo-scoped extension system.
