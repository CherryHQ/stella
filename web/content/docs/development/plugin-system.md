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

Channels use the existing managed plugin host and package registration. A
channel plugin owns its persisted configuration type, complete decoder,
validation, and redaction. The host stores only the channel registration and
asks that registration for the optional guest policy decoder, so guest
admission remains based on the current persisted record and fails closed when a
decoder is absent or rejects the complete configuration.

Account enrollment is a separate, capability-gated host port. A plugin receives
an enrollment port directly from `Platform`, independently of channel runtime
services. The capability declaration is the only opt-in; the plugin must own
exactly one registered channel, which determines its namespace. Plugin requests
contain profile data only: the host supplies the namespace separately to the
account transaction. Message handlers do not acquire account write access
through type assertions. Platform evidence,
such as Feishu tenant and canonical subject checks, remains in the owning
plugin. See [Create a Plugin](/docs/extend-stella/create-a-plugin) for the
current channel authoring path.

## Manifest CLI Integrations

CLI integrations that only contribute binaries, skills, prompt guidance, or
session environment use the built-in manifest. They do not need a Go plugin
package. See [Manifest Tool Integrations](/docs/extend-stella/manifest-tools).

Each shipped Skill has one authored owner in `resources/skills/core/<name>` or
`resources/skills/plugins/<kind>/<plugin>/<name>`. The generated resource
descriptor is the runtime authority for that owner; Skill frontmatter and
mutable user metadata cannot claim plugin ownership. A plugin's CLI version
and Skill content digest are independent update units, so either can change
without forcing the other to change.

The one-time ownership migration copies the legacy `plugin.enabled` intent into
`plugin_override`; when the two sources disagree, disabled wins. Legacy rows are
retained as dormant compatibility data, and existing override config and vault
references are preserved. Runtime and generic plugin-admin reads and writes use
the manifest override instead.

Disabling a manifest plugin prevents future Skill exposure, prompt inclusion,
and session environment injection. It does not revoke OAuth grants, remove
cached context or shared binaries, or stop an already running process. Existing
per-Agent Skill suppression remains a narrower preference and is evaluated
after precedence resolution. Binary installation status is reported separately
from enablement, so an enabled plugin may still expose its help and Skill when
installation needs repair.

## Choosing A Boundary

Use the first contract that fits:

1. Keep ordinary behavior in its owning internal package.
2. Use the manifest for declarative CLI integration.
3. Add a typed family definition for a compiled provider or sandbox backend.
4. Use the managed plugin host only for a channel lifecycle.

A new capability on the generic host is an escalation, not the default extension
mechanism.
