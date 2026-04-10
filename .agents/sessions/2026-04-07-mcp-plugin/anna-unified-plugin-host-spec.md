# Anna Unified Plugin Host Spec

## Purpose

Define a concrete target architecture for evolving Anna from kind-specific plugin registries into a unified plugin host model inspired by pi extensions.

The key design goal is:

> A plugin is a module that registers capabilities with a host. Tool/provider/channel/hook/runtime/admin are capability types, not plugin types.

This spec is the implementation target for moving advanced built-in plugins — starting with MCP — into fully plugin-owned packages without relying on ad hoc `internal/...` escape hatches.

---

## 1. Problems with the Current Model

Anna's current plugin system is split across multiple kind-specific systems:

- `plugins/tools/*`
- `plugins/hooks/*`
- `plugins/providers/*`
- `plugins/channels/*` (hardcoded construction, not even a registry)
- `plugins/memory/*`

This causes four architectural problems:

### 1.1 Plugin kind is overloaded as identity

Current persistence and admin ownership are based on `(kind, name)`:

- `tool/mcp`
- `provider/openai`
- `channel/telegram`

This works for simple one-capability plugins, but breaks down for composite plugins that own multiple related capabilities.

### 1.2 Runtime-heavy plugins do not fit cleanly

MCP is the clearest example:

- it exposes a tool
- it owns a long-lived runtime manager
- it needs typed config validation
- it needs admin status reporting
- it contributes prompt-visible discovered tools

The current model only cleanly supports the tool part.

### 1.3 Registries are fragmented and inconsistent

Tools/hooks/providers/memory each have separate registries.
Channels are still hardcoded in `cmd/anna/channels.go`.
Standalone services like `reflect` are not modeled as plugins in the same sense at all.

### 1.4 Admin and lifecycle integration are special-cased

Runtime plugins require custom wiring in:

- `cmd/anna/gateway.go`
- `internal/admin/server.go`
- `internal/admin/plugins.go`

This makes advanced plugins depend on internals rather than the plugin platform.

---

## 2. Target Design Principles

### 2.1 Plugin package is the primary unit

The package layout should move toward:

```text
plugins/
  mcp/
  telegram/
  anthropic/
  trace/
  reflect/
```

not primarily:

```text
plugins/tools/mcp/
plugins/providers/anthropic/
plugins/hooks/trace/
```

Kind-specific subdirectories may still exist temporarily during migration, but they are no longer the conceptual model.

### 2.2 Plugins can register multiple capabilities

A single plugin may register any combination of:

- tool
- provider
- channel
- hook
- memory backend
- runtime/background service
- admin config/status integration
- prompt/resource contribution

### 2.3 Capabilities are explicit, typed, and narrow

Anna should copy pi's multi-capability extension shape, but keep the APIs narrower and more explicit.

Plugins should not receive broad access to DB/router/auth internals.

### 2.4 Runtime ownership must be first-class

Plugins that run background services must have standard lifecycle support:

- startup
- reconcile on config change
- stop on disable
- status reporting

### 2.5 Backward compatibility matters

The new system should be introduced with adapters so existing plugin code can migrate incrementally.

---

## 3. Core Abstraction

Each plugin exports a single registration entrypoint.

```go
package plugins

type Plugin interface {
    Register(host Host)
}

type PluginFunc func(host Host)
```

A package can register itself with a global plugin catalog.

```go
func Register(name string, plugin Plugin)
func Names() []string
func Get(name string) (Plugin, bool)
```

Registration is one per plugin package, not one per capability kind.

---

## 4. Host API

The host is the only object plugins use to contribute capabilities.

```go
type Host interface {
    Logger(pluginID string) *slog.Logger

    RegisterTool(ToolRegistration)
    RegisterProvider(ProviderRegistration)
    RegisterChannel(ChannelRegistration)
    RegisterHook(HookRegistration)
    RegisterMemory(MemoryRegistration)
    RegisterRuntime(RuntimeRegistration)
    RegisterAdmin(AdminRegistration)
    RegisterPromptContributor(PromptContributorRegistration)

    Config() ConfigService
    Runtime() RuntimeLookup
}
```

