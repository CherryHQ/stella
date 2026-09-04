---
title: Extension Capabilities
description: Reference for Stella's family-specific extension contracts.
---

## Provider Definitions

`pkg/providers.Definition` contains `ID`, `Name`, `DefaultURL`, and `Build`.
`Build` receives deployment-owned `APIKey` and `BaseURL` values and returns a
`ProviderAdapter`. The registry rejects empty metadata, nil builders, and
duplicate IDs, then exposes sorted provider types to the control plane.

## Sandbox Backends

Backend packages implement the public contracts in `pkg/sandbox`. The
composition root adapts them into `internal/agent/sandbox.BackendDefinition`
values because creation also needs internal runner paths, policy, mount sources,
and user identity. The agent package receives only the validated registry.

## Managed Channels

Channels use `pkg/plugins.RegisterManagedChannelPlugin`. A managed channel owns
its `PluginInfo`, config defaults and schema, validation and redaction, configured
check, and runtime factory. Its public host capabilities are `AdminSpec`,
`ChannelSpec`, and `RuntimeSpec`.

The host also contains older tool, hook, prompt, and lifecycle capability types.
They are not the default authoring path for new native features. Keep native
behavior in its internal owner unless a real managed extension boundary exists.

## Manifest CLI Integrations

A manifest entry can contribute binaries, bundled skills, prompt guidance,
session environment, and an OAuth provider. It cannot receive host `Platform`
services or arbitrary Go callbacks.
