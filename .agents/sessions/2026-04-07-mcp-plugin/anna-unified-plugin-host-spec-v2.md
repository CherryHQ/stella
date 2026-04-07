# Anna Unified Plugin Host Spec v2

## Status

This document supersedes the earlier unified-plugin-host draft.

It keeps the core direction:

> A plugin is the primary ownership unit, and it may register multiple capabilities.

But it tightens the design in several important ways:

- plugin identity and capability identity are separated
- runtime lifecycle becomes declarative
- admin is split into config/status concerns instead of one vague blob
- prompt contribution stays narrowly structured
- migration prioritizes behavior over package-layout churn

This is the recommended target design for Anna.

---

## 1. Design Goal

Anna should evolve from:

- multiple kind-specific plugin registries
- special-case runtime wiring for advanced plugins
- plugin ownership split across `plugins/...` and `internal/...`

into:

- one plugin host
- one plugin package as the ownership unit
- many explicit capability registrations per plugin
- generic config/runtime/status plumbing

The immediate forcing case is MCP, but the design must also fit:

- providers
- channels
- hooks
- memory backends
- runtime/background services like reflect

---

## 2. Core Principles

## 2.1 Plugin is the ownership unit

A plugin owns:

- config
- lifecycle
- runtime services it creates
- capability registrations
- admin/status integration

Examples of plugin IDs:

- `mcp`
- `telegram`
- `trace`
- `anthropic`
- `reflect`

## 2.2 Capability is the subsystem-facing unit

A plugin can register one or more capabilities, such as:

- tool
- provider
- channel
- hook
- memory backend
- runtime service
- config contract
- status contract
- prompt inventory contribution

A plugin may register multiple capabilities of the same type.

## 2.3 Plugin identity and capability identity are different

This distinction is required.

### Plugin identity

Used for:

- ownership
- config persistence
- enablement
- runtime lifecycle
- admin management

### Capability identity

Used for:

- subsystem registration
- lookup inside tools/providers/channels/hooks
- human-facing capability names

Example:

- plugin ID: `corp-ai`
- capabilities:
  - provider `corp-primary`
  - provider `corp-backup`
  - tool `corp-login`

Do not collapse these into a single identity model.

## 2.4 Host APIs must stay narrow

Plugins must not receive direct access to:

- raw DB handles
- raw admin router
- auth internals
- pool manager internals
- notifier internals
- prompt rendering internals

Anna should copy pi's **single extension / many capabilities** model, but not pi's fully open runtime surface.

## 2.5 Runtime ownership must be first-class

Any plugin with background work must fit the same platform model:

- desired state application
- status snapshots
- stop semantics
- reconciliation on config change

This is the main missing capability exposed by MCP.

---

## 3. Plugin Model

Each plugin exports a single registration entrypoint.

```go
package plugins

type Plugin interface {
    Register(host Host)
}

type PluginFunc func(host Host)
```

A global catalog registers plugins by plugin ID.

```go
func Register(id string, plugin Plugin)
func Get(id string) (Plugin, bool)
func Names() []string
```

This catalog is plugin-centric, not capability-kind-centric.

---

## 4. Host Shape

The host should be split into two concerns:

1. registration
2. service consumption

```go
type Host interface {
    Registry() RegistryHost
    Services() ServiceHost
}
```

## 4.1 Registry side

Used only to contribute capabilities.

```go
type RegistryHost interface {
    RegisterTool(ToolRegistration)
    RegisterProvider(ProviderRegistration)
    RegisterChannel(ChannelRegistration)
    RegisterHook(HookRegistration)
    RegisterMemory(MemoryRegistration)
    RegisterRuntime(RuntimeRegistration)
    RegisterConfig(ConfigRegistration)
    RegisterStatus(StatusRegistration)
    RegisterPromptInventory(PromptInventoryRegistration)
}
```

## 4.2 Service side

Used by plugins to consume narrow platform services.

```go
type ServiceHost interface {
    Logger(pluginID string) *slog.Logger
    Config() ConfigService
    Runtime() RuntimeLookup
}
```

### Why this split matters

Without this split, `Host` becomes a vague bag of powers.
This separation makes plugin behavior much easier to reason about and test.

---

## 5. Capability Registration Types

## 5.1 Tool capability

```go
type ToolRegistration struct {
    PluginID    string
    Name        string
    Description string
    Build       func(ctx ToolContext) (tools.Tool, error)
}
```

Notes:

- `Name` is the tool name visible to the agent.
- A plugin may register multiple tools.
- Tool build remains session/agent-facing, not process-lifecycle-facing.

