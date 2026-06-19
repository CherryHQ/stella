---
title: Deliverable model (SDLC target)
description: The proposed target architecture that turns goal/plan/task from a forward-only pipeline into an iterative SDLC — one recursive Deliverable abstraction with derived completion, a layered acceptance contract, and a bounded convergence loop.
---

> **Status: proposed direction, not shipped.** This page is the north star for evolving the
> execution core. The current behavior is documented in [Goal system](./goal-system) and
> [Task system](./task-system); this page explains where that model is wrong for a real SDLC
> and the clean target both an internal design pass and an independent adversarial review
> converged on. Build toward it; do not assume any of it exists yet.

## The problem: a pipeline pretending to be an SDLC

Today a structured goal materializes into a forward-only dependency DAG of `agent_task` rows
whose items may be labeled `design` / `impl` / `verify` — though ordering comes from the
dependency edges, not the role label, and validation only requires each `impl` to have a
downstream `verify` (a direct goal is a single `direct` item). It runs the DAG once. This is a
**pipeline**, not a software development lifecycle, because the lifecycle's defining property —
_iterate until correct_ — is missing. Four things sit in the wrong place:

1. **`done` is a state the agent sets.** On the common `none`/`auto` paths a `task_control
submit` call drives a task straight to `done` (`internal/tasks/review.go`); an `agent` or
   `human` review policy delays it through a `reviewing` step, but that still reviews the
   submitted output rather than deriving completion from the work's own acceptance contract.
   Completion is an assertion by the worker (or a reviewer's yes/no), not something the system
   derives from the work's intent.
2. **Acceptance criteria are detached, advisory metadata.** `PlanItem.Criteria` is written to
   rows the completion path never reads (`internal/tasks/plan_service.go`). Nothing checks
   them; nothing injects them into a prompt.
3. **`verify` is a sibling node in the DAG.** A verify task can pass while the `impl` it
   "verifies" is broken — they are independent nodes wired only by a forward dependency edge.
4. **Iteration is an exception path.** Ordinary automatic execution never feeds verify gaps
   back into `impl`; a failed required child rolls the goal to `failed`, and recovery requires
   explicit lifecycle intervention such as `ReopenTask`. Rework — the heart of an SDLC — is
   handled as failure recovery instead of as the normal way work converges.

The consequence is three parallel state machines (goal `draft/planning/planned/running/…`,
plan `draft/in_review/accepted/approved`, task `draft/ready/running/…`) with overlapping but
inconsistent statuses. A caller must understand all three to reason about progress. That is a
shallow design.

## The core abstraction: one recursive Deliverable

The whole model collapses into a single deep module applied recursively.

> Stella schedules **deliverables**. Agents produce **evidence** through **attempts**. The
> system **evaluates** evidence against the deliverable's **acceptance contract**, iterates
> until accepted / blocked / abandoned, and exposes only **accepted output** to dependent
> deliverables.

A **Deliverable** owns its intent, its decomposition, its acceptance, its attempts, and its
convergence. A goal is a root deliverable; its children are deliverables; theirs are too —
the same shape all the way down. `plan` is not a peer object, it is a deliverable's
_decomposition version_. `task`/`run` is not a peer object, it is a deliverable's _attempt_.
`review` is not a peer object, it is an _acceptance evidence source_.

### Runtime entities

Four runtime entities plus one optional revision table — deliberately small; more than this
is over-modeling.

| Entity                              | Job                                                                                                                                                                        |
| ----------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `deliverable`                       | Intent + `acceptance_contract` + `convergence_policy` + nullable `parent_id` + nullable accepted output.                                                                   |
| `deliverable_edge`                  | Accepted-output dependency between sibling deliverables (`hard` blocks; `soft` is advisory context).                                                                       |
| `attempt`                           | One execution episode: a persistent agent session, the frozen input context, and the submitted evidence (evidence folds into the attempt — it is not its own table).       |
| `acceptance_event`                  | Append-only record of a deterministic check result or a judgment verdict. Deliverable acceptance state is a **cached projection** over these events, not a mutable column. |
| `deliverable_revision` _(optional)_ | A decomposition version (the former `plan`). Not a day-one runtime entity; needed only when multi-level decomposition is turned on.                                        |

`acceptance_event` being append-only (not a mutable evaluation table) is friendlier to audit
and to projection rebuilds — see [Scalability](#simplicity-and-scalability).

## Completion is derived, never asserted

An agent never sets `done`. It submits **evidence**; the system **derives** acceptance.

There are two kinds of acceptance, and the boundary between _derived_ and _asserted_ is exact:

- **Deterministic acceptance** — a command exits 0, an artifact exists, a schema validates,
  tests pass. The system runs the check and derives the result. Here completion is genuinely
  _defined out of existence_: the question "is it really done?" cannot be answered wrongly,
  because unmet acceptance simply never reaches `accepted`.
- **Judgment acceptance** — an agent or a human decides. This verdict is an _irreducible
  subjective assertion_; pretending it is objective would be dishonest. But the assertion is
  still **evidence**, not a state mutation:

  ```text
  WRONG:   human clicks "mark done"
  RIGHT:   human submits verdict: approved(rationale, scope, authority, timestamp)
           system derives acceptance_state = accepted → lifecycle = accepted
  ```

So the line is: **asserted** = the verdict, its rationale, scope, authority, and timestamp;
**derived** = acceptance state, completion, and downstream readiness. The codebase already has
the right shape for this — the executor returns a `Result` and the service owns the durable
transition (`internal/tasks/executor.go`, `internal/tasks/worker.go`). The only leak is that
`Submit` treats _submit_ as _done_.

### The acceptance contract

Acceptance lives inside the deliverable, inseparable from intent. It is a composite policy
tree:

```json
{
  "policy": "deterministic_then_judgment",
  "items": [
    { "id": "build", "kind": "deterministic", "command": "mise run build", "expect_exit": 0 },
    { "id": "test", "kind": "deterministic", "command": "mise run test", "expect_exit": 0 },
    {
      "id": "review",
      "kind": "judgment",
      "authority": "agent",
      "rubric": "Matches the OpenAPI spec; no raw-color tokens; error paths handled"
    },
    { "id": "signoff", "kind": "judgment", "authority": "human", "prompt": "Approve the login UX?" }
  ]
}
```

- `deterministic` items are run by the system in a sandbox; the exit code derives pass/fail.
  This is where advisory `criteria` strings that can be operationalized become **binding,
  machine-checked assertions**.
- `judgment` items route to an agent reviewer or a human, who submit a verdict.
- `policy: deterministic_then_judgment` is cost discipline: run the cheap deterministic gate
  first, escalate to expensive judgment only if it passes. Composable with `all` / `any`.

Natural-language criteria that cannot be made deterministic stay as judgment-item rubrics.
The model does not pretend prose is executable.

## Iteration is the convergence loop, not a node

A leaf deliverable's execution _is_ a bounded loop:

```text
deps satisfied → lifecycle: ready → active
loop:
  Attempt[i] with input = intent + upstream accepted outputs + Evaluation[i-1].gaps
  agent works in attempt.session → submits Evidence → attempt ends
  acceptance:  run deterministic checks; if they pass and policy needs it, request verdicts
    ├ all satisfied        → accepted; freeze accepted_output
    ├ failed, i < max      → rejected(gaps) → next iteration (gaps become input)   ← rework
    ├ failed, i == max     → blocked(budget_exhausted) → human decides
    └ verdict required      → blocked(needs_verdict) → human submits → resume
```

**Rework is not a new task type.** It is `Attempt[i+1]` carrying the prior evaluation's gaps
as input. Each attempt's evidence is preserved, so the audit trail reads cleanly: attempt 1
produced X, acceptance rejected with gaps Y, attempt 2 produced Z, accepted.

## Recursion gives decomposition and final acceptance for free

A non-leaf deliverable produces a **decomposition** (the former plan) as the output of an
attempt, gated by a review policy (`none` auto-accepts; `human` awaits approval — the old
plan-review FSM, now scoped to the decomposition). Once accepted, child deliverables and their
edges are materialized, and each child runs its own convergence loop.

When all required children are accepted, the parent runs **its own** acceptance evaluation.
That step _is_ the goal-level final acceptance / synthesizer — not a special, separately-built
feature, but the same acceptance gate every leaf has, applied at the root. The synthesizer
falls out of the recursion.

### What is one deliverable vs two

The recursion needs a hard boundary or it rots into a deep tree of trivial nodes. The
criterion:

> If a downstream can **independently consume, review, reuse, or roll back** the result, it is
> a Deliverable. Otherwise it is an internal attempt phase.

- "Write code → run tests → fix → re-test" → **one** deliverable's convergence loop.
- "Produce a design doc that needs approval before implementation" → **two** deliverables.
- "Migrate the DB schema" and "update the UI", if each is independently acceptable and
  blockable → **two** deliverables.

This is why `verify`, `design`, and `impl` stop being node _types_: verification is every
deliverable's acceptance evaluator, and design/impl are at most categories or internal phases.

## What today's model gets right and keeps

This is a re-rooting, not a teardown. These are already correct and survive unchanged in
spirit:

- **The plan gate** — never run undecomposed work. Today it is spread across `ActivateGoal`,
  the plan, and child counts; it becomes a single rule on the deliverable lifecycle
  (`ready → active` requires a leaf with a contract, or an accepted decomposition with ≥1
  materialized child).
- **The dependency DAG** — kept, but its edges mean _accepted-output_ dependency, not
  procedural order.
- **Handoff** — downstream context from upstream output is right; strengthen it so only
  _accepted_ output flows downstream.
- **The executor boundary** — the agent returns a result; the service owns durable state.
- **Persistent sessions** and the **pending-vs-accepted content isolation** that keeps an
  in-flight edit out of a running prompt.

### Concept mapping

| Today                          | Target                                                   |
| ------------------------------ | -------------------------------------------------------- |
| `agent_goal` (root)            | `deliverable`, `parent_id = NULL`                        |
| goal child statuses            | `deliverable` lifecycle state (derived)                  |
| `agent_goal_plan`              | `deliverable_revision` (decomposition version)           |
| plan status / `review_policy`  | decomposition status / review policy                     |
| plan content items             | proposed child deliverables                              |
| `agent_task`                   | leaf `deliverable`                                       |
| task `running`                 | `attempt` running                                        |
| task `done` (asserted)         | `deliverable` accepted (**derived**)                     |
| task failed — transient        | `attempt` interrupted → new attempt                      |
| task failed — semantic         | acceptance rejected → next attempt, or blocked at budget |
| `criteria []string` (advisory) | `acceptance_contract` items (binding)                    |
| `verify` task (sibling node)   | the deliverable's acceptance evaluation                  |
| `design` / `impl` role         | deliverable category / internal phase                    |
| `handoff.summary`              | attempt evidence summary                                 |
| review `subject=plan`          | decomposition review                                     |
| review `subject=completion`    | judgment items in the acceptance contract                |
| synthesizer (stubbed)          | the parent deliverable's acceptance evaluation           |
| `ReopenTask` (manual rework)   | the next attempt in the convergence loop (automatic)     |

## Simplicity and scalability

An honest assessment — this design is a deep-module trade, not a free lunch.

**Simplicity.** The _model_ is simpler: one recursive concept replaces three inconsistent
state machines, and `done` defined out of existence removes a whole class of "did the agent
lie" reasoning. The _machinery_ is richer, not smaller — derived state must be recomputed
transactionally, and the acceptance contract is a small DSL the planner must author correctly.
The trade is APOSD-shaped: pay internal complexity to buy a simpler interface and eliminate an
error class. The honest caveat: a simple/direct goal does **not** become free — it degrades to
a leaf deliverable with one attempt and a trivial contract, which still drives the full core
machinery. What scales down is the _concept count a caller holds_, not the implementation.

**Scalability.** The model scales in concept: recursion handles arbitrary depth with no new
concepts, new acceptance kinds are additive (no schema change), and the accepted-output DAG
parallelizes naturally. The real ceilings are not the model:

1. **Agent cost and latency is the first wall**, before any database limit. Every rejected
   attempt is another full agent episode; depth × multi-attempt × judgment review multiplies
   token spend and wall-clock. `max_attempts` and `deterministic_then_judgment` are
   load-bearing guards, not optional.
2. **Session and evidence growth** comes next: an attempt per iteration proliferates sessions,
   and evidence (stdout, diffs, artifacts, rationale) grows faster than status rows. Truncate
   by default, externalize artifacts, address them by hash.
3. **The single-writer database** is a real but _designable-away_ constraint. Do not full-scan
   the tree on every event: a child's acceptance updates only the parent's incremental
   counters, parent acceptance is a cached projection, and downstream readiness is pushed by
   accepted-output events. The single writer forbids full rollups — it does not forbid the
   model.
4. **Check-result caching** is needed so a 3-attempt leaf does not rebuild three times. The
   hard part is the cache key, which must include `check_id + command + sandbox image +
repo-tree hash + env hash + upstream accepted-output hashes`. Miss one and the cache is a
   correctness bomb.
5. **Decomposition quality is the autonomy ceiling.** The whole structure rests on the agent
   producing good decompositions and contracts; a bad plan propagates, and clean architecture
   cannot fix it.

## Building toward it without over-building

The target is the north star; do not pour all the concrete at once. The correct first cut is a
**leaf-first deliverable runtime** — and it must be named `Deliverable` from day one. Bolting
acceptance/check/rework fields onto `agent_task` is throwaway work that keeps `task` as the
core concept and trades new debt for old.

The first cut, a true subset of the target:

- `deliverable` (leaf only), `attempt`, `acceptance_event`, and a `deliverable` projection.
- Keep `parent_id` nullable and the `deliverable_edge` table present, with recursion turned
  off. Growing to multi-level trees later opens a capability — it does not change a concept.

This first cut already delivers ~80% of the SDLC value: derived completion, a real acceptance
gate, and a bounded convergence loop with executable checks — without touching recursive
decomposition. The one non-negotiable rule that keeps it clean: **acceptance, evidence, and
the projection are a deep module; nothing outside it may set `done`.**
