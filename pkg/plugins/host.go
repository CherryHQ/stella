package plugins

import "log/slog"

// Host is the flat registration surface exposed to plugins.
// Platform services are provided only through capability-specific contexts.
type Host interface {
	Registry() RegistryHost
	Services() ServiceHost
	SetInfo(PluginInfo)
	AddAdmin(AdminSpec)
	AddTool(ToolSpec)
	AddProvider(ProviderSpec)
	AddChannel(ChannelSpec)
	AddHook(HookSpec)
	AddMemory(MemorySpec)
	AddRuntime(RuntimeSpec)
	AddPromptInventory(PromptInventorySpec)
	AddSystemPrompt(SystemPromptSpec)
	AddBeforeRun(BeforeRunSpec)
	AddBeforeToolCall(BeforeToolCallSpec)
	AddAfterToolResult(AfterToolResultSpec)
}

// RegistryHost is the legacy registration surface retained as a migration shim.
type RegistryHost interface {
	RegisterMetadata(PluginInfo)
	RegisterTool(ToolSpec)
	RegisterProvider(ProviderSpec)
	RegisterChannel(ChannelSpec)
	RegisterHook(HookSpec)
	RegisterMemory(MemorySpec)
	RegisterRuntime(RuntimeSpec)
	RegisterConfig(AdminSpec)
	RegisterStatus(AdminSpec)
	RegisterPromptInventory(PromptInventorySpec)
	RegisterSystemPrompt(SystemPromptSpec)
	RegisterBeforeRun(BeforeRunSpec)
	RegisterBeforeToolCall(BeforeToolCallSpec)
	RegisterAfterToolResult(AfterToolResultSpec)
}

// Platform is the plugin-scoped service surface available during build/runtime work.
type Platform interface {
	Logger() *slog.Logger
	ConfigStore() ConfigStore
	StateStore() StateStore
	Scheduler() Scheduler
	Notifier() Notifier
	Auth() Auth
	RuntimeLookup() RuntimeLookup
	ChannelPlatform() ChannelPlatform
	ReflectPlatform() ReflectPlatform
}

// ServiceHost is the legacy unscoped service surface retained as a migration shim.
type ServiceHost interface {
	Logger(pluginID string) *slog.Logger
	Config() ConfigService
	Runtime() RuntimeLookup
	Notifications() Notifier
	Scheduler() SchedulerService
	StateStore() PluginStateStore
	Auth() Auth
	ChannelRuntime() ChannelPlatform
	ReflectRuntime() ReflectPlatform
}