## 5.2 Provider capability

```go
type ProviderRegistration struct {
    PluginID string
    Name     string
    Meta     ProviderMeta
    Build    func(ctx ProviderContext) (providers.ProviderAdapter, error)
}
```

Notes:

- Provider config persistence stays platform-owned.
- Model cache refresh and provider rebuild behavior remain platform concerns.

## 5.3 Hook capability

```go
type HookRegistration struct {
    PluginID string
    Name     string
    Build    func(ctx HookContext) (hooks.HookPlugin, error)
}
```

## 5.4 Channel capability

Channels are not as simple as tools/hooks, so the type should acknowledge operational semantics.

```go
type ChannelRegistration struct {
    PluginID               string
    Name                   string
    SupportsNotifications  bool
    Build                  func(ctx ChannelContext) (pkgchannel.Channel, error)
}
```

Notes:

- channels usually also need config and status registrations
- channel auth/linking flows may become their own extension point later

## 5.5 Memory capability

```go
type MemoryRegistration struct {
    PluginID string
    Name     string
    Build    func(ctx context.Context, build MemoryContext) (memory.Provider, error)
}
```

Notes:

- memory remains special in that only one may be active at a time
- this is a platform policy, not a reason to keep a separate plugin model

## 5.6 Runtime capability

This is the most important refinement in v2.

Do **not** use a vague `Start/Reconcile/Stop` contract.
Use a declarative desired-state model.

```go
type RuntimeRegistration struct {
    PluginID string
    Name     string
    Factory  func(ctx RuntimeContext) (ManagedRuntime, error)
}
```

```go
type ManagedRuntime interface {
    Apply(ctx context.Context, desired PluginState) error
    Stop(ctx context.Context) error
    Snapshot(ctx context.Context) (RuntimeSnapshot, error)
}
```

### Why `Apply()` instead of `Start()+Reconcile()`

Because the platform wants to express one thing:

- here is the desired plugin state (`enabled`, `config`)

Then the host decides whether to:

- create
- start
- reconfigure
- no-op
- stop

This is cleaner, easier to reason about, and less likely to produce lifecycle bugs.

## 5.7 Config capability

Admin/config behavior should not live in one generic blob.

```go
type ConfigRegistration struct {
    PluginID      string
    DefaultConfig func() map[string]any
    Validate      func(raw map[string]any) error
    Redact        func(raw map[string]any) map[string]any
}
```

Notes:

- `Redact` is optional but strongly recommended for secrets-bearing plugins
- typed decode remains plugin-internal implementation detail unless a shared helper emerges later

## 5.8 Status capability

```go
type StatusRegistration struct {
    PluginID string
    Get      func(ctx context.Context) (any, error)
}
```

Notes:

- in many plugins this will delegate to runtime snapshotting
- some plugins may expose status without owning a long-lived runtime

## 5.9 Prompt inventory capability

Prompt contribution should remain narrow and structured.
Do not allow arbitrary freeform prompt mutation in v1.

```go
type PromptInventoryRegistration struct {
    PluginID string
    Name     string
    GetTools func(ctx context.Context) ([]PromptToolInfo, error)
}
```

```go
type PromptToolInfo struct {
    Name        string
    Description string
    Metadata    map[string]any
}
```

Notes:

- this is enough for MCP discovered-tool inventory
- prompt guidance text like “call `mcp get` before `mcp exec`” should remain platform-owned initially

---

## 6. Persistence Model

## 6.1 Canonical plugin state

The canonical ownership record is plugin-level.

```go
type PluginState struct {
    ID      string         `json:"id"`
    Enabled bool           `json:"enabled"`
    Config  map[string]any `json:"config"`
}
```

## 6.2 Keep the current DB schema initially

Do **not** change the database schema in the first refactor unless forced.

Current `settings_plugins` fields:

- `id`
- `kind`
- `name`
- `enabled`
- `config`

Recommended interpretation during migration:

- `id` becomes the canonical plugin owner ID
- `kind` and `name` become transitional metadata / UI grouping hints

This avoids coupling architecture work to storage churn.

## 6.3 Capability-level enablement

Do not expose capability-level enablement in v1.
But do not design the system so it becomes impossible later.

### Recommended rule

- UX and persistence: plugin-level enablement
- internal architecture: capability-aware

This fits MCP well while preserving headroom for future composite plugins.

---

## 7. Config Service

Plugins should own config rules without getting raw store access.

```go
type ConfigService interface {
    Get(ctx context.Context, pluginID string) (PluginState, error)
    Set(ctx context.Context, pluginID string, config map[string]any) error
}
```

