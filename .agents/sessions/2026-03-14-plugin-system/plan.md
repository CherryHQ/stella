# Plan: Dual Plugin System (Go + JS)

## Overview

Add an extensible plugin system to Anna supporting two plugin types:
1. **Go plugins** — local `.go` packages compiled into the binary via a Caddy-style builder
2. **JS extensions** — local `.js` files executed at runtime via QuickJS (CGO-free, Wasm-based)

Both types register through a unified `plugin.Registry`, starting with tool registration + lifecycle event hooks.

### Goals

- Unified plugin interface that abstracts Go vs JS implementation details
- Go plugins: local files, `anna plugin add/remove/list/build` CLI
- JS extensions: local files, `anna plugin add/remove/list` CLI, sandboxed
- Lifecycle hooks: `before_tool_call`, `after_tool_call`, `on_session_start`, `on_session_end`
- Host APIs for JS: `readFile`, `writeFile`, `fetch`, `log`
- Plugin config in `~/.anna/config.yaml`

### Success Criteria

- [ ] Go plugin can register a custom tool, gets compiled into binary via `anna plugin build`
- [ ] JS extension can register a custom tool, loaded at runtime without recompilation
- [ ] Both plugin types coexist and register through the same registry
- [ ] Lifecycle hooks fire correctly for both plugin types
- [ ] JS extensions are sandboxed — only access granted host APIs
- [ ] `anna plugin add/remove/list/build` commands work for both types
- [ ] Plugin config section in config.yaml supports per-plugin settings
- [ ] Tests pass with `-race`, lint clean

### Out of Scope

- Remote plugin repositories / package registry
- Plugin dependency resolution
- Channel, provider, or runner plugins (future)
- JS extension hot-reload without restart (v1 loads at startup; hot-reload is v2)
- Plugin versioning / semver constraints

## Technical Approach

### Architecture

```
~/.anna/plugins/
  ├── weather.js         # JS extension
  ├── github-tools/      # Go plugin (local package)
  │   ├── plugin.go
  │   └── go.mod
  └── ...

~/.anna/config.yaml:
  plugins:
    - path: ~/.anna/plugins/weather.js
      config:
        api_key: "xxx"
    - path: ~/.anna/plugins/github-tools
      config:
        token: "yyy"
```

```
┌──────────────────────────────────────────────────────┐
│                    anna binary                        │
│                                                      │
│  ┌────────────────┐    ┌──────────────────────────┐  │
│  │ Go Plugins     │    │ JS Extensions            │  │
│  │ (compiled in)  │    │ (QJS runtime per-plugin) │  │
│  │                │    │ + mutex for concurrency   │  │
│  │ init() →       │    │                          │  │
│  │ RegisterFactory│    │ LoadJS() →               │  │
│  │               │    │ eval module →            │  │
│  └───────┬────────┘    └──────────┬───────────────┘  │
│          │                        │                  │
│          ▼                        ▼                  │
│  ┌─────────────────────────────────────────────────┐ │
│  │          plugin.Manager                         │ │
│  │  ┌──────────────────────────────────────────┐   │ │
│  │  │ plugin.Registry                          │   │ │
│  │  │  - tools map[string]Tool                 │   │ │
│  │  │  - hooks map[EventKind][]HookFunc        │   │ │
│  │  └──────────────────────────────────────────┘   │ │
│  └──────────────────────┬──────────────────────────┘ │
│                         ▼                            │
│  ┌─────────────────────────────────────────────────┐ │
│  │  setup() in cmd/anna/commands.go                │ │
│  │  - adapts plugin.Tools → tool.Tool (with        │ │
│  │    collision check against built-in names)      │ │
│  │  - passes Registry into LoopConfig.PluginHooks  │ │
│  │  - fires session hooks from Pool callbacks      │ │
│  └─────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────┘
```

### Components

- **`pkg/plugin`** — Public plugin contract: `Factory`, `Plugin`, `Tool`, `HookFunc`, `EventKind`, event types. Importable by external Go plugin modules.
- **`internal/plugin`** — Internal: `Registry`, `Manager`, `AdaptTool`. Not importable externally.
- **`internal/plugin/jsrt`** — JS runtime: QJS wrapper, host API bindings, per-plugin mutex
- **`internal/plugin/goplugin`** — Go plugin builder: generates main.go + go.mod, invokes `go build`
- **`cmd/anna/plugin.go`** — CLI commands: `anna plugin add/remove/list/build`
- **`internal/config`** — Extended with `Plugins []PluginConfig` field