### 4.1 Why this shape

This gives Anna the pi-style multi-capability registration model, while keeping:

- capability boundaries explicit
- host powers narrow
- runtime/admin integration first-class

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

`Name` is the tool name visible to the agent.
A plugin may register multiple tools if needed.

## 5.2 Provider capability

```go
type ProviderRegistration struct {
    PluginID string
    Name     string
    Meta     ProviderMeta
    Build    func(ctx ProviderContext) (providers.ProviderAdapter, error)
}
```

A plugin may register one or more providers.

## 5.3 Channel capability

```go
type ChannelRegistration struct {
    PluginID string
    Name     string
    Build    func(ctx ChannelContext) (pkgchannel.Channel, error)
}
```

This replaces hardcoded channel construction over time.

## 5.4 Hook capability

```go
type HookRegistration struct {
    PluginID string
    Name     string
    Build    func(ctx HookContext) (hooks.HookPlugin, error)
}
```

## 5.5 Memory capability

```go
type MemoryRegistration struct {
    PluginID string
    Name     string
    Build    func(ctx context.Context, build MemoryContext) (memory.Provider, error)
}
```

## 5.6 Runtime capability

```go
type RuntimeRegistration struct {
    PluginID string
    Name     string
    Create   func(ctx RuntimeContext) (Runtime, error)
}
```

```go
type Runtime interface {
    Start(ctx context.Context, state PluginState) error
    Reconcile(ctx context.Context, state PluginState) error
    Stop(ctx context.Context) error
    Status(ctx context.Context) (any, error)
}
```

This is the capability MCP was missing from the current plugin system.

## 5.7 Admin capability

```go
type AdminRegistration struct {
    PluginID string

    ValidateConfig func(raw map[string]any) error
    RedactConfig   func(raw map[string]any) map[string]any

    ConfigSchema func() any
    Status       func(ctx context.Context) (any, error)
}
```

This does **not** expose raw router internals.
The platform remains responsible for serving generic admin APIs.

## 5.8 Prompt/resource contribution

```go
type PromptContributorRegistration struct {
    PluginID string
    Name     string
    Contribute func(ctx context.Context) (PromptContribution, error)
}
```

For MCP this can power discovered-tool prompt inventory without exposing full prompt-template mutation APIs.

---

## 6. Plugin Identity and Persistence Model

## 6.1 Plugin identity

Every plugin has one stable ID:

- `mcp`
- `telegram`
- `trace`
- `anthropic`
- `reflect`

Capability names are scoped under the plugin but do not define plugin identity.

### Example

- plugin `mcp`
  - tool capability `mcp`
  - runtime capability `mcp`
  - admin capability `mcp`
  - prompt contributor `mcp`

- plugin `telegram`
  - channel capability `telegram`
  - admin capability `telegram`

## 6.2 Persistence target model

Move toward storing plugin state by plugin ID only:

```go
type PluginState struct {
    ID      string         `json:"id"`
    Enabled bool           `json:"enabled"`
    Config  map[string]any `json:"config"`
}
```

Target persistence shape in `settings_plugins`:

| field | meaning |
|---|---|
| `id` | plugin ID |
| `enabled` | plugin-level enablement |
| `config` | plugin-owned config blob |

### Transitional compatibility

For migration safety, Anna may temporarily continue storing:

- `kind`
- `name`

but they become compatibility metadata, not the source of truth for the plugin model.

Recommended transition:

- keep current table shape first
- reinterpret rows as plugin state keyed by canonical plugin ID
- later simplify schema if worthwhile

---

## 7. Public vs Internal Boundaries

## 7.1 Public/shared plugin platform APIs

These should become stable plugin-facing APIs:

- plugin catalog registration
- `Host`
- registration structs for each capability
- `ConfigService`
- `RuntimeLookup`
- runtime lifecycle/status interfaces
- narrow build contexts per capability

These should live in a shared package, e.g.:

```text
pkg/plugins/
```

## 7.2 Must remain internal

