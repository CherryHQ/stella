---
title: Authorization
---

> This section is for developers contributing to Stella.

Every protected operation in Stella starts from a trusted `authz.Authority` — the verified identity of the caller (a user, a delegated agent, a group turn, or a named system worker). Transports derive it from session claims or the runtime context; model-supplied arguments never form identity. What happens next depends on the resource.

Authorization is **domain-owned**: there is no central policy engine, rule table, or revision. Each domain service binds the trusted Authority to an `Access` object and decides against its own static rules, reading only the immutable Authority plus the durable facts it loads. `internal/authz` provides the shared vocabulary — the `Authority` immutable actor shape and the `Action` verbs — and nothing else.

There are two shapes a domain takes, and choosing the wrong one is the most common mistake in this area.

## Two shapes

**Rule-owning domains** encode their own static decision — a small `allow(action, scope, isOwner …)` predicate over the Authority and the loaded row. Use this when a resource has real distinctions to express: multiple durable scopes, an admin-managed tier, or different trusted actor kinds. Agent, Session, Workspace, Goal, Workflow, Scheduler, Skill, and Vault are rule-owning. The control plane (providers, settings, plugins, channels) is a degenerate case: its single rule is an admin gate at `Begin`.

**Ownership/capability domains** bind the Authority to a domain `Access` object and enforce the boundary with user-scoped durable queries — no per-action rule at all. Use this when a resource is a single coarse per-user capability with no scope, admin tier, or actor-specific behavior: writing rules for it would be four copy-pasted lines that only ever say "the owner may act on their own." Connections, Email, Share, and Recally are capability domains.

Do not add scope/admin rules "for symmetry." If the only rule you would write is "owner may act on their own," you want the capability shape. Conversely, if you find yourself hand-rolling scope or admin checks at a transport, push them into the domain's own rule.

## Resource matrix

| Resource            | Shape                | Enforced by                                                             |
| ------------------- | -------------------- | ----------------------------------------------------------------------- |
| Agent               | Rule-owning          | `agentaccess.Service`                                                   |
| Session / Workspace | Rule-owning          | `agent/session/access.Service`                                          |
| Goal                | Rule-owning          | `goal.Service` (durable-worker authority)                               |
| Workflow            | Rule-owning          | `workflow.Service`                                                      |
| Scheduler           | Rule-owning          | `scheduler.Service` (system/plugin jobs hidden)                         |
| Skill               | Rule-owning          | `skillaccess.Service` (four scopes)                                     |
| Vault               | Rule-owning          | `vault.Service` (user/user_agent/system/system_agent + agent-read gate) |
| Control plane       | Rule-owning (admin)  | `controlplane.Service` (admin gate at `Begin`)                          |
| Connections         | Ownership/capability | `connections.Service.Access` — OAuth bundles/flows keyed by user        |
| Email               | Ownership/capability | `email.Service.Access` — config in the user's vault namespace           |
| Share               | Ownership/capability | `share.Service.Access` — `WHERE user_id = ?` + os.Root artifacts        |
| Recally             | Ownership/capability | `recally.Service.Access` — uid-scoped store                             |

Public share content is neither: it is a capability URL (see the recipe below).

## Recipes

### Authorize an endpoint (rule-owning)

Derive the Authority from verified session claims, bind it, decide the resource:

```go
authority, err := info.authority()      // from the request's AuthInfo
acc, err := s.vaultSvc.Begin(r.Context(), authority)
// acc's methods apply the Vault domain's static scope and ownership rules.
```

### Authorize a collection (rule-owning)

A list is one decision; per-row visibility is a second decision under the same Authority. Decide the collection `ActionList`, then filter each row with an `ActionRead` decision built from that row's loaded facts. Never trust a caller-supplied `is_owner`; derive it in the domain from the loaded row and the Authority.

### Authorize a durable worker (rule-owning)

A worker has no live request. Reconstruct the Authority from persisted trusted state — `agentaccess.WorkerAgentAuthority(ownerID, agentID)` — and re-decide on every action. Never persist a decision; persist the facts and re-derive.

### Fold in a cross-domain gate (rule-owning)

When one decision needs another resource's gate (e.g. an agent-scoped vault op must also prove read access to that agent), call that domain directly with the same Authority: `agents.Authorize(ctx, authority, agentID, authz.ActionRead)`. Domains exchange only the trusted Authority, never a scoped query.

### Authorize a user capability (ownership/capability)

Bind the Authority once; capture the user; scope every query to it:

```go
func (s *Service) Access(authority authz.Authority) (*Access, error) {
	if s == nil {
		return nil, fmt.Errorf("service unavailable")
	}
	if !authority.Valid() {
		return nil, authz.ErrForbidden        // invalid identity
	}
	userID := string(authority.UserID())
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
