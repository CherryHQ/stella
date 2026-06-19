# Deliverable model

Durable, async work that survives restarts and is **accepted, not just finished**. Use this for work that outlives a single conversation: long research, multi-step builds, work that may pause for input, and work that needs an acceptance contract met before it counts as done.

A **deliverable** is one recursive entity. A **root** deliverable is the user's objective. A **composite** deliverable decomposes into **child** deliverables (same shape, all the way down); a **leaf** is executed directly by a worker. `kind ∈ {leaf, composite}`.

Completion is **derived, never asserted**. A deliverable converges through a bounded rework loop until its acceptance contract passes — you never mark one done by hand. The worker submits evidence; the acceptance contract decides; if it falls short, the worker is dispatched again with the gaps to repair.

## Who authors deliverables

Deliverables are created and steered by the **user from the Web UI** (Tasks tab), which calls the deliverable HTTP API. There is no agent-facing CLI for authoring, activating, or accepting them. Do not reach for a `stella task` command — it does not exist. Your only role in this system is **worker**: the dispatcher hands you one deliverable to execute or decompose.

Before reaching for a deliverable at all, check you actually need one:

- `delegate` — synchronous focused subtask in a persistent child session, returns inline. Use this first for short research, review, or drafting.
- **deliverable** — async, durable, survives restarts, can block on input, converges through an acceptance contract. This is for work the user tracks to acceptance, authored from the Web UI.
- `scheduler` — a time trigger, not the work itself. For long or reviewable scheduled work, schedule a prompt rather than the work inline.

## Lifecycle

Every deliverable — root or child, leaf or composite — runs through one state machine:

```text
draft ──activate──▶ ready ──claim──▶ active ──submit──▶ (acceptance fold)
  │                   │                │                      │
  │                   │                ├─ block ─▶ blocked ───┤ pass ─▶ accepted (terminal-good)
  │                   │                │             │        └ fail ─▶ active (rework) or
  │                   │                │             └─resolve─▶ ready    rejected_final / abandoned
  └─ cancel ──▶ cancelled
```

- `accepted` — terminal-good; the accepted output is frozen.
- `rejected_final` — a verdict said no with no rework path left.
- `abandoned` — convergence budget exhausted and the policy gave up.
- `blocked` is **recoverable**, not terminal. Block reasons: `budget_exhausted` > `needs_verdict` > `dep`.

Acceptance is a separate projection from lifecycle: `pending | passed | failed`. A deliverable reaches `accepted` only when its contract's acceptance fold passes.

## Composition and dependencies

A composite holds child deliverables produced by a **decomposition** (the only way children come into being — you cannot hand-attach a child). Siblings can declare dependency **edges**: `hard` blocks readiness, `soft` is advisory. Only an upstream's **accepted** output flows downstream. Rollup is automatic:

- all required children accepted → composite's acceptance can pass
- a required child `rejected_final`/`abandoned` → parent fails or blocks
- a required child blocked → parent blocks
- a blocked parent recovers when the blocking child clears

## Worker: the `deliverable_control` contract

If you see a `deliverable_control` tool in your toolset, you are a worker. The deliverable's intent and acceptance criteria arrive as your prompt. Do the work, then call `deliverable_control` **exactly once** with one terminal action:

- `submit` — provide `evidence` (summary + optional artifacts) and `output` when the work meets the acceptance criteria.
- `block` — pause with `kind`/`question` when you need input or an external dependency.
- `fail` — report `reason`/`retryable` when the work cannot be completed.
- `decompose` — **only when dispatched to plan a composite** — return a `decomposition` `{children, edges}`. Each child needs `key`, `title`, `intent`, `kind` (`leaf|composite`), `required`, and `acceptance_contract`; edges declare hard/soft deps by child key. If the deliverable cannot be decomposed, use `fail` instead.

Rules:

- Always end with a terminal action. A final text response without `deliverable_control` is a protocol failure; you get exactly one repair turn, then the attempt fails.
- `submit` does **not** mark the deliverable done — the acceptance contract decides. If your previous attempt fell short, the gaps come back in the next prompt; address them.
- Block only when you truly need a human or external dependency. Do not fake completion to avoid blocking.

## Recovery

Attempts carry a lease and heartbeat. If a worker crashes or Stella restarts, the lease expires and the dispatcher reclaims the deliverable if the convergence budget remains. Submitted evidence and terminal state are durable because they are written to the deliverable's append-only acceptance ledger.
