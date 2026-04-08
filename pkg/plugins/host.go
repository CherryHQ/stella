package plugins

import "log/slog"

// Host exposes plugin registration and service consumption surfaces.
type Host interface {
	Registry() RegistryHost
	Services() ServiceHost
}

// RegistryHost accepts plugin capability registrations.
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
	RegisterSystemPrompt(SystemPromptRegistration)
	RegisterMetadata(PluginMeta)
}

// ServiceHost exposes narrow platform services available to plugins.
type ServiceHost interface {
	Logger(pluginID string) *slog.Logger
	Config() ConfigService
	Runtime() RuntimeLookup
	ChannelRuntime() ChannelRuntimeServices
	ReflectRuntime() ReflectRuntimeServices
}
