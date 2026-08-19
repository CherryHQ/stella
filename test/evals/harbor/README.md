# Harbor evaluation adapter

Run a Stella trial from the host while its `bash`, `read`, `write`, and `edit`
tools operate in the Harbor task container through the bridge backend.

## Prerequisites

- Docker and `uv`
- A running Stella testbed with `STELLA_SANDBOX_BACKEND=bridge` and a private
  `STELLA_EVAL_BRIDGE_DIR`
- `STELLA_EVAL_ADMIN_TOKEN`, a provisioning token for that testbed
- A configured Stella provider and model, for example `openai/gpt-5.6-terra`

Build the two host binaries:

```bash
mise run build
go build -o dist/bin/stella-eval-agent ./cmd/stella-eval-agent
export PATH="$PWD/dist/bin:$PATH"
```

Start a fresh instance only through the sanctioned testbed workflow. Read the
printed credentials-file _path_ to configure your shell; do not print or commit
its contents. Stop it with the paired command when finished.

```bash
export STELLA_SANDBOX_BACKEND=bridge
export STELLA_EVAL_BRIDGE_DIR="$(mktemp -d)"
mise run testbed:start
```

The testbed forwards only these two `STELLA_*` variables to `stellad`, and both
must be exported **before** `testbed:start`: exporting them afterwards has no
effect on the already-running server.

> Get `STELLA_SANDBOX_BACKEND` wrong and the backend falls back to `local`,
> which runs the agent's `bash` on the host machine (under that backend's own
> confinement) instead of in the trial container. The driver refuses to start
> when `/api/status` does not report `sandbox_backend: bridge`, before it
> provisions a user or spends a token, so this now fails fast rather than
> producing a trial that ran in the wrong place.

Then, using the credentials file:

- Create a provider with the admin PAT (`POST /api/providers`, for example
  type `openai-response`) and enable one model; the model reference is
  `<provider-id>/<model-id>`.
- Create a provisioning token with an interactive admin session (log in with
  the admin email and password through `POST /api/auth/local/login` and reuse
  the cookie; a PAT gets 403). Export it as `STELLA_EVAL_ADMIN_TOKEN`.
- Export `STELLA_URL`, `STELLA_EVAL_MODEL`, and `STELLA_EVAL_AGENT_BIN`. Keep
  the driver binary outside `dist/bin`; `mise run build` clears that directory.

Run the single Terminal-Bench smoke trial (add `-k 5` for a reportable
reliability number; a single trial cannot produce one):

```bash
uv run --project test/evals/harbor harbor run \
  -d terminal-bench@2.0 \
  -a stella_harbor.agent:StellaAgent \
  -i regex-log -n 1 \
  -o dist/evals/jobs/regex-log-stella
```

Set `HARBOR_AGENT_TIMEOUT_SEC` to a small value (for example 20) to exercise the
deadline path: the driver must call stop, the turn must end `stopped`, and Harbor
must still run the verifier.

Read the job as a table:

```bash
uv run --project test/evals/harbor python -m stella_harbor.report dist/evals/jobs/regex-log-stella
# add --html to also write a readable report
uv run --project test/evals/harbor python -m stella_harbor.report \
  dist/evals/jobs/regex-log-stella --html dist/evals/report.html
```

Pass the job root, not the timestamp directory inside it. `--html` writes one
self-contained file (no CSS or JS fetched at open time) with the same summary
plus per-trial detail the terminal has no room for: the timing bar, phase
breakdown, bridge operations, and the full bridge ledger.

It prints one row per trial (reward, validity, terminal state, wall/model/tool/
bridge time, turns, tool calls, tool errors, provider-reported tokens and cost),
then the reliability summary: resolution rate with a 95% Wilson confidence
interval, pass^k across tasks, timeouts, every predicate violation, bridge
adapter faults, a failure breakdown, and a per-tool cost table.

The failure breakdown answers what a pass rate cannot: a run is not just "60%
resolved", it is some mix of the agent running out of time, the machinery
failing under it (`execution`), the agent disengaging from the task
(`coherence`), and the agent finishing confidently while wrong (`verification`).
Every rule is deterministic and reads evidence the trial already produced. A
failure no rule explains is labelled `unclassified` and counted, so the gap
stays visible rather than being absorbed into whichever bucket looks plausible.

