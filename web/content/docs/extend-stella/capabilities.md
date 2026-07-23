---
title: Capabilities
description: Reference for every capability type in Stella's plugin system.
---

## Capability Reference

Capabilities are the things a plugin owns and registers with `Host`.

Every capability spec includes `PluginID` so the host can associate it with the correct plugin state and metadata.

## AdminSpec

`AdminSpec` owns operator-facing config and status behavior.

Fields:

- `DefaultConfig`
- `Schema`
- `Validate`
- `Redact`
- `Status`

Use it when a plugin needs any of:

- default config values
- admin form schema
- server-side validation
- secret redaction
- live status output

Most managed plugins register one `AdminSpec` for config and a second `AdminSpec` for status.

## ToolSpec

`ToolSpec` registers an agent-callable tool.

Important fields:

- `Name`
- `Description`
- `Required`
- `Build`

`Required: true` means the tool is treated as core and built even without plugin enablement.

Use `ToolContext` when building:

- `Platform`
- `Paths`
  - `UserRoot`
  - `ToolsBinDir`
  - `StellaHome`
  - `AgentRoot`
  - `ProjectRoot`
- `Runtime`

`ProjectRoot` is the current attached project, if any. Project-aware tools should resolve relative paths from `ProjectRoot` instead of depending on runner cwd details.

`Runtime` is the tool-facing file and process capability surface. Use it instead of depending on sandbox internals; the runner decides whether those operations are local, sandboxed, or otherwise mediated.

## ProviderSpec

`ProviderSpec` registers a provider adapter.

Important fields:

- `Name`
- `Meta`
- `Build`

`Meta` controls display behavior such as provider name and default URL.

Use `ProviderContext` when the provider build needs plugin config or scoped services.

## ChannelSpec

`ChannelSpec` registers a channel capability.

Important fields:

- `Name`
- `SupportsNotifications`
- `Configured`
- `NotificationsEnabled`
- `Build`

Most managed channels also register a `RuntimeSpec` because the host needs to reconcile long-lived channel services from plugin config.

If you are building a managed messaging integration, prefer `RegisterManagedChannelPlugin(...)` instead of hand-writing the repetitive registration pieces.

## HookSpec

`HookSpec` registers a hook plugin.

Important fields:

- `Name`
- `Build`

Use it for engine interception, observability, request tracking, or similar cross-cutting behavior.

## MemorySpec

`MemorySpec` registers a memory provider.

Important fields:

- `Name`
- `Build`

`MemoryContext` includes `DB` and `SummarizerFn` because memory providers need construction-time access to those inputs. That is a narrow exception, not a general plugin service bag.

## RuntimeSpec

`RuntimeSpec` registers a host-managed long-lived runtime.

Important fields:

- `Name`
- `Build`

Use it when the plugin owns a process-local runtime that should be reconciled from desired plugin state.

Examples:

- MCP manager
- managed channel runtimes

## PromptInventorySpec

`PromptInventorySpec` contributes tool inventory to prompt construction.

Important field:

- `GetTools`

Use it when a plugin exposes tools dynamically and the agent should see a structured list.

The MCP plugin is the main example: the plugin owns one generic tool capability, but it also contributes discovered MCP tools into prompt inventory.

## SystemPromptSpec

`SystemPromptSpec` contributes prompt sections.

Important fields:

- `Name`
- `Required`
- `Build`

Use it when a plugin needs to add structured system prompt content.

## Lifecycle Capabilities

There are three lifecycle specs:

- `BeforeRunSpec`
- `BeforeToolCallSpec`
- `AfterToolResultSpec`

These are for dynamic behavior around model turns and tool execution.

Typical uses:

- rewriting prompt state before a run
- blocking or rewriting tool arguments
- post-processing tool results

Each one includes:

- `Name`
- `Order`
- `Required`
- `Run`

`Order` determines execution ordering when multiple lifecycle plugins participate.

## Capability Selection Guide

If your plugin needs:

- an agent tool: use `ToolSpec`
- a provider adapter: use `ProviderSpec`
- a messaging integration: use `ChannelSpec`, usually with `RuntimeSpec`
- a host-managed service: use `RuntimeSpec`
- config and operator status: use `AdminSpec`
- prompt-visible dynamic tools: use `PromptInventorySpec`
- system prompt additions: use `SystemPromptSpec`
- request or tool interception: use lifecycle specs

It is normal for one plugin to register several capability types.

## Capability Composition Examples

Simple tool plugin:

- `SetInfo`
- `AddTool`

Configurable tool plugin:

- `SetInfo`
- `AddAdmin`
- `AddTool`

Managed runtime plugin:

- `SetInfo`
- `AddAdmin` for config
- `AddRuntime`
- `AddAdmin` for status

Managed channel plugin:

- `SetInfo`
- `AddAdmin`
- `AddChannel`
- `AddRuntime`

Dynamic tool inventory plugin:

- `SetInfo`
- `AddTool`
- `AddRuntime`
- `AddPromptInventory`

## Capability Metadata Must Match Reality

Your `PluginInfo.Capabilities` list should describe the capabilities the plugin actually registers.

If the plugin registers runtime, config, and status behavior, list runtime, config, and status.

Do not treat `Capabilities` as marketing copy. It is part of how the host and admin surfaces understand the plugin.
