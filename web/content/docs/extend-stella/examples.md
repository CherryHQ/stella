---
title: Extension Examples
description: Small examples of Stella's supported extension paths.
---

## Provider

`plugins/providers/openai/` exports a `Definition()` with stable metadata and a
builder that converts `providers.Config` into the package's native config.
`cmd/stellad/setup_providers.go` explicitly includes it. This makes supported
provider types auditable without `init()` side effects.

## Sandbox Backend

`plugins/sandbox/docker/` owns Docker-specific session behavior and imports no
Stella internal package. `cmd/stellad/setup_sandboxes.go` supplies the selected
image, embedded bundle revision, mount sources, and error guidance when adapting
that factory into the agent backend registry.

## Managed Channel

`plugins/channels/telegram/plugin.go` is the reference for
`RegisterManagedChannelPlugin`. It keeps metadata, config schema and validation,
redaction, configured detection, and runtime construction under one channel ID.

## Manifest CLI Tool

GitHub CLI is manifest-only: its entry declares a managed binary and OAuth-backed
session environment. There is no Go `tool/gh` package and no runtime registration
callback. See [Manifest Tool Integrations](/docs/extend-stella/manifest-tools)
for the schema and complete examples.
