---
title: Deliverable model
description: The shipped execution architecture — one recursive Deliverable abstraction with derived completion, a layered acceptance contract, and a bounded convergence loop that iterates work to correctness instead of running a forward-only pipeline once.
---

This page is the canonical explainer for how Stella schedules and converges work. The
execution core is one recursive **Deliverable** abstraction: a root deliverable is the user's
objective, child deliverables are its sub-deliverables, and the same shape repeats all the way
down. Completion is _derived_ from each deliverable's acceptance contract, not asserted by the
agent, and work iterates through a bounded convergence loop until accepted.

## Why a recursive Deliverable, not a pipeline

An earlier execution core ran a forward-only dependency DAG once and called it done. That is a
**pipeline**, not a software development lifecycle, because the lifecycle's defining property —
_iterate until correct_ — is missing. The Deliverable model fixes four structural flaws that a
run-once pipeline carries:

1. **`done` must not be a state the agent sets.** When the worker (or a reviewer's yes/no)
   asserts completion, the system trusts an assertion instead of deriving completion from the
   work's own acceptance contract. The Deliverable model derives completion from the work's
   intent — see [Completion is derived, never asserted](#completion-is-derived-never-asserted).
2. **Acceptance criteria must not be detached, advisory metadata.** Criteria that nothing
   checks and nothing injects into a prompt are decoration. The acceptance contract makes them
   binding — see [The acceptance contract](#the-acceptance-contract).
3. **`verify` must not be a sibling node.** A verify node can pass while the work it "verifies"
   is broken when the two are wired only by a forward dependency edge. Verification is instead
   every deliverable's own acceptance evaluation.
4. **Iteration must be the normal path, not an exception.** Treating rework as failure
   recovery — a failed child rolling the objective to `failed`, then a manual reopen — gets the
   SDLC backwards. Rework is how work converges; it is the next attempt in the loop.

The payoff is one state machine instead of three overlapping, inconsistent ones (an objective,
a plan, and a task each carrying their own statuses). A caller reasons about progress through a
single recursive concept. That is a deep design.

## The core abstraction: one recursive Deliverable

The whole model is a single deep module applied recursively.

> Stella schedules **deliverables**. Agents produce **evidence** through **attempts**. The
> system **evaluates** evidence against the deliverable's **acceptance contract**, iterates
> until accepted / blocked / abandoned, and exposes only **accepted output** to dependent
> deliverables.

A **Deliverable** owns its intent, its decomposition, its acceptance, its attempts, and its
convergence. A root deliverable is the user's objective; its children are deliverables; theirs
are too — the same shape all the way down. `kind` distinguishes a **leaf** (worker-executed)
from a **composite** (decomposed into children). A decomposition is not a peer object, it is a
deliverable's _revision_. An attempt is not a peer object, it is the deliverable's _execution
episode_. A review is not a peer object, it is an _acceptance evidence source_.

### Runtime entities

Four runtime entities plus one optional revision table — deliberately small; more than this
is over-modeling.

| Entity                              | Job                                                                                                                                                                        |
| ----------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `deliverable`                       | Intent + `acceptance_contract` + `convergence_policy` + nullable `parent_id` + nullable accepted output.                                                                   |
| `deliverable_edge`                  | Accepted-output dependency between sibling deliverables (`hard` blocks; `soft` is advisory context).                                                                       |
| `attempt`                           | One execution episode: a persistent agent session, the frozen input context, and the submitted evidence (evidence folds into the attempt — it is not its own table).       |
| `acceptance_event`                  | Append-only record of a deterministic check result or a judgment verdict. Deliverable acceptance state is a **cached projection** over these events, not a mutable column. |
| `deliverable_revision` _(optional)_ | A decomposition version. Used for multi-level decomposition; a leaf deliverable carries none.                                                                              |

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
**derived** = acceptance state, completion, and downstream readiness. The executor returns a
`Result`; the service owns the durable transition. A submit records evidence and triggers
acceptance — it never sets `done` directly.

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

A composite deliverable produces a **decomposition** as the output of an attempt, gated by a
review policy (`none` auto-accepts; `human` awaits approval — the plan-review gate, scoped to
the decomposition). Once accepted, child deliverables and their edges are materialized, and
each child runs its own convergence loop.

When all required children are accepted, the parent runs **its own** acceptance evaluation.
That step _is_ the root-level final acceptance / synthesizer — not a special, separate
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

## The load-bearing invariants

A few rules hold the model together. They are deliberately small and apply at every level of
the recursion:

- **The plan gate** — never run undecomposed work. `ready → active` requires either a leaf
  with a contract, or an accepted decomposition with ≥1 materialized child. It is one rule on
  the deliverable lifecycle, not three checks spread across an objective, a plan, and child
  counts.
- **The dependency DAG** — `deliverable_edge` carries _accepted-output_ dependency, not
  procedural order.
- **Handoff** — only _accepted_ output flows downstream; an in-flight attempt's evidence never
  leaks into a dependent's input.
- **The executor boundary** — the agent returns a result; the service owns durable state.
- **Persistent sessions** and **pending-vs-accepted content isolation** keep an in-flight edit
  out of a running prompt.

### Vocabulary

The recursion folds several once-separate concepts into the single Deliverable abstraction:

| Concept                  | In the Deliverable model                                 |
| ------------------------ | -------------------------------------------------------- |
| user objective (root)    | `deliverable`, `parent_id = NULL`                        |
| sub-objective / sub-task | child `deliverable`                                      |
| objective lifecycle      | `deliverable` lifecycle state (derived)                  |
| plan                     | `deliverable_revision` (decomposition version)           |
| plan items               | proposed child deliverables                              |
| worker-executed task     | leaf `deliverable`                                       |
| task running             | `attempt` running                                        |
| task done                | `deliverable` accepted (**derived**)                     |
| transient task failure   | `attempt` interrupted → new attempt                      |
| semantic task failure    | acceptance rejected → next attempt, or blocked at budget |
| acceptance criteria      | `acceptance_contract` items (binding, machine-checked)   |
| verify step              | the deliverable's acceptance evaluation                  |
| design / impl role       | deliverable category / internal phase                    |
| handoff summary          | attempt evidence summary                                 |
| plan review              | decomposition review                                     |
| completion review        | judgment items in the acceptance contract                |
| synthesizer              | the parent deliverable's acceptance evaluation           |
| rework / reopen          | the next attempt in the convergence loop (automatic)     |

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

## What the leaf runtime delivers, and what recursion adds

The model lands as a **leaf-first deliverable runtime** with recursion layered on top — the
same concept, just more depth turned on. The leaf runtime alone carries most of the SDLC value:

- `deliverable` (leaf), `attempt`, `acceptance_event`, and a `deliverable` projection give
  derived completion, a real acceptance gate, and a bounded convergence loop with executable
  checks — without any recursive decomposition.
- `parent_id` is nullable and `deliverable_edge` is present; multi-level decomposition opens a
  capability on top of the same entities — it does not change a concept.

The one non-negotiable rule that keeps the runtime clean: **acceptance, evidence, and the
projection are a deep module; nothing outside it may set `done`.**