Adapter faults are bridge failures whose code is `internal` or `bad_nonce`: the
harness broke, not the task. They get their own line because a capable agent
routes around a broken tool and still scores 1.0, so the reward hides them.
A `not_found` is the agent asking for the wrong path and stays an ordinary
failure.

`model` is the time the model held between messages, `tool` is measured from
the message timeline and so includes Stella's dispatch overhead, and `bridge`
is the time actually spent inside the trial container.

Two deliberate refusals, because both would otherwise turn a broken run into a
plausible score:

- Invalid trials leave the denominator instead of counting as failures. They
  produced no evidence, so they are reported separately.
- Token and cost columns carry only provider-reported usage, read from Stella's
  session usage API. A `-` means the provider reported nothing or the model has
  no configured price, never `$0.00`. The per-message `len/4` estimate stays in
  the trial JSON and never reaches a cost column or Harbor's token fields.

Each trial also writes `<trial>/agent/stella/trajectory.json`: the session's
message history exactly as the API returned it, unmodified. It is the artifact a
failure taxonomy and a public run log are built from, so it is stored verbatim
rather than reshaped into the driver's own structs. A history that fills one
page is recorded as `trajectory_truncated`.

## Comparing against a baseline agent

A Stella score means little on its own. `stella_harbor.pi_gateway` runs upstream
pi inside the same task containers on the same model, which is the closest thing
to a controlled baseline:

```bash
set -a; . ./.env; set +a   # OPENAI_BASE_URL and OPENAI_API_KEY
uv run --project test/evals/harbor harbor run -d terminal-bench@2.0 \
  -a stella_harbor.pi_gateway:PiGateway -m gateway/gpt-5.6-terra \
  -i regex-log -k 2 -n 4 -o dist/evals/jobs/pi-sample -q
```

The `gateway` provider exists because pi resolves the base URL of its built-in
`openai` provider from its own model registry and ignores `OPENAI_BASE_URL`, so
a key for an OpenAI-compatible gateway is sent to api.openai.com and 401s. The
adapter writes a `~/.pi/agent/models.json` naming the gateway instead, priced to
match Stella's eval provider so the two cost columns mean the same thing.

Then put the two jobs side by side:

```bash
uv run --project test/evals/harbor python -m stella_harbor.compare \
  dist/evals/jobs/sample dist/evals/jobs/pi-sample --names stella pi
```

The comparison reads only what every Harbor agent writes (reward and the
agent's own reported usage), so it works against a downloaded community job too.
A missing Stella adapter result is reported as "no evidence contract", never as
a failed one.

## Publishing a run

`harbor upload <job>/<timestamp>` sends the whole trial directory to the Harbor
platform. Uploads are private by default and become public only with
`--public`; a public run is what a leaderboard comparison needs.

What each trial publishes: `instruction.txt`, `result.json` (metrics, evidence
verdict and the full bridge ledger), `bridge-ledger.jsonl`, `bundle.sha256`,
`binding-template.json` and `trajectory.json`. A scan of a real trial finds no
provisioning token and no provider API key; the only credential-shaped value is
the per-trial bridge nonce, which authenticates to a Unix socket that is deleted
when the trial ends and is therefore dead by upload time.

The trajectory is the agent's full message history, so it contains whatever the
task put in front of the agent. Read it before making a run public.

Inspect the job's `result.json` and `<trial>/agent/stella/result.json`. A valid
trial has a Harbor verifier result, an adapter result, `valid: true`, a matching
bridge nonce, terminal turn state, and no predicate violations. `valid: false`
is never a pass, even if the verifier reward is one.

```bash
mise run testbed:stop
```

The evaluation instance must have no MCP servers configured. MCP tools are
reported as always enabled and cannot be turned off, so the driver refuses to
start a turn when it finds one and names it in the result.

## Capability profile

`build_tool_bundle.sh` downloads pinned, checksum-verified static `rg` and
`fd` binaries and copies the built-in Stella system skill. `mise` and `tap` are
excluded: their runtime trees are not present in minimal benchmark images. The
bundle manifest and disabled tool list produce `capability_profile_digest` in
each driver result.

Run the adapter-only tests:

```bash
uv run --project test/evals/harbor pytest test/evals/harbor/tests -q
go test ./cmd/stella-eval-agent
```
