---
title: Plugin System
---

Agent Plugins combine Skills, CLI dependencies, environment bindings and MCP
servers through one scoped package definition.
Compiled Stella Native Plugins use trusted Go registration and deployment-wide
configuration, with an administrator's per-Agent deny policy.

## Native administration

Administrators manage Native switches under **Admin console > Integrations >
Native capabilities**. The global switch applies to every Agent. A per-Agent deny
blocks only the selected Agent; removing it restores access only while the
global switch is enabled. User settings cannot override these limits.

`NativePolicy` reads the global `plugin` row and `native_agent_deny` together.
A missing global row inherits its default from the trusted Native registry.
An unknown registration or failed policy read refuses new admission. Native
hooks, channel listeners and background work use this policy; channel instances
retain their own enabled state and credentials.

Native tools keep their static exported names in `tool_override.tool_name`.
The existing four-scope tool restrictions apply at discovery and on every
invocation, including calls from an already-built runner. Native identities
do not require an Agent Plugin definition foreign key. The importer uses the
same explicit Native registry as runtime admission and leaves Native global
configuration in its existing store.

Native mutations use the runner admission fence. An acknowledged commit, or a
write whose commit outcome is unknown, retires old runners and reconciles
channel listeners. A failed response therefore does not preserve stale access.
Native management methods require a trusted administrator authority, including
calls made outside HTTP. Per-Agent restrictions also pass through the Agent
authorization service. OAuth access tokens cannot reach the management API.
The Host reads configuration through `ConfigStore.Get`; it has no configuration
write or enable-switch shortcut around the management service.

## Native Go contract

Native runtimes implement `Apply`, `Stop` and `Snapshot`. Runtime lookup uses
`Get`; channel reconciliation accepts the committed channel ID through
`ReconcileChannel`. Registrations use `ManagedChannelPluginRegistration.Info`.
There are no `Start`, `Reconcile`, `Status`, `Lookup`, `ApplyChannel` or `Meta`
compatibility aliases on these interfaces. Native plugins compiled outside this
repository must update those calls before rebuilding.

`PluginInfo.Capabilities` declares registration traits; the host validates them
against the actual registrations. `RequiredCapabilities` separately declares
host ports and remains subject to fail-closed authorization.

## Definition and configuration

`PluginDefinition.ID` is the unique canonical package name. It describes the
shipped resources and default enabled state. Builtin definitions come
from trusted release declarations; persisted builtin rows are projections.
One Agent definition can contain several resource kinds together. Transport and
installation choices belong to the respective resource consumers; the definition
has no root backend discriminator. Compiled Go implementations are registered
and managed through the separate Native path.

Management routes use `/api/plugins/{plugin_id}` and its sub-resources. Pass the
exact ID returned by the catalog as one URL-encoded path segment.
Source-directory categories do not participate in request addressing.
Names use 1–64 lowercase letters, digits, periods and hyphens, start and end
with a letter or digit, and cannot contain `..` or `--`. A name conflict fails;
Stella does not add a suffix. The identity is fixed after creation; the display
name can change.

`PluginConfig` records one decision for a definition at one scope:

| Scope        | Applies to                    |
| ------------ | ----------------------------- |
| System       | Everyone in the deployment    |
| System agent | One Agent, across its users   |
| User         | One user, across their Agents |
| User agent   | One user using one Agent      |

There is at most one configuration for each definition and scope tuple. The
selected configuration is user agent, user, system agent, then system. An
explicit system or matching system agent `false` is an independent upper bound.
A narrower `true` cannot override either restriction. `null` follows the shipped
default of the selected definition.

This is the Agent Plugin configuration model: one `PluginDefinition` plus at most
one `PluginConfig` for each of the four scope tuples. `user_id` and `agent_id`
come from the trusted authority and are never accepted as caller-owned identity.
The definition owns the stable package identity, declared resources, source,
version and content digest. The selected config owns formal parameters for
already-declared resources and credential references for that scope. A config
cannot add a binary, Skill, OAuth requirement, source, or package member. Its
stored parameter object names declared binaries and MCP servers; the resolver
applies those parameters to a copy of the definition.

