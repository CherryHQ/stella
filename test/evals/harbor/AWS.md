# AWS eval configuration

[中文](AWS.zh.md)

The AWS runner reads `AWS_REGION`, `OPENAI_BASE_URL`, `OPENAI_API_KEY`, and
`OPENAI_MODEL` from the deployment-local `.env`. It accepts models other than
Luna. Keep credentials out of Git and command output.

Set all four cost variables in `.env` or the calling environment, in USD per
million tokens: `EVAL_COST_INPUT`, `EVAL_COST_OUTPUT`, `EVAL_COST_CACHE_READ`,
and `EVAL_COST_CACHE_WRITE`. Use `0` for a category with no separate charge.
The runner forwards these values to the remote evaluation. They are supplied
estimates, not prices discovered from the gateway or a billing reconciliation.
Values in `.env` override the calling environment.

```bash
mise run eval:tb21:aws -- --plan
mise run eval:tb21:aws -- --smoke --commit HEAD
mise run eval:tb21:aws -- --commit HEAD
```

Full mode runs an excluded warm-up followed by five ordered dataset passes.
Warm-up trials without scoreable evidence are retried, up to
`--max-topup-rounds` (default 3), without selecting on reward. Warm-up evidence
is discarded before the measured passes. New run IDs use `tb21-experimental`;
record the model and commit when citing results. Follow [PROTOCOL.md](PROTOCOL.md)
for comparison claims.

## Performance experiments

The historical model warm-up remains the default. Opt in with
`--warmup environment`: Harbor's `nop --install-only` starts and health-checks
the pinned task environments without calling a model or a verifier. Preparation
uses at most four concurrent environments and retains Docker build artifacts.
It does not test Stella installation or model connectivity; measured trials
still do those steps. Cache reuse and its speedup must be measured on the worker.

Use `--topup-concurrency 4` to queue missing tasks together, within the main
concurrency limit. Only invalid or unscored attempts are retried. A scoreable
zero is never retried. The default of one retains sequential top-ups.

Before choosing a new concurrency, run the bounded AWS pilot:

```bash
mise run eval:tb21:aws -- --pilot --warmup environment --concurrency 4 --topup-concurrency 4 --timeout-hours 3 --commit HEAD
```

This prepares all 89 environments, then runs the same five smoke tasks four
times at concurrency `1,4,4,1` on one disposable worker. It is a performance
experiment, not a full benchmark or a model improvement verdict. Agent deadlines
are unchanged. The combined sample deliberately has no single concurrency
fingerprint. `performance.json` records each stage's wall time, exit code and
sampled host memory/CPU/load, without command arguments or credentials. Top-up
time is separate from primary runs. Existing cleanup and shutdown backstops apply.

Changing warm-up or concurrency changes the run conditions. Do not compare an
experimental score against an archived baseline as if those conditions matched.

For full-dataset capacity testing, use:

```bash
mise run eval:tb21:aws -- --capacity --warmup environment --concurrency 16 --max-topup-rounds 0 --timeout-hours 4 --commit HEAD
```

This runs 89 tasks once at each of `16,32,48,64,16` workers on the same host,
up to 445 attempts. The final 16-worker pass checks time-of-day drift. There
are no top-ups. A sample below 8 GiB available memory or a new host OOM kill
stops the current command. A failed command, incomplete task set, or at least
five unscoreable trials stops further passes. Task deadlines remain unchanged.

Each completed pass uploads a checksummed checkpoint with per-task outcomes,
timeout classes, observed trial overlap, phase timing, and scoreable trials per
hour. `capacity-summary.json` distinguishes `running`, `stopped`, and `completed`;
an interrupted run may contain only an earlier checkpoint. The capacity mode
does not produce a merged benchmark score. Compare throughput with failure and
timeout counts before recommending a setting. Passing 64 workers establishes
a tested lower bound on capacity, not the machine's absolute limit.

The controller writes progress to `dist/evals/aws/<run-id>/journal.ndjson`.
A closed stdout pipe does not stop evaluation or file logging. Cleanup is
attempted even if failure reporting raises an exception. If cleanup is
interrupted, resume it with:

```bash
mise run eval:tb21:aws -- --cleanup dist/evals/aws/<run-id>
```
