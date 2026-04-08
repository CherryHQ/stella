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
- system prompt sections
- run lifecycle hooks
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

## Host-Owned Services

Extensions should gain power through narrow host services, not through direct
imports of app-private packages.

The next capability surfaces should be:

- notifications
- plugin state storage
- identity/auth lookups
- later, only if still justified, structured data access

Notifications are the immediate next step. The shape should be:

```go
type NotificationService interface {
	Notify(ctx context.Context, n channel.Notification) error
	NotifyUser(ctx context.Context, userID int64, n channel.Notification) error
}
```

Rules:

- expose the service through `pkg/plugins.ServiceHost`
- back it with app-owned routing inside `internal/pluginhost`
- do not expose `internal/notify`
- do not expose raw channel registries

That gives extensions repo-level power to send user-visible events while
keeping routing, identity preference, and delivery policy centralized.

Plugin state storage should follow the same rule: expose a host-owned service,
not raw database handles.

The shape should be a scoped JSON KV store:

```go
type PluginStateScope struct {
	Kind string
	ID   string
}

type PluginStateStore interface {
	Get(ctx context.Context, pluginID string, scope PluginStateScope, key string) (map[string]any, bool, error)
	Set(ctx context.Context, pluginID string, scope PluginStateScope, key string, value map[string]any) error
	Delete(ctx context.Context, pluginID string, scope PluginStateScope, key string) error
}
```

This is strong enough for plugin-owned runtime state, cursors, and watermarks,
while keeping schema, migrations, and DB policy host-owned.

Identity and auth access should also start as a narrow host-owned directory,
not as a full export of the policy engine.

The first shape should be:

```go
type AuthService interface {
	GetUser(ctx context.Context, userID int64) (UserInfo, error)
	ListUserIdentities(ctx context.Context, userID int64) ([]LinkedIdentity, error)
	GetIdentityByPlatform(ctx context.Context, platform, externalID string) (LinkedIdentity, error)
}
```

Rules:

- expose the service through `pkg/plugins.ServiceHost`
- include enough user metadata for role-aware behavior, but not the full auth engine
- do not expose `internal/auth`
- do not expose direct DB-backed auth stores to plugins

This is enough for plugin-owned routing, user-aware behavior, and identity
lookup, while leaving policy evaluation as a later deliberate capability if it
turns out to be necessary.

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

Prompt integration should not be hardcoded in app code.

The host owns prompt assembly. Extensions own prompt contributions.

There are two distinct layers:

1. **Declarative prompt sections**
2. **Run lifecycle hooks**

Do not collapse these into one mechanism.

### Declarative Prompt Sections

Prompt sections are for stable, structured prompt content contributed by an extension.

Examples:

- `skills` contributes a section that explains how to use the `skills` tool and what skills are available
- `mcp` contributes a section that describes discovered MCP tools
- a future repo-policy extension contributes a section with repository operating rules

This should be modeled as a normal extension contribution, not as an app-owned special case.

```go
type SystemPromptSectionContribution struct {
	Name string
	Build func(ctx context.Context, build PromptContext) (PromptSection, error)
}
```

Required rules:

- the host decides whether the owning extension is enabled
- the host gathers sections from enabled extensions
- the host orders and renders the final prompt
- extensions contribute content, not full-template ownership

This is the right level for predictable, reusable prompt building.

### Run Lifecycle Hooks

Prompt sections are not enough for full extension power.

Pi's `before_agent_start` model shows the missing class of behavior: some extensions need to react to a specific run, current prompt, or current runtime state.

Anna should support a narrow lifecycle hook layer for those cases.

The first high-value hook is:

```go
type BeforeRunHook interface {
	BeforeRun(ctx context.Context, req RunRequest) (RunMutation, error)
}
```

Where `RunMutation` can do a small set of things:

- append or replace system prompt text for this run
- inject structured context/messages for this run
- optionally annotate run metadata for observability

Do **not** start with a giant open-ended event bus. Start with a small set of run hooks that map to real needs.

The ordering should be:

1. base system prompt from agent config
2. host-built declarative prompt sections from enabled extensions
3. run hook mutations from enabled extensions
4. final prompt sent to the runner

This keeps the system understandable while still allowing extension-owned behavior.

### Why Both Layers Exist

If only declarative sections exist, extensions cannot adapt prompt behavior per run.

If only runtime hooks exist, every stable prompt block becomes imperative code and prompt assembly becomes harder to reason about.

The final model should keep both:

- sections for stable owned prompt content
- lifecycle hooks for dynamic run-time behavior

That is the closest Anna equivalent to pi's extension power without copying pi's UI-specific runtime model.

## Capability Roadmap

The next platform work should expose more extension power through host capabilities, not by leaking `internal/...` packages.

Priority order:

1. **Run lifecycle hooks**
2. **Notification capability**
3. **Plugin state storage**
4. **Identity/auth capability**
5. **DB-facing capability only if still necessary**

### Phase A: Run Lifecycle Hooks

Goal: let extensions influence one run at a time without owning the whole runner.

Start narrow:

- `BeforeRun`
  - receives current run context
  - may replace or augment the effective system prompt for that run
- later phases may add:
  - `BeforeToolCall`
  - `AfterToolResult`
  - `BeforeProviderRequest`

Important rule:

- do not start with a giant event bus
- add only the hooks that map to real repo extension needs

### Phase B: Notification Capability

Goal: let extensions notify users without importing `internal/notify` or channel internals.

Expose a narrow host service:

- send a notification to a user
- optionally include structured status/event metadata

### Phase C: Plugin State Storage

Goal: let extensions persist their own state without raw database access.

Expose a narrow storage surface:

- plugin-scoped key/value state
- optional per-user and per-agent scoping
- small document/blob payloads if needed

This should solve most extension persistence needs before raw SQL is considered.

### Phase D: Identity/Auth Capability

Goal: let extensions act on user-aware context without importing `internal/auth`.

Expose a narrow identity surface:

- current user/agent/session identity
- identity lookup where needed
- permission checks for sensitive actions

The host should remain the owner of policy.

### Phase E: DB Capability Re-evaluation

Only after Phases A-D are in place should the platform decide whether any DB-facing extension capability is still necessary.

If a DB capability is needed, prefer:

- domain-specific repository services
- read-only query surfaces
- explicitly scoped persistence helpers

Do not expose `*sql.DB` broadly as a shortcut.

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
