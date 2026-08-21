# Harbor evaluation results

Every scoreable Harbor run Stella has produced, newest first. One row is one
job; the archived rows keep their raw evidence in
`<benchmark>/<date>-<name>/`.

Read the **Comparable** column before putting two rows side by side. A score
only compares against another score with the same benchmark version, model,
gateway, and harness generation. Voided rows record what went wrong and must
never be quoted as a score.

## Scoreboard

| Date       | Benchmark | Agent  | Model             | k | Resolution rate          | pass^k      | Cost  | Comparable                     |
| ---------- | --------- | ------ | ----------------- | - | ------------------------ | ----------- | ----- | ------------------------------ |
| 2026-08-20 | TB 2.1    | Stella | `gpt-5.6-luna`    | 5 | **47.4% ±4.6** (211/445) | 22.5%       | $6.83 | baseline, current harness      |
| 2026-08-20 | TB 2.1    | Pi     | `gpt-5.6-luna`    | 5 | **57.6%** (175/304)      | —           | —     | partial: run died at 320/445   |
| 2026-08-20 | TB 2.1    | Stella | deepseek-v4-flash | 5 | ~~22.9% (102/445)~~      | ~~0.0%~~    | $2.69 | **VOID**: gateway rejected 246 |
| 2026-08-19 | TB 2.0    | Stella | deepseek-v4-flash | 5 | 58.8% ±4.8 (238/405)     | unavailable | $8.83 | no: TB 2.0, pre-fix harness    |

Cost is provider-reported and always a lower bound: trials the provider never
priced are excluded, which is not the same as costing nothing.

## Row notes

**2026-08-20 · TB 2.1 · Stella · gpt-5.6-luna.** The reference baseline. All 445
trials carry valid evidence, so the leaderboard-style score equals the
resolution rate. Failure mix: 176 verification (finished and wrong), 41 deadline,
16 provider stream failures, 1 coherence. Archived in
[`terminal-bench-2.1/2026-08-20-luna-vs-pi/`](terminal-bench-2.1/2026-08-20-luna-vs-pi/),
with the rendered per-trial report in `stella-luna-k5-report.txt`.

**2026-08-20 · TB 2.1 · Pi · gpt-5.6-luna.** The control for the same model and
host. Harbor's installed Pi adapter died decoding a truncated UTF-8 agent-output
file, leaving 304 scoreable trials and no `pass^5`. Against the same 304 trials
Stella resolves 47.0%, so Pi leads by 10.5 points on the observed population
only. Archived alongside the Stella row.

**2026-08-20 · TB 2.1 · Stella · deepseek-v4-flash.** Void, not a score. 246 of
445 trials died on the gateway rejecting replayed assistant tool calls
(`the content[].thinking in the thinking mode must be passed back to the API`).
Measurement showed this is not fixable from the client: echoing
`reasoning_content` back does not reduce the rate, and tool-call responses carry
no reasoning signature at all. Per-model probe: deepseek-v4-flash 20-55%,
deepseek-v4-pro 10/10, gpt-5.6-terra 0/10, gpt-5.6-luna 0/12. Artifacts were not
archived.

**2026-08-19 · TB 2.0 · Stella · deepseek-v4-flash.** Predates three harness
fixes (unbounded container commands, the finalization wedge, and tool-fault
miscounting), and ran a different dataset version. 40 of 445 trials were never
scoreable and 106 hit the deadline. Kept for history only. Artifacts were not
archived.

## Adding a run

1. Merge the jobs into one directory holding exactly k trials per task, taking
   the first k valid trials in job order, decided before looking at rewards.
2. Render the report:
   `python -m stella_harbor.report <merged-dir> --html <out>.html`.
3. Create `<benchmark>/<date>-<name>/` with a `README.md` recording dataset
   name and hash, model, k, concurrency, timeout multiplier, host class, the
   result, and every limitation that bounds it.
4. Bundle the raw trial JSON, adapter evidence, config, and scheduler logs.
   Exclude per-trial stdout/stderr: Terminal-Bench ships synthetic-secret tasks
   and those logs contribute nothing to scoring.
5. Record digests in `SHA256SUMS` and add a row above.

The trial artifacts do not record the model name, so step 3 is the only place it
is written down. Getting it wrong makes the row uncomparable and unfixable
later.