### Data Models

```go
// pkg/plugin/types.go — PUBLIC, importable by external Go plugins

// Factory is registered by Go plugins in init(). Manager instantiates
// plugins from factories after config is loaded.
type Factory struct {
    Name string
    New  func(cfg map[string]any) (Plugin, error)
}

// Plugin is what both Go and JS plugins implement.
type Plugin interface {
    Name() string
    Init(ctx Context) error
    Close() error
}

// Context is passed to Plugin.Init(), providing registration APIs.
type Context struct {
    Config   map[string]any       // per-plugin config from yaml
    Logger   *slog.Logger
    RegisterTool func(Tool) error
    OnEvent      func(EventKind, HookFunc)
}

// Tool is a plugin-provided tool.
type Tool struct {
    Name        string
    Description string
    InputSchema map[string]any
    Execute     func(ctx context.Context, args map[string]any) (string, error)
}

// EventKind identifies lifecycle events.
type EventKind string

const (
    EventBeforeToolCall EventKind = "before_tool_call"
    EventAfterToolCall  EventKind = "after_tool_call"
    EventSessionStart   EventKind = "session_start"
    EventSessionEnd     EventKind = "session_end"
)

// HookFunc is a lifecycle hook handler.
// For "before" events, returning a non-nil error cancels the action.
type HookFunc func(ctx context.Context, event any) error

// BeforeToolCallEvent is passed to before_tool_call hooks.
type BeforeToolCallEvent struct {
    ToolName  string
    Arguments map[string]any
}

// AfterToolCallEvent is passed to after_tool_call hooks.
type AfterToolCallEvent struct {
    ToolName string
    Result   string
    IsError  bool
}

// SessionEvent is passed to session_start and session_end hooks.
type SessionEvent struct {
    SessionID string
    Channel   string
}
```

```go
// pkg/plugin/register.go — global factory registry for Go plugins

var (
    mu        sync.Mutex
    factories []Factory
)

// Register is called from Go plugin init() functions to register a factory.
func Register(f Factory) {
    mu.Lock()
    factories = append(factories, f)
    mu.Unlock()
}

// Factories returns all registered factories (called by Manager at startup).
func Factories() []Factory {
    mu.Lock()
    defer mu.Unlock()
    result := make([]Factory, len(factories))
    copy(result, factories)
    return result
}
```

### APIs / Interfaces

```go
// internal/plugin/registry.go

// Registry collects tools and hooks from all plugins.
// Tools stored in map for O(1) lookup. Thread-safe.
type Registry struct {
    mu       sync.RWMutex
    tools    map[string]plugin.Tool  // keyed by tool name
    hooks    map[plugin.EventKind][]plugin.HookFunc
    reserved map[string]struct{}     // built-in tool names that cannot be overridden
}

func NewRegistry(builtinNames []string) *Registry  // reserves built-in names
func (r *Registry) RegisterTool(t plugin.Tool) error  // error on duplicate OR reserved name
func (r *Registry) RegisterHook(event plugin.EventKind, fn plugin.HookFunc)
func (r *Registry) Tools() map[string]plugin.Tool
func (r *Registry) RunHooks(ctx context.Context, event plugin.EventKind, data any) error
```

```go
// internal/plugin/manager.go

// Manager discovers, loads, and manages all plugins.
// LoadAll is best-effort: logs warnings for failing plugins, continues loading.
type Manager struct {
    registry *Registry
    plugins  []plugin.Plugin
    logger   *slog.Logger
}

func NewManager(logger *slog.Logger, builtinToolNames []string) *Manager
func (m *Manager) LoadAll(configs []config.PluginConfig) error
func (m *Manager) Registry() *Registry
func (m *Manager) Close() error  // closes all plugins in reverse order
```