The following must not be exposed directly to plugins:

- `internal/config.Store`
- raw DB handles
- `internal/admin.Server`
- raw HTTP router internals
- `internal/auth` engine and policy internals
- `internal/agent.PoolManager`
- notification dispatcher internals
- prompt template rendering internals
- full bootstrap sequencing details

The host should adapt internal services into narrow plugin-facing APIs.

---

## 8. Config Service

Plugins need typed config ownership, but not raw store access.

```go
type ConfigService interface {
    Get(ctx context.Context, pluginID string) (PluginState, error)
    Set(ctx context.Context, pluginID string, config map[string]any) error
}
```

Admin save flow becomes generic:

1. resolve plugin by ID
2. if plugin has `AdminRegistration.ValidateConfig`, run it
3. persist config via `ConfigService`
4. if plugin has runtime, reconcile runtime

This removes MCP-specific config validation branches from admin code.

---

## 9. Runtime Lookup

Tools and prompt contributors may need access to a plugin-owned runtime.

```go
type RuntimeLookup interface {
    Get(pluginID string) (RuntimeHandle, bool)
}

type RuntimeHandle interface {
    Status(ctx context.Context) (any, error)
}
```

For plugin-internal type assertions, plugin code may cast to its own runtime interface.

Example for MCP tool build:

- host runtime lookup retrieves runtime for plugin `mcp`
- tool uses that runtime instead of `internal/mcp.DefaultManager()`

---

## 10. Admin API Model

The admin server should become generic around plugin capabilities.

## 10.1 Generic plugin config update

Current:
- MCP-specific validation/reconcile logic in `internal/admin/plugins.go`

Target:
- generic `PUT /api/plugin-config/{id}`
- generic validation through plugin admin capability
- generic runtime reconcile through runtime manager

## 10.2 Generic plugin status endpoint

Current:
- MCP-specific `/api/plugin-status/tool/mcp`

Target:
- generic `GET /api/plugin-status/{id}`
- if plugin has runtime/admin status, return it
- else return empty object

## 10.3 UI rendering model

Short term:
- preserve plugin-specific UI code in the admin frontend
- drive it from generic config/status endpoints

Long term:
- optionally add schema-driven or component-driven admin metadata

Do **not** overdesign admin rendering in the first migration.
MCP should first move to generic backend ownership while keeping current UI structure.

---

## 11. Prompt Contribution Model

Anna should not expose arbitrary prompt mutation APIs to plugins initially.

Instead, define narrow structured prompt contributions.

```go
type PromptContribution struct {
    ToolInventory []PromptToolInfo
    Notes         []string
}
```

```go
type PromptToolInfo struct {
    Name        string
    Description string
    Metadata    map[string]any
}
```

The core prompt builder remains owner of final rendering.

For MCP:
- plugin contributes discovered MCP tools as structured inventory
- prompt builder renders them in the existing tools section
- prompt rules like “call get before exec” remain platform-controlled or plugin-contributed as structured notes

---

## 12. Backward-Compatible Implementation Strategy

This should be an additive migration.

## 12.1 New package

Introduce:

```text
pkg/plugins/
internal/pluginhost/
```

Where:
- `pkg/plugins` defines stable plugin-facing interfaces
- `internal/pluginhost` implements catalog loading, runtime management, config/status dispatch

## 12.2 Adapter layer for existing registries

Keep existing registries working by adapting them into the new host internally.

Examples:

- existing tool registrations can still populate `ToolRegistration`
- existing provider registrations can still populate `ProviderRegistration`
- existing hook registrations can still populate `HookRegistration`
- memory registry can adapt similarly

Channels should be migrated from hardcoded builders to real registrations as a later phase.

## 12.3 Transitional package layout support

Temporarily support both:

- `plugins/tools/mcp`
- `plugins/mcp`

with one delegating to the other if needed.

The end-state should favor plugin-centric package layout.

---

## 13. MCP Migration Plan Under This Spec

## Phase 1 — Build the host platform

Deliver:
- plugin catalog
- `Host`
- capability registration structs
- runtime manager
- generic config/status plumbing
- adapters for existing registries

