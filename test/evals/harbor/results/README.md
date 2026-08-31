# Harbor evaluation results

Complete Harbor runs only, newest first. One row is one job, and a job earns a
row only when every planned trial produced valid evidence: k attempts on every
task, no unscoreable trial, no harness fault bounding the result. Partial and
voided runs are diagnosis, not measurement, and stay out of this table; they
belong in the issue that ordered the run.

The primary comparison is Stella against its own previous release. Other agents
may appear as reference rows, never as a target. Two rows compare only when
benchmark version, model, gateway, and harness generation all match. Nothing
here is comparable to a published leaderboard unless that configuration is
stated and identical.

## Scoreboard

| Date       | Benchmark | Agent  | Model          | k | Resolution rate          | pass^k | Cost   | Trials        |
| ---------- | --------- | ------ | -------------- | - | ------------------------ | ------ | ------ | ------------- |
| 2026-08-31 | TB 2.1    | Stella | `gpt-5.6-luna` | 5 | **52.8% ±4.6** (235/445) | 37.1%  | $6.80  | 445 scoreable |
| 2026-08-21 | TB 2.1    | Pi     | `gpt-5.6-luna` | 5 | **58.2% ±4.6** (259/445) | 36.0%  | $10.22 | 445 valid     |
| 2026-08-20 | TB 2.1    | Stella | `gpt-5.6-luna` | 5 | **47.4% ±4.6** (211/445) | 22.5%  | $6.83  | 445 valid     |

Cost is provider-reported and always a lower bound: trials the provider never
priced are excluded, which is not the same as costing nothing.

## Row notes

**2026-08-31 · TB 2.1 · Stella Code Mode · gpt-5.6-luna.** Complete current-harness run: all 89 tasks have five selected scoreable trials; one extra adapter-invalid attempt was excluded before selection. It resolved 235/445, a raw +5.4 percentage-point movement from the 2026-08-20 Stella run, and its `pass^5` rose from 22.5% to 37.1%. This begins the long-term Stella performance timeline; the historical capability treatment differs, so the movement is descriptive context rather than causal evidence. Pi remains an optional peer reference, not a release target. Archived in [`terminal-bench-2.1/2026-08-31-luna-code-mode-k5/`](terminal-bench-2.1/2026-08-31-luna-code-mode-k5/).

**2026-08-21 · TB 2.1 · Pi · gpt-5.6-luna.** Upstream pi through the same
gateway, on the same 89 task digests, as an optional peer reference. Run as five sequential `k=1` passes
because Harbor leaks ~160 MB per trial and OOM-killed the Stella job at 378 of
445; the passes resolved 51/53/49/52/54, well inside the interval. Failure mix:
168 verification, 12 deadline, 6 non-zero agent exit. Task by task Pi leads on
35, Stella on 9, 45 tie, and 18 tasks defeat both on all ten attempts. Archived
in [`terminal-bench-2.1/2026-08-21-pi-k5/`](terminal-bench-2.1/2026-08-21-pi-k5/).

**2026-08-20 · TB 2.1 · Stella · gpt-5.6-luna.** The reference baseline. All 445
trials carry valid evidence, so the leaderboard-style score equals the
resolution rate. Failure mix: 176 verification (finished and wrong), 41 deadline,
16 provider stream failures, 1 coherence. Per-task shape is bimodal, which
`pass^5` alone hides: 29 tasks resolved none of five attempts, 20 resolved all
five, 17 sat at four. Archived in
[`terminal-bench-2.1/2026-08-20-luna-vs-pi/`](terminal-bench-2.1/2026-08-20-luna-vs-pi/),
with the rendered per-trial report in `stella-luna-k5-report.txt`.

## Adding a run

1. Merge the jobs into one directory holding exactly k trials per task, taking
   the first k valid trials in job order, decided before looking at rewards.
2. Render the report:
   `python -m stella_harbor.report <merged-dir> --html <out>.html`. The HTML
   headline reads the run against Stella's own release history in
   `timeline.json`; add `--peers` to overlay other agents, which stay off by
   default because a peer is a reference, not Stella's target.
3. Create `<benchmark>/<date>-<name>/` with a `README.md` recording dataset
   name and hash, model, k, concurrency, timeout multiplier, host class, the
   result, and every limitation that bounds it.
4. Bundle it with the archive tool, never by hand:
   `python -m stella_harbor.archive <job> --output <dir> --include-trajectories`.
   It keeps `result.json`, `config.json`, and every agent transcript it can
   redact, scrubs credential shapes, drops any string it cannot classify, and
   writes a `manifest.json` recording what was redacted and what was dropped.
   Terminal-Bench ships synthetic-secret tasks, so a raw `tar` of a job
   publishes their passwords verbatim.
5. Record digests in `SHA256SUMS`, add a row above, and add the matching entry
   to [`timeline.json`](timeline.json) in the same commit — that file is what
   the HTML report plots, and a scoreboard row without it is invisible to every
   later report. Do this only if the run is complete. If it is not, record what happened in the issue and leave the table
   alone.

The trial artifacts do not record the model name, so step 3 is the only place it
is written down. Getting it wrong makes the row uncomparable and unfixable
later.
