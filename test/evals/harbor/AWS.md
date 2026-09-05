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

The controller writes progress to `dist/evals/aws/<run-id>/journal.ndjson`.
A closed stdout pipe does not stop evaluation or file logging. Cleanup is
attempted even if failure reporting raises an exception. If cleanup is
interrupted, resume it with:

```bash
mise run eval:tb21:aws -- --cleanup dist/evals/aws/<run-id>
```
