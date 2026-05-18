package plugins

import "log/slog"

// Host is the flat registration surface exposed to plugins.
// Platform services are provided only through capability-specific contexts.
type Host interface {
	SetInfo(PluginInfo)
	AddAdmin(AdminSpec)
	AddTool(ToolSpec)
	AddProvider(ProviderSpec)
	AddChannel(ChannelSpec)
	AddHook(HookSpec)
	AddRuntime(RuntimeSpec)
	AddPromptInventory(PromptInventorySpec)
	AddSystemPrompt(SystemPromptSpec)
	AddBeforeRun(BeforeRunSpec)
	AddBeforeToolCall(BeforeToolCallSpec)
	AddAfterToolResult(AfterToolResultSpec)
	AddSessionEnv(SessionEnvSpec)
	AddBundledSkill(BundledSkillSpec)
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
	SkillStore() SkillStore
}