### Generic config save flow

1. resolve plugin by ID
2. if plugin registered config capability, validate incoming config
3. persist via `ConfigService`
4. if plugin has runtime capability, apply desired state through runtime host
5. return updated plugin state

This removes MCP-specific branches from admin handlers.

---

## 8. Runtime Host and Lookup

## 8.1 Runtime manager responsibilities

The platform owns the runtime state machine.
Plugins implement runtime behavior, not runtime orchestration policy.

### Runtime host responsibilities

- instantiate runtime objects from registrations
- keep one managed runtime per `(pluginID, runtimeName)`
- apply desired plugin state
- stop runtimes on disable/shutdown
- expose snapshots/status
- isolate failures so one runtime does not poison the whole plugin platform

## 8.2 Runtime lookup

```go
type RuntimeLookup interface {
    Get(pluginID string, runtimeName string) (RuntimeHandle, bool)
}
```

```go
type RuntimeHandle interface {
    Snapshot(ctx context.Context) (RuntimeSnapshot, error)
}
```

### Important note

Do not encourage arbitrary type assertions all over the codebase.
Instead, plugin packages should provide typed lookup helpers locally.

Example pattern for MCP:

```go
func LookupRuntime(host ServiceHost) (MCPRuntime, bool)
```

This keeps dynamic lookup localized and typed at the package boundary.

---

## 9. Public vs Internal Boundaries

## 9.1 Shared/public plugin platform APIs

These should live in a shared package such as:

```text
pkg/plugins/
```

They include:

- plugin registration interfaces
- host interfaces
- registration structs
- config service interface
- runtime lookup interfaces
- runtime snapshot types
- narrow build contexts

## 9.2 Must remain internal

These stay internal:

- `internal/config.Store`
- raw DB access
- `internal/admin.Server`
- raw HTTP router
- `internal/auth` engine/policy details
- `internal/agent.PoolManager`
- notifier internals
- prompt rendering internals
- bootstrap sequencing internals

The host adapts internal services into safe plugin-facing surfaces.

---

## 10. Admin Model

The earlier design over-grouped admin concerns. v2 splits them.

## 10.1 Config management

Generic backend path:

- `GET /api/plugins/{id}`
- `PATCH /api/plugins/{id}/enabled`
- `PUT /api/plugins/{id}/config`

Config save uses the plugin's `ConfigRegistration`.

## 10.2 Status reporting

Generic backend path:

- `GET /api/plugins/{id}/status`

If plugin has a registered status capability, return it.
Otherwise return `{}`.

## 10.3 UI metadata

Do not overdesign schema-driven admin rendering in v1.

Short-term approach:

- keep plugin-specific frontend components where useful
- back them with generic config/status APIs

Long-term possibility:

- optional UI metadata layer or schema helpers

This should not block the MCP migration.

---

## 11. Prompt Contribution Model

Prompt mutation is high-risk. Keep the first version narrow.

### Allowed in v1

- structured prompt inventory contributions

### Not allowed in v1

- arbitrary freeform prompt rewrites by plugins
- arbitrary instruction notes contributed by any plugin

### Why

Prompt policy should stay centrally controlled until the host model is proven.

For MCP this means:

- plugin contributes discovered tool inventory
- platform-owned prompt builder renders that inventory
- platform-owned prompt builder keeps the rule:
  - “always call `mcp get` before `mcp exec`”

---

## 12. Startup and Reconciliation Order

This needs to be explicit to avoid runtime ordering bugs.

Recommended order:

1. build plugin catalog
2. create plugin host
3. load persisted plugin state
4. instantiate registered runtimes
5. apply desired plugin state to runtimes
6. build providers/hooks/tools/channels/memory against the host
7. start agent pools / gateway services

### On config change

1. validate config via config capability
2. persist config
3. apply plugin state to runtime host
4. reload affected subsystem registrations where needed

### On enable/disable change

1. persist enabled flag
2. apply plugin state to runtime host
3. reload affected subsystem registrations where needed

---

## 13. Backward-Compatible Migration Strategy

The behavior migration matters more than path cleanup.

## 13.1 New packages

Introduce:

```text
pkg/plugins/
internal/pluginhost/
```

Where:

- `pkg/plugins` holds stable plugin-facing contracts
- `internal/pluginhost` implements catalog loading, runtime orchestration, config/status dispatch, and compatibility adapters

## 13.2 Keep existing package paths initially

Do not force a directory flattening in the first migration.

This is acceptable initially:

