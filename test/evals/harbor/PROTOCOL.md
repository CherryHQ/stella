# Loop protocol

The loop is the iteration tool between full baselines: run a small task set
against the current checkout, compare it to a same-machine reference, and get
a verdict the same day a fix is written. It is deliberately not a benchmark.
Only a complete k=5 run on the reference host may touch
`results/README.md`, and only that run may back a public improvement claim.
Everything below exists to keep the cheap loop honest about what it can and
cannot conclude. Designed in #1107; amendments live in that issue's comments.

## Task selection

`tasksets/loop.yaml` is the default set: 12 tasks in three tiers (improvement
targets, high-sensitivity flaky, regression guards), selected from the k=5
baselines and annotated in place. Arbitrary task selection is equally valid:
a targeted fix runs its own task list, typically single-task at k=5. What is
frozen is not the task list but the comparison discipline: the manifest
records exactly what ran, and the comparator refuses a candidate that is
missing any task its reference declares. There is no silent intersection.

## Definitions

- A **trial is valid** when it produced usable evidence: the adapter evidence
  validates, or a verifier reward exists. Infrastructure failures (bridge
  faults, harness exceptions before the agent ran) make a trial invalid; they
  leave the denominator and are never counted as agent failures.
- **Resolved** is verifier reward 1.0. All rates are `resolved / valid`.
- A **reference** is a completed loop run of the same task set, at the same k,
  with the same model and gateway, on the same machine, from a stated git
  commit. The pinned EC2 baseline is an absolute sanity check, never the
  verdict: its host architecture and timing differ from a local machine, so
  fix verdicts always come from same-machine before/after pairs.

## Verdict tiers

Outcome movement is judged per task, paired against the reference, never as a
headline rate: the loop's trial counts are far too small for one.

- **SIGNAL**: any per-task movement at loop k, including a target rising 0 to
  1. Reported, never gates.
- **SUSPECTED_REGRESSION**: a guard task drops below k/k, or any task drops
  by two or more resolved. Reported loudly, still does not gate.
- **CONFIRMED_REGRESSION / CONFIRMED_IMPROVEMENT**: the suspected task re-run
  at k=5 on both sides, candidate first then reference, so gateway drift
  works against the confirmation rather than for it. Only a confirmed
  regression exits nonzero.

## Process metrics

At loop scale, process metrics are the primary signal: resolution is 0/1 with
a resolution of one attempt, while cost, tokens, and tool behavior are
continuous, paired per task, and detect stable movement at k=3. A fix can be
a clear win while resolution stands still (#1076's case: fewer wasted edits
at the same 2/5). The comparator reports per-task paired deltas in three
trust tiers:

1. **Behavioral** (tool calls, per-tool error counts, turns): host
   independent, judged. Error counts are trustworthy only after #1077.
2. **Gateway-reported** (tokens, cost): host independent, judged.
3. **Wall time**: reported, never judged on an emulating host (Apple Silicon
   runs the amd64 task images under emulation; timing there is noise).

Same resolution with a paired tier-1 or tier-2 delta beyond the threshold is
an **EFFICIENCY_SIGNAL**: shown in the PR table, not a gate. Direction alone
proves nothing; fewer tool calls can mean smarter or can mean giving up
earlier, so efficiency deltas are always displayed beside the resolution
column and no single column is a conclusion.

## Thresholds and calibration

Starting values, to be recalibrated by the pilot (an A/A run: the same commit
twice on the same machine) and recorded here with the pilot's job ids:

- Efficiency threshold: +/-25% paired per-task delta.
- Loop k: 3. Concurrency: 4, pending the pilot's 1/2/4 comparison.

## Manifest

Every loop run writes a manifest next to its job directory: git commit,
dirty-tree flag, task set and its file hash, k, concurrency, model, gateway
base URL host, capability profile digest, and created-at. The comparator
takes explicit job paths; nothing ever defaults to "the latest directory".

## Model policy

Verdicts run `gpt-5.6-luna` through the eval gateway, always. A cheaper model
may drive a smoke run for tool-protocol crashes, but its output is never a
verdict: changing the model changes tool-calling strategy, which is exactly
what tool-behavior fixes touch.

## Upgrade triggers

Deliberate ceilings, and what would raise them:

- Whole-job sequential A/B (no per-task interleaving). Revisit if a
  timeout-class flip appears between same-config rounds, or the A/A pilot
  shows directional pass/fail flips beyond what k=3 predicts.
- Scripted cookie login for testbed provisioning (no server-side bootstrap
  endpoint). Revisit if an auth-flow change breaks the script more than once.
- EFFICIENCY_SIGNAL never gates. Revisit once A/A data bounds its false-alarm
  rate.
