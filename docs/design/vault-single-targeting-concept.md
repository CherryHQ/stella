# Design: vault secrets — one concept (scope), automatic ambient delivery

Status: **frozen** (V + fable signed off)
Related: #686 / PR #688 (partial 4-scope work this supersedes on the vault side)

## Decision in one line

A user secret is **name + value + scope**. Nothing else. Delivery is automatic:
every user secret is ambient in the sandbox env of the sessions its scope makes
visible, keyed by name. No `inject_always`, no bindings, no declarable path, no
audit chain, no injection UI.

## Why

Dominant real usage: the agent runs **third-party bash CLIs** that read an API key
from an env var (e.g. `expense-cli` reads `$EXPENSE_AGENT_TOKEN`). The agent
cannot know which env var a third-party tool needs — that knowledge lives in the
tool's docs, not in stella. So any model that requires the agent to _declare_ a
secret before use (the old declarable path, #652) fails: the agent declares
nothing, the tool runs without its key, the tool call fails. Observed as an
elevated tool-call failure rate.

The fix is not "make the agent smarter" — it is to remove the need for it to know:
**the binding is the name.** The user names the secret exactly as the env var the
tool expects and scopes it to the agent; the runtime puts it in the bash env; the
third-party tool reads it. The agent knows nothing and nothing fails.

### Security posture (explicit, accepted)

Ambient env means a prompt-injected agent can `echo $TOKEN` and exfiltrate. This
is accepted here: reliability of third-party tool calls outweighs it, and the
secret is the agent's own. The mitigation does **not** belong in the secret model
— it belongs at the sandbox **egress** boundary (deny/allow-list outbound hosts so
a read secret cannot leave). stella's network policy is currently binary
(`disabled` | `allow_all`, `pkg/sandbox/policy.go`); an egress allow-list is a
separate, decoupled follow-up (tracked as its own issue), NOT part of this PR.
Blast radius today is bounded by scope hygiene: a single-purpose agent holding one
key leaks only that key. High-risk agents can still run with `network: disabled`.

## Model

**User-facing: scope only.** The 4-scope grid (owner × range) is unchanged:

| scope          | owner  | reaches (ambient env of…)             |
| -------------- | ------ | ------------------------------------- |
| `user`         | me     | all my agents' sessions               |
| `user_agent`   | me     | my one selected agent's sessions      |
| `system`       | global | all agents' sessions (admin)          |
| `system_agent` | global | one selected agent's sessions (admin) |

**Delivery: fully derived, no user choice.**

- **User secrets** → ambient in scope-matching sandbox sessions, keyed by name.
- **System-managed secrets** (`OAUTH_*` prefix and registered OAuth provider
  vault keys) → **never ambient**; read directly by stella Go code via `Lookup`.
  This split is by name/origin, not a user toggle.
- **Group sessions** already skip vault entirely (`sandbox/env.go`) — unchanged.

**Agent awareness (should-have):** replace the removed declarable prompt section
with an "available secrets" list — the **names** (never values) of the ambient
secrets in this session — so the agent knows `EXPENSE_AGENT_TOKEN` is set and can
confidently run tools that need it. Names are not secret.

## Changes

### Data model — goose migration (read `web/.../rules/goose.md`)

- `DROP` tables `vault_entry_agent_binding`, `vault_entry_project_binding`
  (+ indexes).
- `DROP` table `vault_exec_secret_audit`.
- `ALTER TABLE vault_entry DROP COLUMN inject_always`.
- Down migration recreates them empty; binding/audit rows are unrecoverable
  (document as one-way, like the existing STELLA_TOKEN delete).
- No backfill needed: injection is now unconditional per scope, so every existing
  agent/user-scoped secret starts injecting (this is the intended, more-inclusive
  behavior; it only _adds_ the previously non-injected user-scoped secrets, which
  is correct — they are user secrets meant for their agents).

### sqlc queries (`internal/db/queries/vault_entry.sql`) — read `rules/sqlc.md`

- `ListVaultEntriesForRuntime`: keep only the 4-branch scope-visibility clause +
  the precedence `ORDER BY`. Delete the entire `AND (inject_always OR … bindings)`
  gate and the `project_id` param. ASCII-only comments.
- **Delete** `ListVaultEntriesDeclarableForRuntime`.
- **Delete** binding queries: `ListVaultEntryAgentBindings`,
  `ReplaceVaultEntryAgentBindings`, `ListVaultEntryProjectBindings`,
  `ReplaceVaultEntryProjectBindings`.
- **Delete** audit queries: `CreateVaultExecSecretAudit`,
  `ListVaultExecSecretAuditByUser` (wherever they live).
- `UpsertVaultEntryByScope`: drop `inject_always` column from insert/update.

### Vault service (`internal/vault/service.go`)

- `LoadEnvForAgentProject`: call the simplified runtime query, then **filter out
  system-managed names** before building the env map (reuse the `isDeclarableName`
  predicate — user-managed secrets are ambient-eligible; rename to something like
  `isAmbientSecret`/`isUserManagedSecret` for clarity). Drop the `projectID` arg.
- **Delete** `ListDeclarableForAgentProject`, `ResolveDeclarableEnv`,
  `declarableEntries`, `RecordExecSecretUse`, `ListExecSecretAudit`, the
  `DeclarableSecret` / `ExecSecretAudit` types, and the binding-assembly in
  `metaFromEntry` (`~:662-668`, also removes an N+1).
- `SetScoped`/`SetOptions`: drop `inject_always` and binding params entirely.

### Agent runtime

- **Delete** `internal/agent/exec_secrets.go` and its wiring.
- `internal/agent/sandbox/config.go`: remove `ListDeclarableForAgentProject` /
  `ResolveDeclarableEnv` from the `VaultEnvLoader` interface; trim `projectID`
  from the remaining loader signature.
- `internal/agent/runner_builder.go` (`~:155-161`): replace the declarable prompt
  section with the names-only "available secrets" section (or drop if simpler and
  add awareness later — keep the PR focused).
- Bash tool: remove the `secrets` parameter and its declarable resolution.

### API — spec-first (`api/CLAUDE.md`, schemas in `api/spec/domain/profile/`)

- `SetScopedVaultEntryRequest`: remove `inject_always`, `inject_agent_ids`,
  `inject_project_ids`.
- Vault entry response schema (`profile/schemas.yaml:~40-41`): remove
  `inject_agent_ids` / `inject_project_ids` (and `inject_always` if present).
- `x-agent-tool.fixed` block (`profile/paths.yaml:~411-423`): remove the injection
  fields.
- Remove the exec-secret-audit endpoint(s) if any are exposed.
- `mise run generate` → sqlc + OpenAPI Go + TS SDK.

### Web (`web/src/features/credentials/CredentialsPage.tsx`)

- **Delete** the entire injection section (the `inject_always` switch, agent
  multi-select, project multi-select) and all `injectAlways` / `injectAgentIDs` /
  `injectProjectIDs` state and save-body fields.
- The scope ladder (owner × range) is the only control left besides name + value.
- **Delete** `injectionBadge` and its column (or replace with nothing). Scope
  color/label already communicates reach.
- Fix scope descriptions: a secret at scope X is available in X's sessions, full
  stop — no more "可用 vs 注入" split.
- i18n: delete now-unused injection keys; update scope descriptions in **en + zh**.

### Docs

- Rewrite `web/content/docs/guides/secrets-and-keys.md` **and** `.zh.md`: one
  concept (scope), ambient-by-name delivery, name = env var name, system secrets
  internal-only. Remove binding/declarable/inject_always docs.

## Non-goals (out of this PR)

- **Egress allow-list** (the actual safety lever) — separate tracked issue.
- Secret-value redaction in model-visible output — separate hardening.
- Scope grid / precedence changes.
- Per-project secrets (dropped with bindings; a future scope-grid extension).

## Verification

- `mise run format && mise run build && mise run test`. Vault tests:
  - each scope's secret lands in the matching runtime env (user_agent only for its
    agent; user for all the user's agents; system/system_agent for admin scopes);
  - system-managed names (`OAUTH_*`) never appear in the ambient env map;
  - no reference anywhere to the dropped tables/columns/queries.
- `web/`: `vp check --fix && vp build`; manual add/edit dialog per scope — only
  scope + name + value remain.
- Grep gate — zero matches: `inject_always`, `inject_agent_ids`,
  `inject_project_ids`, `vault_entry_agent_binding`, `vault_entry_project_binding`,
  `vault_exec_secret_audit`, `Declarable`, `exec_secrets`.
- Cross-platform build spot-check (`GOOS=windows` per repo rule) if touching
  sandbox/env code.

## Rollout

One PR, linked to the new follow-up issue. Breaking for anyone relying on
binding-based partial injection or the declarable/audit path; call out in the PR
body.
