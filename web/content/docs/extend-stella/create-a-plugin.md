---
title: Create an Extension
description: Add a provider, sandbox backend, channel, or manifest CLI integration.
---

## Pick The Family First

Do not start by adding a capability to the generic host. Choose the existing
family whose lifecycle matches the feature:

1. Use the built-in manifest for a CLI integration.
2. Export a `providers.Definition` for an LLM adapter.
3. Implement `pkg/sandbox` contracts for an isolation backend.
4. Use the managed plugin host for a messaging channel.
5. Keep anything else in its owning internal package until it needs a real
   independent lifecycle.

## Add A Provider

Create a package under `plugins/providers/<name>/` that owns its adapter and
exports `Definition() providers.Definition`. Add that definition to
`setupProviderRegistry()` in `cmd/stellad`. Test adapter behavior and assert the
composition root exposes the new type.

The provider package must not import `internal/**`. Credentials, configured base
URL, models, and enablement remain provider control-plane state.

## Add A Sandbox Backend

Create a package under `plugins/sandbox/<name>/` that implements the public
session and factory contracts. Add a `BackendDefinition` adapter in
`setupSandboxBackends()` so the composition root can supply internal policy and
process-owned dependencies.

Test the backend package independently, then test its composition-root adapter.
Do not make `internal/agent/sandbox` import the concrete backend.

## Add A Channel

Create `plugins/channels/<name>/` and register it through
`RegisterManagedChannelPlugin`. The package owns config decoding, validation,
redaction, channel construction, runtime reconciliation, and focused tests. Add
its blank import to the channel catalog imports.

Channel work also requires checking its configuration, notification, startup,
quiesce, and shutdown behavior. Provider and sandbox changes do not use this
path.

## Verify The Boundary

Update the relevant composition-root test, run the package tests, and keep
`internal/boundary_test.go` green. New behavior, config, or architecture also
requires paired English and Chinese documentation.