The selected scope owns its configuration. It can override allowed parameters
in the shipped definition, but fields and credentials are never merged across
scopes. A disabled or incomplete winner does not fall back to a broader
configuration. Builtin plugins follow the same rules and administrators can
disable them.

### HTTP boundary and formal parameters

The service accepts the formal parameter shape after the request boundary.
The HTTP adapter still translates older flat MCP fields and binary arrays into
that shape before calling `Plugin.Access`. Persisted definitions, configs, and
runtime code do not decode the old shape.

The adapter is a compatibility boundary, not a second configuration model. Its
exit condition is the declared compatibility window closing after supported
clients use the formal shape. This document does not assign that condition to
a release number.

## One execution snapshot

The common service resolves a snapshot from trusted user, Agent or group
identity. The Agent runtime derives resource visibility, binaries, environment
bindings and declarative prompt sections together from that snapshot. The
context constructor accepts only the snapshot, so callers cannot pair it with
resources from another identity or revision. Native Host handles Go-registered
capabilities and native prompt contributions through its separate policy.

The plugin detail view, `PluginConfig.resource_summary`, and the effective
configuration endpoint describe declared or configured resources. Package
update preview is also read-only: it validates the candidate and reports its
digest, resource names, OAuth changes and incompatible scopes. None of these
views installs a CLI, connects an MCP server, obtains a token, or proves that
the next turn is executable.

At turn admission, the runtime rechecks authorization and prepares each
selected package. It records package-scoped OAuth and CLI readiness, then
publishes only successful package resources to the prompt, tools, sandbox and
environment. A failed package remains a masked selection candidate so a
same-name lower-priority resource cannot be revived by a preparation error.

The host attaches a secret-free execution summary to the durable user-message
anchor for that admitted turn. Each package entry records the immutable package
version and digest, config ID, scope and revision, authorization, and readiness.
Skill entries record the actual winner and its `selected`, `masked`, or
`overridden` state, with its version, source, scope and digest. Binary entries
record the requested and resolved versions plus the backend, selection identity
and source that provide the installation evidence. A pre-existing ready cache
without evidence leaves the resolved version or installation evidence unknown.
The summary is absent from turns recorded before this metadata existed; Stella
does not reconstruct historical admission from current configuration.

Each Agent Plugin resolves by its exact package ID. Different packages do not
replace each other's resources. Native tools and hooks are absent from that
snapshot; a matching Agent package name cannot grant Native admission, which
uses the trusted registration ID and independent policy.

MCP exported names adapt the package name, server key and remote tool name into
an ASCII name of at most 64 characters, with a deterministic 12-hex-character
hash suffix. The server key is the authored MCP entry key; imported single-server
registrations use `main`. The actual exposed set is checked for collisions.
Authorization uses the package, server key and raw remote tool name, never a
parsed display name. Native tools keep their registered static names.

Configuration writes run under the admission boundary and commit atomically.
Idle runners are retired; an admitted turn can finish before its runner is
retired. Credential access must still match the captured configuration revision:
an old runner must never send newly rotated credentials to an old endpoint.
Plugin switches govern Stella-managed exposure and admission. Filesystem and
network restrictions remain sandbox responsibilities; disabling a plugin does
not revoke an existing OAuth grant or erase a previously loaded Skill.

## CLI and Skill resources

A CLI integration can contain binaries, Skills, environment declarations and
prompt guidance. A CLI version pin and a Skill source are independent fields;
changing one need not change the other. `agentpackage` reads standard package
files at build time; the generator embeds a normalized definition catalog as
JSON. Startup reads that catalog directly, without an intermediate manifest or
YAML conversion. `internal/plugin` owns the authored resource declaration and
the formal, scoped parameter validation. Resource consumers own installation,
connection and execution. OAuth provider documents are loaded and validated by
`internal/connections/oauth`.

Builtin Skills require an explicit source path and package owner. Generation and
runtime loading use that release declaration; the old directory scanner and
implicit owner layout are removed. Startup migration checks for extracted
legacy Skills live in `cmd/stellad`, where they can block an unsafe upgrade
without making legacy layouts part of the resource loader.

