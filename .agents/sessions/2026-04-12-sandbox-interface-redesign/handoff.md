# Handoff

<!-- Append a new phase section after each phase completes. -->

## Phase 1: Finalize architecture, policy model, and enforcement scope

**Status:** complete ✅

**Tasks completed:**

- [x] 1.1: Wrote comprehensive design spec for `sandbox.Policy`, `sandbox.Session`, and `sandbox.Host`
- [x] 1.2: Inventoried built-in tool (bash/read/write/edit) filesystem/process/network access paths
- [x] 1.3: Inventoried plugin tool (webfetch, skills, agent, notify, mcp) access paths
- [x] 1.4: Inventoried MCP-related local execution and transport paths (stdio, SSE, HTTP)
- [x] 1.5: Classified all paths as mediated (4), to-be-mediated (18+), or explicit exception (8+)
- [x] 1.6: Defined unsupported backend/policy behavior with fail-closed semantics
- [x] 1.7: Defined relaxed-mode creation rules (explicit opt-in only, never implicit fallback)
- [x] 1.8: Defined required transport classes for plugin/MCP: stdio, SSE, StreamableHTTP, HTTP
- [x] 1.9: Defined compatibility/deprecation plan for current execution APIs
- [x] 1.10: Defined observability requirements with events, logs, metrics, and alerts
- [x] 1.11: Produced policy compatibility matrix (boxsh vs local backend)
- [x] 1.12: Produced exceptions register with 8 documented exceptions (EX-001 through EX-008)
- [x] 1.13: Produced rollout/deprecation notes with 6-phase migration plan

**Files changed:**

- `.agents/sessions/2026-04-12-sandbox-interface-redesign/plan.md` — approved implementation/design plan (no changes)
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/tasks.md` — updated Phase 1 tasks to completed
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/handoff.md` — appended Phase 1 completion section
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/design-spec.md` — **NEW**: Detailed design specification
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/execution-paths-inventory.md` — **NEW**: Complete inventory of all execution paths with classification
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/exceptions-register.md` — **NEW**: 8 documented exceptions with owners and closure plans
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/policy-compatibility-matrix.md` — **NEW**: Backend/policy compatibility matrix
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/rollout-deprecation-notes.md` — **NEW**: Rollout phases and deprecation timeline
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/observability-requirements.md` — **NEW**: Observability events, metrics, and alerts

**Commits:**

- `📋 docs: Phase 1 sandbox interface redesign design artifacts`
  - Add design-spec.md with Policy, Session, Host interfaces
  - Add execution-paths-inventory.md classifying all tool paths
  - Add exceptions-register.md with 8 documented exceptions
  - Add policy-compatibility-matrix.md (boxsh vs local)
  - Add rollout-deprecation-notes.md with 6-phase plan
  - Add observability-requirements.md with events/metrics
  - Update tasks.md marking Phase 1 complete

**Decisions & context for Phase 2:**

- `internal/sandbox` is the owning abstraction package
- Top-level concepts are `sandbox.Policy`, `sandbox.Session`, and `sandbox.Host`
- Sandbox is the execution boundary for all local tool execution paths by default
- Build-time contexts remain sandbox-agnostic; execution-time contexts receive `sandbox.Host`
- Unsupported backend/policy combinations fail closed unless explicit relaxed mode is chosen
- Remote MCP remains a separate trust boundary; local side remains under sandbox control
- **Transport validation complete**: Required classes are stdio, SSE, StreamableHTTP, HTTP (WebSocket deferred)
- **Exceptions register established**: 8 exceptions documented with owners and closure plans (EX-001 through EX-008)
- **Rollout plan approved**: 6 phases from types introduction to cleanup
- **Core finding**: Dual implementation path exists (boxsh adapters + native tools) - Phase 4 will unify

### Fixes
- Reconciled the canonical `Policy` contract across the design artifacts: added `Backend` and `Relaxed` to the spec and standardized `Factory.Supported(policy) error`.
- Added the missing first-cut `Host` request/result type appendix and clarified exec/HTTP semantics for Phase 2.
- Aligned execution-path inventory classifications with the exceptions register and updated summary counts.

**Phase 2 Ready:**
- [x] Design artifacts reviewed and complete
- [x] All paths inventoried and classified
- [x] Exceptions documented
- [x] Rollout plan established
- [ ] Ready to implement types in `internal/sandbox/`