```go
// internal/plugin/adapt.go

// adaptedTool wraps plugin.Tool to satisfy tool.Tool interface.
type adaptedTool struct {
    inner plugin.Tool
}

func (a *adaptedTool) Definition() toolspec.Definition {
    return toolspec.Definition{
        Name:        a.inner.Name,
        Description: a.inner.Description,
        InputSchema: a.inner.InputSchema,
    }
}

func (a *adaptedTool) Execute(ctx context.Context, args map[string]any) (string, error) {
    return a.inner.Execute(ctx, args)
}

func AdaptTool(t plugin.Tool) tool.Tool {
    return &adaptedTool{inner: t}
}
```

```go
// internal/plugin/jsrt/runtime.go

// JSPlugin wraps a single JS extension loaded via QJS.
// All JS calls are serialized via mu since QJS runtimes are not goroutine-safe.
type JSPlugin struct {
    name    string
    mu      sync.Mutex
    runtime *qjs.Runtime
}

func LoadJS(path string, cfg map[string]any, registry *Registry, logger *slog.Logger) (*JSPlugin, error)
func (p *JSPlugin) Name() string
func (p *JSPlugin) Close() error
```

```js
// JS extension API (exposed to plugins)
// All host APIs are synchronous except fetch.
// Lifecycle hooks (anna.on) must be synchronous — returning a string blocks
// the action with that string as the reason.
// Tool execute functions may use async/await.
export default function(anna) {
    // anna.config — per-plugin config from yaml (read-only object)
    // anna.log(level, msg) — structured logging via host slog
    // anna.readFile(path) → string — sync, scoped to allowedDirs
    // anna.writeFile(path, content) — sync, scoped to allowedDirs
    // anna.fetch(url, options?) → { status, body } — async (Promise)
    //   options: { method, headers, body, timeout (ms, default 30000, max 60000) }
    //   max response body: 1MB, http/https only

    anna.registerTool({
        name: "my_tool",
        description: "...",
        parameters: { type: "object", properties: { ... } },
        execute: async (args) => { return "result"; }
    });

    // Hooks are SYNCHRONOUS. Return a string to block (for "before" events).
    // Return undefined/null to allow.
    anna.on("before_tool_call", (event) => {
        anna.log("info", `calling: ${event.toolName}`);
        // return "reason" to block, or return nothing to allow
    });
}
```

**JS File Access Scoping Rules:**
- `readFile` / `writeFile` paths are resolved relative to the plugin's parent directory
- Absolute paths allowed only within: (a) plugin's parent dir, or (b) `~/.anna/workspace/`
- Symlinks resolved via `filepath.EvalSymlinks` before path check — no symlink escapes
- Access outside allowed dirs returns a permission error

**JS fetch constraints:**
- Schemes: `http` and `https` only
- Timeout: configurable per-call, default 30s, max 60s
- Response body: max 1MB, truncated with warning
- Cancellation: respects Go context

```go
// internal/plugin/goplugin/builder.go

// Builder compiles a custom anna binary with Go plugins.
//
// Go plugins are local directories with a Go package that calls
// plugin.Register() in init():
//
//   ~/.anna/plugins/my-plugin/
//     ├── go.mod       (module example.com/my-plugin)
//     ├── plugin.go    (func init() { plugin.Register(plugin.Factory{...}) })
//
// Note: plugins import "github.com/vaayne/anna/pkg/plugin" (public package),
// NOT "internal/plugin" which is app-private.
//
// Build steps:
// 1. Create temp directory
// 2. Generate main.go with blank imports for all plugin packages
// 3. Generate go.mod requiring anna + plugin modules
// 4. Add `replace` directives for local plugin dirs
// 5. Run `go build -o <output>`
type Builder struct {
    annaModule  string   // github.com/vaayne/anna
    annaVersion string   // current version or "latest"
    plugins     []string // local paths to plugin packages
    output      string   // output binary path
}

func NewBuilder(plugins []string, output string) *Builder
func (b *Builder) Build(ctx context.Context) error
```

### Hook Integration with Engine

Plugin hooks are threaded via `LoopConfig` (not `ToolCallbacks`) so they flow through all `engine.Run` callers including `DelegateTool`:

```go
// internal/agent/engine/types.go — add PluginHooks field
type LoopConfig struct {
    Model           ai.Model
    StreamOptions   ai.StreamOptions
    MaxTurns        int
    Tools           ToolSet
    ToolDefinitions []toolspec.Definition
    System          string
    Interrupt       <-chan struct{}
    PluginHooks     PluginHookRunner  // NEW — nil = no hooks
}

// PluginHookRunner is an interface so engine doesn't import internal/plugin.
type PluginHookRunner interface {
    RunHooks(ctx context.Context, event string, data any) error
}
```

In `ExecuteToolCalls`, the hooks are extracted from `LoopConfig` (passed as a new parameter or via `ToolCallbacks`):

```go
// tool_execution.go — updated signature
type ToolCallbacks struct {
    OnStart     func(call ai.ToolCall)
    OnFinish    func(result ai.ToolResultMessage)
    PluginHooks PluginHookRunner  // from LoopConfig
}

// Before calling toolFn:
if cb.PluginHooks != nil {
    if err := cb.PluginHooks.RunHooks(ctx, "before_tool_call", plugin.BeforeToolCallEvent{
        ToolName: call.Name, Arguments: call.Arguments,
    }); err != nil {
        // Hook blocked the tool call
        result := ai.ToolResultMessage{
            ToolCallID: call.ID, ToolName: call.Name, IsError: true,
            Content: []ai.ContentBlock{ai.TextContent{Text: "blocked by plugin: " + err.Error()}},
        }
        results = append(results, result)
        if cb.OnFinish != nil { cb.OnFinish(result) }
        continue
    }
}

// After toolFn returns:
if cb.PluginHooks != nil {
    _ = cb.PluginHooks.RunHooks(ctx, "after_tool_call", plugin.AfterToolCallEvent{
        ToolName: call.Name, Result: content.Text, IsError: err != nil,
    })
}
```

**Threading path:** `setup()` → `GoRunnerConfig.PluginHooks` → `GoRunner` stores it → `GoRunner.Chat()` sets `LoopConfig.PluginHooks` → `engine.Run()` passes to `ExecuteToolCalls` → hooks fire.

**DelegateTool** also gets hooks: `DelegateConfig` gains a `PluginHooks PluginHookRunner` field, threaded into its child `LoopConfig`. This ensures subagent tool calls also trigger plugin hooks.

### Session Lifecycle Hooks

Session hooks fire from Pool, not engine:

```go
// Pool.getOrCreateRunner — after creating a new runner:
if pluginHooks != nil {
    _ = pluginHooks.RunHooks(ctx, "session_start", plugin.SessionEvent{
        SessionID: sessionID, Channel: sess.Info.Channel,
    })
}

// Pool.ArchiveSession — before closing runner:
if pluginHooks != nil {
    _ = pluginHooks.RunHooks(context.Background(), "session_end", plugin.SessionEvent{
        SessionID: sessionID, Channel: info.Channel,
    })
}

// Pool.Close — for each session:
if pluginHooks != nil {
    _ = pluginHooks.RunHooks(context.Background(), "session_end", plugin.SessionEvent{
        SessionID: id, Channel: sess.Info.Channel,
    })
}
```

Pool gains a `pluginHooks PluginHookRunner` field set via a `PoolOption`.

### Hook Cancellation Contract (unified Go + JS)

- **Go**: Return a non-nil `error` to block a "before" event. The error message becomes the block reason.
- **JS**: Return a string to block. Return `undefined`/`null`/nothing to allow.
- **JS bridge**: If JS hook returns a string, the Go bridge wraps it as `errors.New(reason)`.
- **"after" events**: Return values are ignored — after hooks are fire-and-forget.

### CLI Commands Surface

```
anna plugin list                  # List all configured plugins (name, type, path, status)
anna plugin add <path>            # Add local .js file or Go dir to config.yaml plugins list
  --config key=value              # Optional: set per-plugin config values (repeatable)
anna plugin remove <name|path>    # Remove plugin from config.yaml by name or path
anna plugin build                 # Compile custom binary with all Go plugins
  --output <path>                 # Output binary path (default: ./anna-custom)
```