Only mise and Xberg are embedded release runtimes prepared synchronously. Other CLIs, including fd and rg, are preinstalled
in the background through the same installer used for session selection.
`internal/platform/toolinstall` owns mise execution, private configuration and
atomic artifact publication. It receives concrete tools and paths; Agent
sandbox code owns scope, revision and selection identity.
Prewarming fills the mise artifact cache using temporary private configuration;
it neither publishes a session selection nor maintains a second installation
state file. A runner prepares its selected snapshot from matching cached
artifacts, installing missing versions within its sandbox boundary.

Builtin Skill ownership is generated from the release package declarations.
User frontmatter cannot claim an owner. Prompt listing, search and direct loading
apply the same owner restriction after selecting the resource winner.

The email, recally and scheduler guides are standard Agent packages under
`plugins/agent/<name>/`, with `plugin.json` and `skills/<name>/SKILL.md` as their
authored sources. Their Agent Plugin identities are the bare package names.
Disabling a guide hides its Skill without changing the corresponding Native
capability. Loading a guide does not enable Native tools; its compatibility text
states that the Native capability must be available separately.

All shipped Agent packages use `plugins/agent/<name>/plugin.json`. Web owns its
Skill and declares both Bun and Lightpanda; disabling the separate Bun package
does not disable Web. Python Script belongs to uv. Disabling a package suppresses
its controlled resources but does not delete shared cached binaries. Lightpanda
provides rendering within Web, while Bun runs its fetch and search scripts.

Each runner receives only the selected CLI artifacts and their entry points.
Trusted system installations keep private options out of the runner filesystem;
Docker prepares Linux artifacts in its existing tool cache, keyed by one resolved
image ID and the complete four-scope selection. A selection helper supplies only
the chosen entries and binaries. Native managed installs use the managed tree;
user and user agent installations stay in their own sandbox trees and take
precedence in PATH. Plugin permissions govern the resources Stella supplies; the
`none` backend provides no filesystem isolation.

## Managed Skill revision retention

Managed Skills use four typed Stella Home scopes. `system` lives under
`.agents/db-skills`, `system_agent` under `agents/<agent>/.agents/skills`, `user`
under `users/<user>/.agents/skills`, and `user_agent` under
`users/<user>/agents/<agent>/.agents/agent-skills`. Each managed Skill has
immutable files under `.stella-revisions/<skill-id>/<digest>`; the selector named
`<skill-id>` chooses the current revision. Project Skills and release-provided
builtin Skills use their own storage authorities and are outside this collector.

Revision cleanup runs after a successful managed mutation, release of the last
active turn owner, relevant Reflect usage changes, and successful startup
reconciliation. While holding the managed advisory lock, the collector snapshots
database evidence and active turn views, then walks only existing trusted roots.
It protects the selector's current target, every exact revision held by an active
turn, and non-null `skill_usage.content_digest` and `skill_changelog.content_digest`
evidence. A revision is eligible only when its manifest is valid and no such
evidence reaches it.

The collector first renames proven-unreachable revision directories to a
`.stella-gc-*` quarantine while the root and managed locks are held. It removes
quarantine entries after releasing those locks, and retries entries left by an
interrupted run. It never removes a current selector target. Pending or degraded
startup reconciliation, malformed ownership or selector evidence, database or
Home errors, and unknown runtime termination all preserve the affected bytes for
retry; an uncertain delete outcome does not authorize physical cleanup.

## Package retirement and runtime revocation

Retiring a custom package records `retired_at`; resolution stops admitting it,
but the definition remains as a retryable cleanup record. Package cleanup keeps
the digest of every current non-retired definition and every process owner from
building, active, or closing `PluginContext` views and admitted package Skill
turns. Only after no runtime owner reaches a retired digest does the content store
quarantine and remove that content-addressed tree. A shared digest remains while
any current definition or runtime owner uses it. The finalizer then locks the
retired definition by its revision and CAS-deletes its configs, tool policies,
and row. A failed or uncertain file removal or CAS leaves the retired row,
configuration and package name reserved for a later retry.

Account deactivation or deletion, assignment removal, OAuth disconnect, and Vault
entry deletion are terminal revocations over the target scope, including user,
user-agent, shared-agent, and deployment-wide operations. The durable mutation
runs inside the lifecycle fence; after a successful or uncertain outcome,
matching active and reserved turns are cancelled or detached, and slow runner
`Close` work runs after the fence. Ordinary plugin and configuration changes only
mark busy runners stale, so an admitted turn finishes with its captured snapshot
and the next turn builds the new one.

