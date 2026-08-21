# Terminal-Bench 2.1: Stella and Pi, 2026-08-20

This directory preserves the raw evidence and comparison for the Luna control
run. Both agents used `gateway/gpt-5.6-luna` against the same endpoint, dataset,
instance class, concurrency, and deadline multiplier. Pi is intentionally a
different agent implementation, so this measures the agent stacks rather than
the model alone.

## Configuration

| Setting                  | Value                                                                     |
| ------------------------ | ------------------------------------------------------------------------- |
| Dataset                  | `terminal-bench/terminal-bench-2-1` (89 tasks)                            |
| Dataset hash             | `sha256:7d7bdc1cbedad549fc1140404bd4dc45e5fd0ea7c4186773687d177ad3a0699a` |
| Model                    | `gateway/gpt-5.6-luna`                                                    |
| Attempts                 | `k=5`                                                                     |
| Concurrency              | `16`                                                                      |
| Agent timeout multiplier | `1.0`                                                                     |
| Host                     | AWS `c7i.8xlarge`                                                         |

## Result

Stella completed the full five attempts per task. Its result combines jobs in
this order, retaining the first five valid trials of each task: `luna-k5`,
`luna-k5b`, then `luna-k5c`.

| Population                    |   Stella resolved |       Pi resolved |  Difference |
| ----------------------------- | ----------------: | ----------------: | ----------: |
| Full Stella run, 89 tasks × 5 | 211 / 445 (47.4%) |                 — |           — |
| Matched observed Pi trials    | 143 / 304 (47.0%) | 175 / 304 (57.6%) | Pi +10.5 pp |

For the matched observed trials, Stella's Wilson 95% interval is 41.5–52.7%;
Pi's is 51.9–63.0%. At task level, Pi has more resolved attempts on 32 tasks,
Stella on 8, and 49 are tied. The most pronounced Pi advantages are
`llm-inference-batching-scheduler` (4–0) and `qemu-startup` (4–1); Stella leads
on `pytorch-model-recovery` (4–0) and `qemu-alpine-ssh` (4–2).

## Limitation

The Pi job stopped when Harbor's installed Pi adapter attempted to decode a
truncated UTF-8 agent-output file. Harbor's final counters were 320 completed,
28 errored, and 125 pending. The preserved artifacts contain 320 trial records:
304 have a verifier reward, while 16 have no scoreable reward (2 non-zero agent
exits, 13 cancellations, and 1 timeout). Each task has only 1–4 scoreable Pi
trials, so there is no complete Pi `pass^5` estimate and this must not be read
as a full k=5 comparison.

The matched row selects, for every task, the same number of Stella trials as
the number of scoreable Pi trials and uses Stella's original merged trial order.
It excludes unstarted Pi trials rather than treating them as failures.

## Evidence bundles

- `luna-k5-results.tgz`: all raw Harbor results for the full merged Stella
  baseline, including trial results, adapter evidence, configuration, and logs.
- `pi-luna-k5-partial-results.tgz`: preserved Pi result JSON, configuration,
  and scheduler logs from the interrupted run. Per-trial stdout/stderr logs are
  intentionally excluded because Terminal-Bench includes synthetic-secret
  tasks; they do not contribute to scoring or reproducibility.

- `stella-luna-k5-report.txt`: the rendered report for the merged Stella
  baseline, one line per trial plus the task, tool, and failure summaries, so
  the numbers can be read without unpacking the bundle.

Both bundles are self-contained relative to `dist/evals/` paths. Their SHA-256
digests, and the report's, are recorded in `SHA256SUMS`.

The cross-run scoreboard lives in [`../../README.md`](../../README.md).