No behavior change yet.

## Phase 2 — Move MCP to unified registration

Create plugin package:

```text
plugins/mcp/
```

Register:
- runtime capability
- tool capability
- admin capability
- prompt contributor

Keep `internal/mcp` temporarily as compatibility shim if needed.

## Phase 3 — Remove MCP special cases

Delete:
- `SetMCPLifecycle`
- MCP-specific admin branches
- MCP-specific gateway wiring
- `DefaultManager()` singleton access from prompt/tool code

## Phase 4 — Physically move MCP runtime/config code

Move:
- config
- manager
- session
- supervisor
- status

from `internal/mcp` to `plugins/mcp`.

## Phase 5 — Use MCP as the reference implementation

Document MCP as the first advanced plugin using the unified host model.

---

## 14. Migration Plan for Other Existing Plugins

## 14.1 Providers

Move provider packages to unified plugin registration while preserving current adapter behavior.

Examples:
- `plugins/anthropic`
- `plugins/openai`
- `plugins/openai-response`

## 14.2 Channels

Replace hardcoded `buildChannel()` with channel capability registration.

Examples:
- `plugins/telegram`
- `plugins/qq`
- `plugins/feishu`
- `plugins/weixin`

These plugins can also register admin capabilities for config validation and status.

## 14.3 Hooks

Move hook plugins to unified registrations without changing hook runtime semantics.

## 14.4 Reflect
n
Model `reflect` as a plugin with:
- runtime capability
- admin capability

This removes its current standalone special-case status.

## 14.5 Memory

Memory remains special in that only one may be active at a time, but it still fits as a capability registration.

---

## 15. Risks and Mitigations

## 15.1 Import cycles

**Risk:** very high if plugins import internal host code and internal code imports plugins.

**Mitigation:**
- plugin-facing APIs live in `pkg/plugins`
- host implementation lives in `internal/pluginhost`
- plugins import only `pkg/...`

## 15.2 Overexposing internals

**Risk:** plugin platform becomes a thin wrapper over internal implementation details.

**Mitigation:**
- only expose narrow interfaces
- keep DB/router/auth/pool internals private

## 15.3 Runtime ordering issues

**Risk:** tools/providers/channels may initialize before plugin runtimes or config services are ready.

**Mitigation:**
Establish startup order:
1. build plugin catalog
2. create host
3. load plugin state
4. instantiate runtimes
5. start enabled runtimes
6. build pools/channels/providers/hooks/tools against the host

## 15.4 Persistence transition complexity

**Risk:** moving away from `kind/name` may complicate migration and admin compatibility.

**Mitigation:**
- treat plugin ID as the canonical identifier first
- preserve `kind`/`name` fields during transition
- only simplify schema later if needed

## 15.5 Testing surface expansion

**Risk:** a unified host could be harder to test if too much logic centralizes.

**Mitigation:**
Test at three layers:
- host registration/unit tests
- runtime manager tests
- plugin integration tests

---

## 16. Non-Goals

This spec does **not** propose:

- third-party sandboxed plugin execution
- arbitrary external plugin installation format changes
- broad arbitrary prompt mutation APIs
- immediate schema-driven admin UI generation
- replacing all current plugin packages in one PR

The goal is a cleaner built-in plugin platform, not a full marketplace/plugin sandbox system.

---

## 17. Recommended Initial Deliverables

The first implementation slice should deliver:

1. `pkg/plugins` host + capability registration interfaces
2. `internal/pluginhost` implementation
3. catalog registration for built-in plugins
4. generic runtime status/config plumbing
5. MCP migrated to the new model

This is the smallest coherent unit that proves the architecture.

---

## 18. Bottom Line

Anna should evolve from:

> many kind-specific plugin systems

into:

> one plugin host, many optional capabilities per plugin

This matches the strongest part of pi's extension design while staying compatible with Anna's needs around:

- background runtimes
- admin config/status
- multi-user/channel services
- explicit typed interfaces

MCP is the correct first migration target because it exercises nearly every missing platform capability.
