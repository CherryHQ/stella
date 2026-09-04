---
title: Plugin System
---

> This section is for developers contributing to Stella.

## Plugin Means Ownership, Not One Runtime API

Stella compiles trusted integrations into `stellad`, but does not route every
integration through one universal host. Each family has the smallest contract
that matches its lifecycle:

| Family           | Registration                                               | Runtime owner                       |
| ---------------- | ---------------------------------------------------------- | ----------------------------------- |
| Providers        | explicit `providers.Definition` list in `cmd/stellad`      | provider registry                   |
| Sandbox backends | explicit `sandbox.BackendDefinition` list in `cmd/stellad` | agent sandbox                       |
| Channels         | compiled catalog registration                              | channel plugin host                 |
| CLI tools        | built-in manifest                                          | sandbox session and prompt assembly |

Native agent tools, hooks, and memory implementations remain internal modules.
They should not become plugins merely to make the directory layout look uniform.

## Dependency Boundary

Production code under `plugins/providers/**` and `plugins/sandbox/**` may depend
on public contracts under `pkg/**`, sibling family packages, the standard
library, and third-party modules. It must not import `internal/**`.

The composition root in `cmd/stellad` is allowed to know both sides. It selects
the compiled definitions, supplies Stella-owned dependencies, validates duplicate
IDs, and injects immutable registries into the services that execute them. The
table-driven guard in `internal/boundary_test.go` enforces this direction.

## Provider Adapters

A provider package exports a `Definition()` containing its ID, display metadata,
default URL, and adapter builder. `setupProviderRegistry()` lists the supported
definitions explicitly. Adding a package without adding that wiring has no
runtime effect.

Provider credentials, base URLs, models, and enabled state are managed by the
provider control plane. Providers are not plugin rows and are not enabled or
reloaded through the plugin admin surface.

## Sandbox Backends

A sandbox package implements the public sandbox interfaces. The composition root
adapts it into a `sandbox.BackendDefinition`, supplying process-owned concerns
such as the embedded runtime bundle and development image selection. The agent
sandbox receives only the resulting registry and never imports a concrete
backend.

Backend selection is static for a runner configuration. There is no dynamic
unregister or rollback protocol because Stella does not load third-party backend
code at runtime.

## Channels

Channels still use the existing managed plugin host and package registration.
This change deliberately leaves their configuration, enablement, notification,
and quiesce lifecycle untouched. See [Create a Plugin](/docs/extend-stella/create-a-plugin)
for the current channel authoring path.

## Manifest CLI Integrations

CLI integrations that only contribute binaries, skills, prompt guidance, or
session environment use the built-in manifest. They do not need a Go plugin
package. See [Manifest Tool Integrations](/docs/extend-stella/manifest-tools).

## Choosing A Boundary

Use the first contract that fits:

1. Keep ordinary behavior in its owning internal package.
2. Use the manifest for declarative CLI integration.
3. Add a typed family definition for a compiled provider or sandbox backend.
4. Use the managed plugin host only for a channel lifecycle.

A new capability on the generic host is an escalation, not the default extension
mechanism.