- `add` validates path exists, detects type (.js file vs directory with go.mod)
- `add` appends to `~/.anna/config.yaml` plugins list, deduplicates by path
- `remove` removes from config only (doesn't delete files)
- `build` requires Go toolchain; checks `go version` and errors clearly if missing
- `list` shows: name, kind (go/js), path, load status from last startup

## Implementation Steps

### Phase 1: Core Plugin Framework

1. Create `pkg/plugin/` — public contract: types, factory registry (files: `types.go`, `register.go`)
2. Create `internal/plugin/` — registry, manager, adapt (files: `registry.go`, `manager.go`, `adapt.go`)
3. Add `Plugins []PluginConfig` to `Config` struct (files: `internal/config/config.go`)
4. Add `PluginHookRunner` interface to engine, add `PluginHooks` to `LoopConfig` (files: `internal/agent/engine/types.go`)
5. Wire hooks into `ExecuteToolCalls` — before/after tool call (files: `internal/agent/engine/tool_execution.go`)
6. Thread hooks through GoRunner: `GoRunnerConfig.PluginHooks` → `LoopConfig.PluginHooks` (files: `internal/agent/runner/gorunner.go`)
7. Thread hooks through DelegateTool: `DelegateConfig.PluginHooks` → child `LoopConfig` (files: `internal/agent/tool/delegate.go`)
8. Wire plugin manager into `setup()` — load plugins, adapt tools with collision check, set hooks (files: `cmd/anna/commands.go`)
9. Add session hooks to Pool — fire `session_start`/`session_end` from Pool lifecycle (files: `internal/agent/pool.go`, `internal/agent/pool_options.go`)

### Phase 2: JS Extension Runtime

10. Add `fastschema/qjs` dependency (files: `go.mod`, `go.sum`)
11. Create `internal/plugin/jsrt/runtime.go` — QJS runtime with per-plugin mutex, module loader, JS→Go tool/hook bridging
12. Create `internal/plugin/jsrt/hostapi.go` — host APIs: `readFile`, `writeFile`, `fetch`, `log` with path scoping + fetch limits
13. Wire JS loading into Manager.LoadAll — detect `.js` extension, call `jsrt.LoadJS()` (files: `internal/plugin/manager.go`)

### Phase 3: Go Plugin Builder

14. Create `internal/plugin/goplugin/builder.go` — generates main.go + go.mod with replace directives
15. Create `internal/plugin/goplugin/templates.go` — Go text/templates for generated files

### Phase 4: CLI Commands

16. Create `cmd/anna/plugin.go` — `anna plugin` subcommand with list/add/remove/build

### Phase 5: Tests & Polish

17. Unit tests for pkg/plugin (factory registry), internal/plugin (registry collision, hooks, manager, adapt)
18. Unit tests for jsrt (load valid/invalid JS, sandbox, host APIs, concurrent access via mutex, exceptions)
19. Unit tests for goplugin builder (correct generation, missing Go toolchain)
20. Integration test: JS extension registers tool → engine executes it
21. Integration test: before_tool_call hook blocks tool
22. Integration test: session hooks fire from Pool
23. Lint, format, update docs + builtin anna skill

## Testing Strategy

### Unit Tests

- `pkg/plugin`: Register factories, Factories() returns copies, concurrent Register is safe
- `internal/plugin.Registry`: register tools (O(1) lookup), duplicate name → error, reserved built-in name → error, register hooks, RunHooks (before cancels on error, after ignores error), concurrent RegisterTool + RunHooks
- `internal/plugin.Manager`: detect kind (go=dir with go.mod, js=.js file), best-effort loading, Close in reverse order
- `internal/plugin.AdaptTool`: wraps Execute, synthesizes Definition
- `jsrt.LoadJS`: valid module, syntax error → error, sandbox (no global fs/net), host APIs, exception in init → error, exception in execute → error string, concurrent tool calls serialized by mutex
- `goplugin.Builder`: correct main.go imports, go.mod with replace, missing `go` → clear error

### Integration Tests

- End-to-end: JS extension registers tool → setup loads → engine calls → correct result
- Hook blocking: before_tool_call returns error → engine returns "blocked by plugin"
- Session hooks: Pool creates runner → session_start fires; Pool archives → session_end fires
- Multi-plugin: Go + JS both register tools → all tools in definitions, no collision with built-ins
- Plugin failure: one JS has syntax error → others still load, broken logged

### Edge Cases

- Plugin tool named "read" or "bash" → rejected by reserved name check
- Plugin with duplicate tool name → `RegisterTool` returns error, logged
- JS extension throws during init → LoadJS returns error, Manager logs, continues
- JS extension tool throws during execute → caught, error string to LLM
- Empty plugins config → no-op, no crash
- Invalid plugin path → Manager logs, continues
- JS `fetch` to unreachable URL → timeout error
- JS `fetch` returns >1MB → truncated with warning
- JS `readFile` outside allowed dirs → permission error
- JS `writeFile` with symlink escape → resolved, permission error
- Go plugin build with no Go toolchain → "go binary not found in PATH"
- Concurrent tool calls from multiple sessions hit same JS plugin → mutex serializes safely
- DelegateTool subagent calls plugin tool → hooks still fire

## Considerations

### Security

- JS sandboxed via QJS/Wazero — zero host access except granted APIs
- `readFile`/`writeFile` scoped to plugin parent dir + `~/.anna/workspace/`; symlinks resolved
- `fetch`: http/https only, 30s default timeout (60s max), 1MB response cap
- Go plugins have full access (compiled in) — same trust as any Go dependency
- Reserved tool names prevent plugins from shadowing built-in tools

### Performance

- JS runtime startup: ~1-2ms per QJS instance (Wasm)
- JS tool calls serialized per-plugin via mutex — no concurrent JS execution within one plugin
- Go plugins: zero overhead (native code)
- Plugin loading once at startup — not on hot path
- Registry uses `map[string]Tool` for O(1) tool lookup
- Hook execution sequential per event kind

### Risks & Mitigations

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| QJS API instability | Low | Medium | Pin version, wrap in thin adapter |
| JS mutex contention under load | Medium | Low | Per-plugin mutex; only blocks same plugin |
| Go builder needs Go toolchain | Medium | Low | Detect, warn; document in README |
| Plugin tool name conflicts | Medium | Low | Reserved names + duplicate rejection |
| fastschema/qjs platform support | Low | High | Verify darwin-arm64 + linux-amd64 in CI |
| pkg/plugin API stability | Medium | Medium | Keep minimal; only types + Register |

### Resolved Questions

- **Go plugin importability**: Public contract in `pkg/plugin`, internal logic in `internal/plugin`
- **Hook threading**: Via `LoopConfig.PluginHooks` interface, flows through engine + delegate
- **JS concurrency**: Per-plugin mutex serializes all JS calls
- **Session hooks**: Fired from Pool.getOrCreateRunner and Pool.ArchiveSession/Close
- **Hook cancellation**: Go returns error, JS returns string; bridge converts
- **Tool collisions**: Reserved built-in names + duplicate rejection
- **Fetch safety**: http/https only, timeout, body cap
- **Go plugin init() vs config**: Factory pattern — init registers factory, Manager instantiates with config later

## Review Feedback

### Round 1 (internal reviewer)

6 blockers resolved: hook passing, config struct, AdaptTool, load failure, JS async, file scoping.

### Round 2 (gpt-5.4 codex, thinking high)

4 critical + 4 warnings resolved:
1. **Engine threading** → Hooks via `LoopConfig.PluginHooks` (interface), not just `ToolCallbacks`. DelegateTool also threads hooks.
2. **Session lifecycle hooks** → Fired from Pool (getOrCreateRunner, ArchiveSession, Close), not engine.
3. **Go plugin importability** → Public contract moved to `pkg/plugin`; internal logic stays in `internal/plugin`.
4. **JS concurrency** → Per-plugin `sync.Mutex` serializes all QJS calls.
5. **Go plugin factory pattern** → init() registers Factory (name + constructor); Manager instantiates with config at runtime.
6. **Tool name collisions with built-ins** → Registry tracks reserved names, rejects collisions.
7. **JS fetch safety** → http/https only, 30s timeout, 1MB body cap.
8. **Hook cancellation consistency** → Go: return error; JS: return string; bridge converts string→error.

## Implementation Progress

(Updated during implementation phase)
