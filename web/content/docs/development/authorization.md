---
title: Authorization
---

> This section is for developers contributing to Stella.

Every protected operation in Stella starts from a trusted `authz.Authority` — the verified identity of the caller (a user, a delegated agent, a group turn, or a named system worker). Transports derive it from session claims or the runtime context; model-supplied arguments never form identity. What happens next depends on the resource.

There are two enforcement mechanisms, and choosing the wrong one is the most common mistake in this area.

## Two mechanisms

**Policy-backed resources** open an `authz.Authorizer` evaluation and decide a typed `authz.Request` against its fixed built-in rules. Use this when a resource has real distinctions to express: multiple durable scopes, an admin-managed tier, or per-role differences. Agent, Session, Workspace, Goal, Workflow, Scheduler, Skill, and Vault are policy-backed. Their built-in rules live in `internal/authz/policy`; the domain PEP supplies the durable facts.

**Ownership/capability-backed resources** bind the Authority to a domain `Access` object and enforce the boundary with user-scoped durable queries — no policy evaluation at all. Use this when a resource is a single coarse per-user capability with no scope, admin, or role distinctions: authorizing it against a policy engine would be four copy-pasted rules that only ever say "the owner may act on their own." Connections, Email, Share, and Recally are capability-backed.

Do not add a policy resource "for symmetry." If the only rule you would write is "owner may act on their own," you want the capability mechanism. Conversely, if you find yourself hand-rolling scope or admin checks inside a domain, you want the policy mechanism.

## Resource matrix

| Resource            | Mechanism            | Enforced by                                                             |
| ------------------- | -------------------- | ----------------------------------------------------------------------- |
| Agent               | Policy               | `agentaccess.Service` + built-in rules                                  |
| Session / Workspace | Policy               | `agent/session/access.Service`                                          |
| Goal                | Policy               | `goal.Service` (durable-worker authority)                               |
| Workflow            | Policy               | `workflow.Service`                                                      |
| Scheduler           | Policy               | `scheduler.Service` (system/plugin jobs hidden)                         |
| Skill               | Policy               | `skillaccess.Service` (four scopes)                                     |
| Vault               | Policy               | `vault.Service` (user/user_agent/system/system_agent + agent-read gate) |
| Connections         | Ownership/capability | `connections.Service.Access` — OAuth bundles/flows keyed by user        |
| Email               | Ownership/capability | `email.Service.Access` — config in the user's vault namespace           |
| Share               | Ownership/capability | `share.Service.Access` — `WHERE user_id = ?` + os.Root artifacts        |
| Recally             | Ownership/capability | `recally.Service.Access` — uid-scoped store                             |

Public share content is neither: it is a capability URL (see the recipe below).

## Recipes

### Authorize an endpoint (policy-backed)

Derive the Authority from verified session claims, open one evaluation, decide the resource:

```go
authority, err := info.authority()      // from the request's AuthInfo
acc, err := s.vaultSvc.Begin(r.Context(), authority)
// acc's methods decide ResourceVault against one immutable built-in rule set.
```

### Authorize a collection (policy-backed)

A list is one decision; per-row visibility is a second decision in the **same** evaluation. Decide the collection `ActionList`, then filter each row with an `ActionRead` request built from that row's loaded facts. Never trust a caller-supplied `is_owner`; derive it at the PEP from the loaded row and the Authority.

### Authorize a durable worker (policy-backed)

A worker has no live request. Reconstruct the Authority from persisted trusted state — `agentaccess.WorkerAgentAuthority(ownerID, agentID)` — and re-decide on every action. Never persist a decision; persist the facts and re-derive.

### Fold in a cross-domain gate (policy-backed)

When one decision needs another resource's gate (e.g. an agent-scoped vault op must also prove read access to that agent), reuse the open evaluation: `agents.AuthorizeWithin(ctx, eval, authority, agentID, authz.ActionRead)`.

### Authorize a user capability (ownership/capability-backed)

Bind the Authority once; capture the user; scope every query to it:

```go
func (s *Service) Access(authority authz.Authority) (*Access, error) {
	if s == nil {
		return nil, fmt.Errorf("service unavailable")
	}
	if !authority.Valid() {
		return nil, authz.ErrForbidden        // invalid identity
	}
	userID := string(authority.Actor().UserID())
	if userID == "" {
		return nil, authz.ErrUnauthenticated  // valid but no user (e.g. a system agent)
	}
	return &Access{svc: s, userID: userID}, nil
}
```

Reject an invalid or no-user Authority up front, so every method can assume a real acting user. Enforce ownership by scoping the durable query to the captured `userID` — a foreign row is simply not found, never leaked. A delegated agent acts as its user (these capabilities are shared across a user's agents), so capture the user, not the executor agent.

Two extra obligations show up in these domains:

- **Parent-keyed writes.** A table keyed only by a parent id (recally article content, feed entries) cannot be trusted to a caller who "already loaded" the parent. Load the parent uid-scoped inside the write, so a foreign parent is not-found before any mutation.
- **Workspace confinement.** A Share artifact read must stay inside the acting agent's workspace: an agent-scoped actor is confined to its bound agent, and files are read through `os.Root` so a symlink swap cannot escape.

### Serve a public capability URL

The public share view has no session and no Authority. It is authorized solely by an unguessable token: look the share up by token **hash**, honor its expiry, and never accept a raw id. This stays entirely outside any `Access`.

```go
share, err := s.q.GetShareByTokenHash(r.Context(), share.TokenHash(token))
// no Authority; the token hash + expiry are the whole capability.
```

### Call raw Service methods (trusted callers only)

The raw `Service` methods (the ones an `Access` wraps) skip identity entirely and must only be called from documented host-side paths that have no live user request:

- **OAuth callback & token refresh** — keyed by the persisted flow/user, not a request.
- **Recally startup backfill** — a maintenance sweep with no caller.
- **Vault host-side plumbing** — MCP, OAuth, email config, channel config, the sandbox env loader, and key provisioning read and write credentials as the host, not as a user.

If a new call site is not one of these, route it through `Access`/`Begin` instead.
