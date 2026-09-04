---
title: Channel Platform API
description: Scoped host services available to managed channel plugins.
---

## Scope

`pkg/plugins.Platform` belongs to the managed plugin host. Today its production
consumer is the channel family; provider and sandbox extensions do not use it.

A channel declares required capabilities in `PluginInfo.RequiredCapabilities`.
At seal time the host checks that every declared service is backed. An accessor
for an undeclared capability returns `nil`.

## Available Services

The interface exposes `Logger`, `ConfigStore`, `StateStore`, `Notifier`, `Auth`,
`RuntimeLookup`, and `ChannelPlatform`. Each accessor is gated by its matching
capability. `ChannelPlatform` supplies the parent context, channel handler, and
notification registry used to construct a managed channel runtime.

## Rules

- Declare only the services the channel needs and nil-check optional accessors.
- Use `ConfigStore` for desired config and `StateStore` for derived operational
  state.
- Use `RuntimeLookup` for status instead of retaining global runtime pointers.
- Do not add provider or sandbox dependencies to `Platform`; their typed
  registries are injected into their actual consumers.

Manifest integrations cannot declare or receive `Platform` capabilities.
