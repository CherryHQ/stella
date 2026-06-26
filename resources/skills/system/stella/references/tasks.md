# Goal model

Durable, async work that survives restarts and is **accepted, not just finished**. Use this for work that outlives a single conversation: long research, multi-step builds, work that may pause for input, and work that needs an acceptance contract met before it counts as done.

A **goal** is one recursive entity. A **root** goal is the user's objective and is always **planned first** — it is a **composite** that decomposes into **child** goals (same shape, all the way down). A **leaf** child is executed directly by a worker; a **composite** child is planned and decomposed in turn. `kind ∈ {leaf, composite}`. There is no top-level leaf: every goal goes through plan → decompose → run before it can be accepted.

Completion is **derived, never asserted**. A goal converges through a bounded rework loop until its acceptance contract passes — you never mark one done by hand. The worker submits evidence; the acceptance contract decides; if it falls short, the worker is dispatched again with the gaps to repair.

## Who authors goals

Two surfaces author goals, both over the same goal HTTP API:

- **You, the agent** — via the `stella goal` CLI (alias `stella task`). `stella goal create --title ... --intent ...` creates a goal and runs it autonomously: the server **plans first** (decomposes it into verifiable sub-tasks), then the dispatcher runs each child and converges to acceptance, with no further prompting. You do not choose leaf vs composite or call plan/approve/activate — just write a clear, self-contained intent. This is how you give yourself long-running work that outlives the current conversation; check back with `stella goal list`/`get`. For goals that need a human approval gate (`review_policy=human`), the dispatcher still plans automatically but parks the composite at `blocked(needs_plan_approval)`; inspect the pending plan with `stella goal get <id>` and then `stella goal approve <id>` (materialize) or `stella goal reject <id>` (re-decompose). See `stella goal --help`.
- **The user** — from the Web UI (Tasks tab); the same goal HTTP API the CLI uses.

Authoring and working are separate roles: once a goal is active you may also be handed it as a **worker** (see the `goal_control` contract below).

Before reaching for a goal at all, check you actually need one:

- `delegate` — synchronous focused subtask in a persistent child session, returns inline. Use this first for short research, review, or drafting.
- **goal** — async, durable, survives restarts, can block on input, converges through an acceptance contract. This is for work tracked to acceptance: create it yourself with `stella goal create`, or the user authors it from the Web UI.
- `scheduler` — a time trigger, not the work itself. For long or reviewable scheduled work, schedule a prompt rather than the work inline.

## Lifecycle

Every goal — root or child, leaf or composite — runs through one state machine:

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

Acceptance is a separate projection from lifecycle: `pending | passed | failed`. A goal reaches `accepted` only when its contract's acceptance fold passes.

## Composition and dependencies

A composite holds child goals produced by a **decomposition** (the only way children come into being — you cannot hand-attach a child). Siblings can declare dependency **edges**: `hard` blocks readiness, `soft` is advisory. Only an upstream's **accepted** output flows downstream. Rollup is automatic:

- all required children accepted → composite's acceptance can pass
- a required child `rejected_final`/`abandoned` → parent fails or blocks
- a required child blocked → parent blocks
- a blocked parent recovers when the blocking child clears

Decomposition is automatic: `stella goal create` produces a composite whose planning runs on its own, materializing children and activating them so the dispatcher runs them — no manual steps. When a goal needs a human approval gate (`review_policy=human`), the dispatcher still plans automatically but parks the composite at `blocked(needs_plan_approval)` with the proposed `{children, edges}` stored on the goal; `stella goal get <id>` shows the pending plan, `stella goal approve <id>` materializes it (children become ready), and `stella goal reject <id>` returns it to draft for re-decomposition. See each subcommand's `--help` for the JSON shape and flags.

## Workflows (frozen goals)

A **workflow** is a frozen goal: the decomposition plan of an **accepted composite**, snapshotted so a later run skips the planner and deterministically rebuilds the same subtree. Use it for a recurring objective whose plan you already trust — re-running it costs no planning.

- `stella workflow save-as <goal-id>` freezes an accepted composite you own into a workflow (records its version + plan hash). The plan is captured **verbatim**, including sibling edges.
- `stella workflow instantiate <workflow-id>` (alias `run`) rebuilds the frozen plan into a fresh goal tree and activates it; it returns the new root goal — track it with `stella goal get <id>`. A composite node with no frozen subplan is left to the planner ("semi-frozen"); everything else is deterministic.
- `stella workflow schedule <workflow-id> --cron/--every/--at` creates a scheduler job that instantiates a fresh run on each fire (routed to the workflow's managing agent).
- `stella workflow list/get/update/delete` manage your workflows. `stella workflow create` authors one from a hand-written frozen plan (rare; `save-as` is the usual path).

See each subcommand's `--help` for flags and JSON shapes.

## Worker: the `goal_control` contract

If you see a `goal_control` tool in your toolset, you are a worker. The goal's intent and acceptance criteria arrive as your prompt. Do the work, then call `goal_control` **exactly once** with one terminal action:

- `submit` — provide `evidence` (summary + optional artifacts) and `output` when the work meets the acceptance criteria.
- `block` — pause with `kind`/`question` when you need input or an external dependency.
- `fail` — report `reason`/`retryable` when the work cannot be completed.
- `decompose` — **when dispatched to plan a goal** — return a `decomposition` `{children, edges}`. Each child needs `key`, `title`, `intent`, `kind` (`leaf|composite`), `required`, and `acceptance_contract`; edges declare hard/soft deps by child key. If the goal cannot be decomposed, use `fail` instead.

Rules:

- Always end with a terminal action. A final text response without `goal_control` is a protocol failure; you get exactly one repair turn, then the attempt fails.
- `submit` does **not** mark the goal done — the acceptance contract decides. If your previous attempt fell short, the gaps come back in the next prompt; address them.
- Block only when you truly need a human or external dependency. Do not fake completion to avoid blocking.

## Recovery

Attempts carry a lease and heartbeat. If a worker crashes or Stella restarts, the lease expires and the dispatcher reclaims the goal if the convergence budget remains. Submitted evidence and terminal state are durable because they are written to the goal's append-only acceptance ledger.