- `plugins/tools/mcp`
- `plugins/channels/telegram`
- `plugins/providers/anthropic`
- `plugins/hooks/trace`

as long as each package can register multiple capabilities through the new host.

### Why

The architectural win is the unified host, not the directory rename.
Package flattening can happen later if still desirable.

## 13.3 Compatibility adapters

The host should adapt the current registry-based systems during migration.

Examples:

- old tool registrations become `ToolRegistration`s
- old provider registrations become `ProviderRegistration`s
- old hook registrations become `HookRegistration`s
- old memory registrations become `MemoryRegistration`s

Channels will likely need a more deliberate migration because they are currently hardcoded.

---

## 14. MCP as the First Reference Plugin

MCP should register these capabilities:

1. `ConfigRegistration`
2. `RuntimeRegistration`
3. `ToolRegistration`
4. `StatusRegistration`
5. `PromptInventoryRegistration`

### MCP plugin-owned responsibilities

- typed config/defaults/validation
- runtime manager
- session/transport handling
- supervision/backoff/suppression
- tool facade
- status snapshot generation
- prompt inventory data

### Platform-owned responsibilities

- config persistence
- plugin toggle endpoints
- runtime orchestration
- generic status endpoint
- prompt rendering
- pool/plugin reload decisions

### Explicit cleanup target

After MCP migration, these special cases should disappear:

- `SetMCPLifecycle`
- MCP-specific admin config/status branches
- MCP-specific gateway wiring
- `DefaultManager()` singleton usage from outside the plugin package

---

## 15. Reflect as the Second Validation Target

Reflect is a strong second candidate because it is:

- runtime/background-service oriented
- admin-configured
- not a tool/provider/channel

If the host can model MCP and reflect cleanly, the abstraction is probably sound.

Recommended reflect capabilities:

- `ConfigRegistration`
- `RuntimeRegistration`
- `StatusRegistration`

---

## 16. Channels Under This Model

Channels should migrate after runtime semantics are proven.

A channel plugin will typically need:

- `ConfigRegistration`
- `ChannelRegistration`
- `StatusRegistration`

Potentially later:

- auth/linking capability for things like QR flows

Do not assume channels are as simple as tools/hooks.
Their notifier integration and lifecycle behavior are richer.

---

## 17. Risks and Mitigations

## 17.1 Import cycles

**Risk:** very high.

**Mitigation:**

- plugin-facing contracts in `pkg/plugins`
- host implementation in `internal/pluginhost`
- plugins import only `pkg/...`

## 17.2 Overexposing internals

**Risk:** plugins become internal code with a thin wrapper.

**Mitigation:**

- keep service host narrow
- no raw DB/admin/auth/pool access

## 17.3 Runtime lifecycle ambiguity

**Risk:** start/reconcile semantics drift across plugins.

**Mitigation:**

- use declarative `Apply()` contract
- platform owns orchestration state machine

## 17.4 Prompt sprawl

**Risk:** plugins gradually own prompt policy.

**Mitigation:**

- allow only structured prompt inventory in v1
- keep guidance text platform-owned

## 17.5 Schema churn distracting from architecture

**Risk:** database and package layout changes consume the migration.

**Mitigation:**

- keep DB schema stable initially
- delay package flattening

---

## 18. Non-Goals

This v2 design does **not** propose:

- sandboxed third-party plugin execution
- arbitrary external plugin marketplace design
- broad event-bus-first plugin behavior like pi
- arbitrary prompt mutation by plugins
- immediate schema-driven admin UI generation
- one-shot migration of every plugin package

This is a cleaner built-in plugin platform, not a full sandbox/plugin ecosystem redesign.

---

## 19. Recommended First Implementation Slice

The first coherent implementation slice should include:

1. `pkg/plugins` interfaces and registration types
2. `internal/pluginhost` implementation
3. compatibility adapters for current tool/provider/hook/memory systems
4. generic config/status runtime plumbing
5. MCP migrated to the new host model

### Out of scope for the first slice

- DB schema changes
- aggressive package layout flattening
- channel migration
- reflect migration
- generic admin schema rendering

This keeps the first slice focused, testable, and reviewable.

---

## 20. Bottom Line

The better design is not merely:

> one plugin can register many things

It is:

> one plugin owns config and lifecycle, registers many capabilities, and interacts with the platform through narrow typed services.

The critical refinements in this v2 design are:

- separate plugin identity from capability identity
- use declarative runtime application instead of ad hoc lifecycle methods
- split admin into config and status concerns
- keep prompt contribution narrow
- migrate architecture before storage/layout cleanup

This is the design Anna should build toward.
