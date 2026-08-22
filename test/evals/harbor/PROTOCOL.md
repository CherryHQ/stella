# Loop protocol

The loop is the iteration tool between full baselines: run a small task set
against the current checkout, compare it to a same-machine reference, and get
a verdict the same day a fix is written. It is deliberately not a benchmark.
Only a complete 89-task k=5 run on the reference host may touch
`results/README.md`, and only that run may back a public improvement claim; a
single-task k=5 confirmation is never a "complete run". Everything below
exists to keep the cheap loop honest about what it can and cannot conclude.
Designed in #1107; amendments live in that issue's comments.

## Task selection

`tasksets/loop.yaml` is the default set: 12 tasks in three tiers (improvement
targets, high-sensitivity flaky, regression guards), selected from the k=5
baselines and annotated in place. The tier comments are selection rationale
only; task roles are derived at compare time (see verdicts), never parsed
from comments. Arbitrary task selection is equally valid: a targeted fix runs
its own task list, typically single-task at k=5. What is frozen is not the
task list but the comparison discipline: the manifest records exactly what
ran, and the comparator refuses a candidate that is missing any task its
reference declares. There is no silent intersection.

## Definitions

- A **trial is valid** when it produced usable evidence. When Stella adapter
  evidence is present, its verdict is final: an adapter-invalid trial stays
  invalid even if a verifier reward exists. The verifier-reward fallback
  applies only to trials that carry no adapter evidence at all (external
  agents such as pi). Infrastructure failures (bridge faults, harness
  exceptions before the agent ran) make a trial invalid; they leave the
  denominator and are never counted as agent failures.
- A trial is **scoreable** when it is valid and carries a verifier reward.
  Valid only says the agent evidence is trustworthy; a valid trial with no
  reward means the verifier's infrastructure failed, which is not an agent
  failure, so it never counts as unresolved.
- **Resolved** is verifier reward 1.0. All rates are `resolved / scoreable`.
- A task is **judged** only when both sides hold exactly k scoreable trials
  for it. A side that comes up short is re-run until it does, or the task is
  reported as INSUFFICIENT_EVIDENCE; an insufficient task is never folded
  into a verdict.
- A **reference** is a completed loop run of the same task set, at the same k,
  with the same model and gateway, on the same machine, from a stated git
  commit. The pinned EC2 baseline is an absolute sanity check, never the
  verdict: its host architecture and timing differ from a local machine, so
  fix verdicts always come from same-machine before/after pairs.
- A **guard** is any task the reference side resolved k of k scoreable,
  whatever task list
  is in play. The rule is dynamic so explicit task lists need no role
  metadata.

## Verdict tiers

Outcome movement is judged per task, paired against the reference, never as a
headline rate: the loop's trial counts are far too small for one.

- **SIGNAL**: any per-task movement at loop k, including a target rising 0 to
  1. Reported, never gates.
- **SUSPECTED_REGRESSION**: a guard drops below k/k, or any task drops by two
  or more resolved. Reported loudly, still does not gate.
- **Confirmation** is a single-task k=5 run on both sides, candidate first,
  then reference, so gateway drift works against the confirmation rather
  than for it. The predicates are frozen:
  - **CONFIRMED_REGRESSION**: at k=5, candidate resolved is at least two
    below reference resolved. Anything weaker is recorded as DISMISSED with
    both counts.
  - **CONFIRMED_IMPROVEMENT**: entered only when a PR wants to claim a gain;
    at k=5, candidate resolved is at least two above reference resolved. A
    loop-k rise that never takes this step remains a SIGNAL and is reported
    as such.
- Only CONFIRMED_REGRESSION exits nonzero.

The comparator's first argument is always the candidate and its second the
reference, and the report header names both roles; no role is ever inferred.

## Process metrics

At loop scale, process metrics are the primary signal: resolution is 0/1 with
a resolution of one attempt, while cost and tool behavior are continuous,
paired per task, and detect stable movement at k=3. A fix can be a clear win
while resolution stands still (#1076's case: fewer wasted edits at the same
2/5). The comparator reports per-task paired deltas in three trust tiers:

1. **Behavioral** (tool calls, per-tool error counts, turns): host
   independent. Error counts are trustworthy only after #1077.
