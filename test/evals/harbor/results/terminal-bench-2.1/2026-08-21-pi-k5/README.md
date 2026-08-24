# Terminal-Bench 2.1: Pi baseline, 2026-08-21

A complete `k=5` Pi run on the same dataset, model, gateway, and host class as
the 2026-08-20 Stella baseline, so the two rows on the scoreboard compare. Pi is
a different agent implementation, so the difference measures the agent stacks,
not the model.

## Configuration

| Setting                  | Value                                                   |
| ------------------------ | ------------------------------------------------------- |
| Dataset                  | `terminal-bench/terminal-bench-2-1` (89 tasks)          |
| Task digests             | identical to the 2026-08-20 Stella run, all 89 verified |
| Agent                    | `stella_harbor.pi_gateway:PiGateway` (upstream pi)      |
| Model                    | `gateway/gpt-5.6-luna`                                  |
| Attempts                 | `k=5`, as five sequential `k=1` passes                  |
| Concurrency              | `16`                                                    |
| Agent timeout multiplier | `1.0`                                                   |
| Host                     | AWS `c7i.8xlarge`                                       |
| Wall clock               | 2026-08-21 08:14Z → 14:45Z                              |

Five `k=1` passes rather than one `k=5` job: Harbor grows roughly 160 MB of RSS
per trial, and the earlier 445-trial Stella job was OOM-killed at 378 on a 61 GB
host. A pass is also the recovery unit. The passes are independent samples of
the same task set, so merging them is the same population a single `k=5` job
would have produced.

## Result

| Metric          | Pi                       | Stella (2026-08-20)      |
| --------------- | ------------------------ | ------------------------ |
| Resolution rate | **58.2% ±4.6** (259/445) | **47.4% ±4.6** (211/445) |
| 95% CI          | 53.6–62.7                | 42.8–52.1                |
| `pass^5`        | 36.0% (32/89)            | 22.5% (20/89)            |
| Cost            | $10.22 (440 priced)      | $6.83 (391 priced)       |

Per pass: 51, 53, 49, 52, 54 resolved of 89 (57.3–60.7%), so the spread across
passes is well inside the interval and no pass is an outlier.

All 445 trials carry a verifier reward, including the 18 that raised: 12
`AgentTimeoutError` and 6 `NonZeroAgentExitCodeError` still ran the verifier and
scored 0. Failure mix is therefore 168 verification (finished and wrong), 12
deadline, 6 non-zero agent exit.

Per-task shape: 20 tasks resolved none of five, 32 resolved all five, and the
middle is thin (9 / 5 / 12 / 11 at one through four). The same bimodality shows
in the Stella run; `pass^5` alone hides it.

## Head to head

Task by task, Pi resolves more attempts on 35 tasks, Stella on 9, and 45 tie.
Eighteen tasks defeat both agents on all ten attempts, which bounds how much of
the gap any agent change can close.

The widest Pi leads are `llm-inference-batching-scheduler` (5–0),
`caffe-cifar-10` (5–0), `qemu-startup` (5–1), and `compile-compcert` (4–0).
Stella's only clean sweep against Pi is `pytorch-model-recovery` (5–0), where
every Pi attempt exited non-zero; its other leads are one-attempt margins.

## Evidence

- `pi-luna-k5-results.tgz`: per-trial `result.json` and `config.json` for all
  445 trials under their original per-pass job paths, plus the five run-level
  results and the archive `manifest.json`. Built with
  `python -m stella_harbor.archive`, so credential shapes are redacted and the
  manifest records every redaction. Pi's own transcripts are not in it: this run
  predates transcript archiving and the host is gone.
- `pi-luna-k5-report.txt`: the rendered report, one line per trial plus the task
  and cost summaries, readable without unpacking the bundle.

Digests are in `SHA256SUMS`.

The cross-run scoreboard lives in [`../../README.md`](../../README.md).
