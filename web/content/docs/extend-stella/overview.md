---
title: Extension Overview
description: Choose the narrow extension contract that matches the integration.
---

## There Is No Universal Plugin Runtime

Stella compiles trusted Go integrations into `stellad`. "Plugin" describes who
owns an integration, not one API that every integration must implement.

| Need                                      | Extension path                                             |
| ----------------------------------------- | ---------------------------------------------------------- |
| LLM API adapter                           | `providers.Definition` and the provider registry           |
| Process or filesystem isolation           | public sandbox interfaces and an injected backend registry |
| Messaging ingress and notifications       | managed channel plugin host                                |
| CLI binary, skill, prompt, or session env | built-in manifest                                          |

Native agent tools, hooks, and memory implementations stay in their owning
internal packages. Stella does not load third-party Go binaries or subprocess
plugins at runtime.

## Composition And Dependencies

`cmd/stellad` is the composition root. It explicitly lists provider and sandbox
definitions, supplies Stella-owned dependencies, validates their IDs, and injects
the resulting registries.

Production packages in `plugins/providers/**` and `plugins/sandbox/**` must not
import `internal/**`. Channel packages use the public `pkg/plugins` contract and
the existing compiled catalog. The dependency guard is tested in
`internal/boundary_test.go`.

## Next Reading

- [Create an Extension](/docs/extend-stella/create-a-plugin)
- [Capability Reference](/docs/extend-stella/capabilities)
- [Manifest Tool Integrations](/docs/extend-stella/manifest-tools)
- [Platform API](/docs/extend-stella/platform), for managed channels only