2. **Gateway-reported** (tokens, cost): host independent.
3. **Wall time**: reported, never judged on an emulating host (Apple Silicon
   runs the amd64 task images under emulation; timing there is noise).

All tiers are displayed. **EFFICIENCY_SIGNAL** triggers on exactly two
metrics: provider-reported cost, and per-tool error counts, judged per tool
name — any single tool crossing the threshold triggers; there is no
aggregate-errors metric. Error counts recorded before #1077 fold command
exits in, so they are displayed with a marker and never judged. The
computation is frozen: per task and side, take the mean over valid trials; the
delta is
`(candidate - reference) / reference`; a reference mean of zero or a missing
value leaves that metric unjudged for that task. A task whose resolved count
is unchanged but whose judged delta exceeds 25% in either direction is an
EFFICIENCY_SIGNAL: shown in the PR table, not a gate. Direction alone proves
nothing; fewer tool calls can mean smarter or can mean giving up earlier, so
efficiency deltas are always displayed beside the resolution column and no
single column is a conclusion.

## Sequential A/B discipline

Sides run as whole jobs, sequentially, on one machine. The mitigations are
part of the protocol, not advice: task images are pulled before either side
runs, so neither side pays the cold cache; every trial records its duration
and timeout class; and a delta whose only outcome change is a timeout-class
flip is marked untrusted rather than judged. Trials within a task are not
paired, so the flip test is count-based: the timeout-class distribution
changed between the sides, the timed-out count moved opposite to the resolved
delta, and its change is at least the delta's size. A flip so detected marks
the task untrusted; it is listed, never folded into a verdict.

The timeout classes are frozen, one per trial, by the first matching rule:

- `harness_timeout`: Harbor recorded a timeout exception for the trial
  (`exception_info` names an agent or verifier timeout).
- `agent_deadline`: the trial deadline stopped the agent mid-task (the
  failure taxonomy's `timeout` evidence: the turn ended `stopped` by the
  deadline).
- `command_timeout`: no trial-level timeout, but at least one tool result
  carries the command-timeout sentinel (a killed command, exit code -1).
- `none`: everything else.

## Thresholds and calibration

Starting values, to be recalibrated by the pilot and recorded here with the
pilot's job ids. The pilot uses 2-3 tasks: an A/A run (the same commit twice
on the same machine) to bound the false-alarm rate, and a concurrency 1/2/4
comparison to fix the concurrency setting.

- Efficiency threshold: 25% paired per-task delta, either direction.
- Loop k: 3. Concurrency: 4, pending the pilot.

The comparator reads k from the jobs' recorded attempt budget; a command-line
k may only fill in a budget the artifacts never recorded, never override one
they did.

## Manifest

Every loop run writes a manifest next to its job directory: git commit,
dirty-tree flag, the task list and its canonical hash (always the SHA-256
of the sorted, newline-joined, dataset-qualified task names, so a taskset
file and an identical explicit list hash the same and a comment edit changes
nothing; when a taskset file was used its own SHA-256 is recorded separately
as provenance), dataset name and hash, per-task image digests,
k, concurrency, timeout multiplier, tool strategy, capability profile digest,
model, gateway base URL host, and created-at. These are the fields the
comparator's fingerprint guard checks. The comparator takes explicit job
paths; nothing ever defaults to "the latest directory".

## Model policy

Verdicts run `gpt-5.6-luna` through the eval gateway, always. A cheaper model
may drive a smoke run for tool-protocol crashes, but its output is never a
verdict: changing the model changes tool-calling strategy, which is exactly
what tool-behavior fixes touch.

## Upgrade triggers

Deliberate ceilings, and what would raise them:

- Whole-job sequential A/B (no per-task interleaving). Revisit if a
  timeout-class flip appears between same-config rounds, or the A/A pilot
  shows directional pass/fail flips beyond what k=3 predicts. The per-trial
  duration and timeout-class records above are the evidence this trigger
  reads.
- Scripted cookie login for testbed provisioning (no server-side bootstrap
  endpoint). The script keeps the cookie in a jar file, never prints a
  credential, and deletes the jar on exit. Revisit if an auth-flow change
  breaks the script more than once.
- EFFICIENCY_SIGNAL never gates. Revisit once A/A data bounds its false-alarm
  rate.