## Sandbox recovery evidence

Before starting a process, the local and `none` backends persist a
`cache/sandbox-recovery/<session>.json` marker. Descendants can outlive the leader,
so a normal `Close` does not clear this marker and the runner scratch bytes stay
available for recovery. Any marker globally blocks package and managed-Skill
resource cleanup; this is conservative evidence, not an automatic final cleanup
promise. The marker is never removed automatically, can block cleanup
indefinitely even after a normal `Close`, and there is currently no product
command or safe automated recovery path that clears it. There is no TTL or PID
guess that clears it.

The Docker backend snapshots only the scoped container IDs that existed before
the current runtime starts. Cleanup is allowed only after each initial ID is
proven terminal and removed; containers created after the snapshot do not block
the current runtime. A list, inspect, or remove failure, or a non-terminal state,
keeps cleanup pending for a later event and retry. Docker does not use TTL or PID
heuristics to infer ownership.

## Channels and accounts

A channel plugin describes one platform. Each account remains a separate
channel instance, with its own exact ID, credentials, active state and persistent
Agent binding. Saving one account cannot replace another account's credentials
or re-enable an administrator-disabled platform.

Listeners use the Native global switch and per-Agent deny plus instance active
state. A user's tool restriction does not stop a listener shared with other
users. Event admission also checks existing Agent access or guest policy.
Channel signatures, enrollment and platform identity checks remain in their
owning adapters.

The existing uniqueness rule, `UNIQUE(agent_id, type)`, permits at most one
instance of a platform per Agent. Each instance has its own credentials, so
multiple accounts remain separate even when they use the same platform. Multiple
accounts can be bound to different Agents. Identity linking is separate from
provisioning a bot account.

## MCP credentials and observations

Each MCP server is a resource within an Agent Plugin. The selected parent config
stores endpoint settings in `mcp_servers`, keyed by the authored server name;
credential references use the same keys. Each child has a stable UUID under that
parent, unique by `(config_id, server_key)`. The child relation stores identity,
not a second endpoint or auth configuration. Tokens remain in the Vault. Shared
and per-user credentials never fall back to each other.

The child UUID identifies credentials, OAuth flows and connection observations.
The parent owns scope, actor authorization and the revision fence. A child write
checks membership and consumes the parent revision once. Changing an endpoint
or auth identity retires only the affected child state; moving or deleting a
parent handles all children atomically. A failed child does not hide working
sibling servers or package Skills.

OAuth client registration belongs to each child and is managed by its parent
configuration owner. For system and
system agent configurations, an administrator initializes a missing client
through the OAuth start action before users authorize their own accounts. User
and user agent configuration owners can initialize their own client. Existing
system configurations without a client ID need this administrator step after
upgrade; per-user tokens remain isolated. Disable and reset preserve grants.
Deleting a configuration removes its grants atomically.

Remote tool catalogs and connection status are observations keyed by
child UUID and credential owner, with a parent configuration revision fence. A
per-user catalog cannot become another user's tool list. Legacy per-user catalogs
have no trusted owner provenance and are cold-probed after migration. Internal
OAuth bundles are excluded from public Vault access and ambient environment
injection.

## Core boundaries and upgrade

Provider adapters and sandbox backends retain their explicit compiled
registries. Core storage, orchestration and credential services do not become
optional simply because a plugin uses them. For example, disabling the public
Xberg plugin hides its public resources; the Library's explicit internal parser
dependency remains available.

`cmd/stellad` composes backend adapters and the common catalog. Backend packages
must not introduce their own scope or enabled resolver. Production provider and
sandbox adapters depend on public `pkg/**` contracts, not `internal/**`.

The legacy cutover is a maintenance upgrade: stop every old writer before
starting the new runtime. One transaction imports and validates configurations,
credential relationships and tool policies before recording completion. Legacy
Agent Plugin rows remain for inspection after import. Native global configuration
continues to use its existing store. Rolling old and new writers against that
database is not supported during this cutover.
