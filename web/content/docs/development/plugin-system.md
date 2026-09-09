---
title: Plugin System
---

Agent Plugins combine Skills, CLI dependencies, environment bindings and MCP
servers through one complete package file tree in a typed resource scope.
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

## Filesystem resource model

Agent resources are ordinary files under four typed roots. The roots are
provided by a trusted composition layer and opened through Home capabilities;
resource files never supply their own owner.

| Scope          | Owner              | Typical root                                        |
| -------------- | ------------------ | --------------------------------------------------- |
| `system`       | deployment         | `$STELLA_HOME/.agents/`                             |
| `system_agent` | one Agent          | `$STELLA_HOME/agents/<agent>/.agents/`              |
| `user`         | one user           | `$STELLA_HOME/users/<user>/.agents/`                |
| `user_agent`   | one user and Agent | `$STELLA_HOME/users/<user>/agents/<agent>/.agents/` |

A project may add `.agents/skills/` resources, which participate in Skill
selection only. The resolver chooses complete package candidates from the four
typed roots in the order `user_agent > user > system_agent > system`.
A candidate with the same name replaces the complete package at a broader
scope. It never merges a package's files, CLI declarations, environment
bindings, Skills, or MCP declarations with the losing candidate. Standalone
Skills and MCP files are independent resources and do not become package
members by sharing a name.

`settings.json` is policy, not package content. It carries `disabled`,
administrator-only `forbidden`, and `disabled_tools` entries. A disabled or
invalid winner masks inherited resources. A personal setting cannot override an
administrator prohibition. Native capabilities keep their own registry and
policy; a matching package name cannot grant a Native tool.

The file API and Web UI edit the same bytes that runtime discovery reads. A
complete package copy creates a new independent resource under the destination
owner. It has no parent link, so later source edits do not upgrade the copy.
Package edits are complete-tree replacements. A standalone MCP declaration is
`mcp/<name>.json`; its public endpoint and credential references live in that
file, while secret values live in the credential store.

## Turn capture and lifecycle

At admission, the runtime captures the selected files and uses that fixed view
for the prompt, Skill search and loading, CLI selection, environment binding,
and MCP discovery. An edit made during a turn affects the next turn. Existing
connections and unchanged CLI artifacts can be reused when their captured
identity still matches; the current turn never reopens a later file revision.

A digest on API mutations detects stale UI edits. Direct shell writes remain
ordinary nontransactional file edits. The Home owner lock protects an owner from
concurrent deletion, but it is not a general compare-and-swap for arbitrary
shell writes.

Resource deletion, policy disable, and credential revocation are separate. A
file deletion removes its declaration and future selection. It does not revoke
an OAuth grant. MCP **Disconnect** locally revokes the matching stored grant,
rotates its generation, closes the matching connection, and blocks late
callbacks and refreshes. It does not promise revocation at the remote provider.
Endpoint or authentication identity changes produce a new target and cannot
reuse the old grant. Native account and Agent revocation remains in the Native
or Agent policy that owns it.

The runtime does not perform online garbage collection of package bytes, Skill
history, resource-derived snapshots, or installation caches. MCP connections are
session-owned and close with the session; catalog observations may be retained
for later discovery but are never an authority. Resource cleanup requires an
explicit, verified maintenance action. No TTL or guessed PID substitutes for
evidence that a process and its descendants have stopped.

## CLI and Skill resources

A package may contain binaries, Skills, environment declarations, prompt
sections, and MCP declarations. Package parsing reads the captured file tree;
there is no generated runtime catalog that can override the files. Package
consumers prepare only the entries selected for the current turn and may reuse
matching installed artifacts. Shared installation caches are retained until
process cleanup is safe and are never treated as a second package authority.

Builtin and release Skills are shipped files with trusted ownership. User
frontmatter cannot claim a release owner. Package Skills and standalone Skills
still pass the same frontmatter and size checks, and a package failure remains
masked for that turn rather than reviving a lower-priority candidate.

Native integrations such as channels, email, recally, and scheduler are separate
system capabilities. Their compiled registration, account credentials, and
per-Agent policy remain in their own domains. Disabling a package hides its
file resources; it does not disable a Native integration with a similar name.

## Sandbox process boundary

The local backend enforces its configured filesystem and network policy, but a
leader close does not prove that detached descendants have stopped. The `none`
backend provides no reliable process isolation. A normal turn close therefore
does not prove that resource bytes or derived caches can be removed. Docker owns
cleanup of the session resources it created; package and MCP resource retention
remains independent of that session lifecycle.

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

MCP declarations are files, either standalone under `mcp/<name>.json` or inside a
complete package. The declaration contains endpoint, transport, public headers,
authentication type, credential mode, and opaque credential references. Secret
values never enter the file. OAuth grants, refresh tokens, client secrets, and
connection generations live in the credential store and are addressed by the
canonical file resource identity plus credential owner.

Discovery reads the current turn's captured declaration. Its tool catalog and
status are observations for that target and owner, not an authoritative
registration table. A later turn can rediscover after a missing or stale
catalog. Unchanged sessions may reuse their connection. Skill, description, or other
non-authentication file edits keep the grant; an endpoint, authorization-server,
client, or other authentication identity change creates a new target and cannot
reuse the old grant.

A file delete removes the declaration and future selection. It does not revoke a
grant. Disconnect is explicit: it locally revokes the matching stored grant,
rotates its generation, closes the matching connection, and rejects late
callbacks or refreshes; remote provider revocation is not guaranteed. Shared
system-agent resources use their shared owner tuple; per-user resources use the
caller's verified user tuple. No owner is inferred from a request payload or
fabricated for a migration.

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
starting the new runtime. The migration publishes complete files into the four
typed roots, validates credential relationships and tool policies, and records
source and target digests. Historical rows remain readable for business
evidence, but runtime discovery does not depend on them. Native global
configuration continues to use its existing store. Rolling old and new writers
against that database is not supported during this cutover.
