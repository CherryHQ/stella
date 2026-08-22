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

Terminal-Bench 2.1 (`terminal-bench/terminal-bench-2-1`, 89 tasks) is the
dataset to run. It keeps 2.0's task collection but repairs 28 tasks whose
verification was broken, which moved scores enough that the two versions cannot
be compared: one unchanged model and harness gained 12.1 points from the repairs
alone. 2.0 (`terminal-bench@2.0`, legacy registry) still resolves, and the k=5
baseline in #1054 was measured on it.

Run the single Terminal-Bench smoke trial (add `-k 5` for a reportable
reliability number; a single trial cannot produce one):

```bash
uv run --project test/evals/harbor harbor run \
  -d terminal-bench/terminal-bench-2-1 \
  -a stella_harbor.agent:StellaAgent \
  -i terminal-bench/regex-log -n 1 \
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
bridge time, turns, tool calls, tool errors, nonzero command exits,
provider-reported tokens and cost), then the reliability summary: resolution rate with a 95% Wilson confidence
interval, pass^k across tasks, timeouts, every predicate violation, bridge
adapter faults, a failure breakdown, and a per-tool cost table.

`errs` and `cmd!0` are deliberately two columns. `errs` (`tool_error_total`) is
the tool itself failing: an `edit` whose `old_string` did not match, a `read` of
a path that does not exist. `cmd!0` (`command_nonzero_total`) is a command that
ran to completion and exited nonzero: probing for a binary, a test suite failing
before the fix, a `grep` that matched nothing. That is the container answering,
not the machinery breaking, and only `errs` feeds the `execution` failure class.
The driver classifies each result at the source, from the sessions API's
`error_kind`, never by reading the message text. `cmd!0` shows `-`, never 0, whenever the
trial did not measure the field: a Stella run archived before the split, or an
agent that writes no adapter metrics at all (pi and anything else driven through
Harbor alone). For a pre-split Stella trial the report recounts the exits from
the bridge ledger instead; a non-Stella trial has nothing to recount and reports
no tool counts either. The per-tool table follows the same rule per column — one
contributing trial without the count makes the whole total unknowable, so it
prints `-` rather than a partial sum.

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
uv run --project test/evals/harbor harbor run -d terminal-bench/terminal-bench-2-1 \
  -a stella_harbor.pi_gateway:PiGateway -m gateway/gpt-5.6-luna \
  --ak cost_input=0.20 --ak cost_output=1.20 \
  --ak cost_cache_read=0.02 --ak cost_cache_write=0.25 \
  -i terminal-bench/regex-log -k 2 -n 4 -o dist/evals/jobs/pi-sample -q
```

The prices are per million tokens and must be the model's own; the adapter
refuses to run without them. They are baked into every trial as it finishes and
cannot be recomputed afterwards, so pass them as `--ak`: Harbor records agent
kwargs in each trial's `config.json`, which is the only place the price a trial
was scored at survives. `EVAL_COST_INPUT` and friends still work as a fallback,
but leave no trace in the artifacts. `--ak context_window=` and `--ak
max_tokens=` override the limits, which default to gpt-5.6-luna's.

`-i` matches the dataset's qualified task name, so `regex-log` alone silently
matches nothing and runs all 89 tasks.

The `gateway` provider exists because pi resolves the base URL of its built-in
`openai` provider from its own model registry and ignores `OPENAI_BASE_URL`, so
a key for an OpenAI-compatible gateway is sent to api.openai.com and 401s. The
adapter writes a `~/.pi/agent/models.json` naming the gateway instead, priced
from the same numbers as Stella's eval provider so the two cost columns mean the
same thing.

Then put the two jobs side by side:

```bash
uv run --project test/evals/harbor python -m stella_harbor.compare \
  dist/evals/jobs/sample dist/evals/jobs/pi-sample --names stella pi
```

Before reading any score, the comparison derives and checks a run fingerprint
from the Harbor artifacts. It includes dataset id and hash, attempt budget,
concurrency, timeout multiplier, model, agent name, tool strategy, capability
profile digest, and candidate commit. Dataset, model, budget, concurrency, and
timeout are run conditions and must be present and equal. Agent name,
capability profile, tool strategy, and candidate commit are agent identity: a
same-agent comparison checks the capability and tool fields, while allowing the
candidate commit to differ; a cross-agent comparison reports both identities
without using them as a gate.

A value difference is a hard refusal under `CONFIGURATION DIFFERENT`; a missing
run-condition value is a separate hard refusal under `CANNOT VERIFY
CONFIGURATION`, with the field and expected artifact location named explicitly.
Missing agent-identity fields are reported with coverage but do not block a
cross-agent comparison. Internal inconsistency inside any run is reported and
blocks the comparison. For an intentional exploratory comparison only, pass
`--allow-mismatch`; every non-empty line of the resulting report is then marked
`[UNTRUSTWORTHY COMPARISON]` so the exception cannot disappear in copied output.
The Stella driver writes the actual model reference as `model` and
`git rev-parse HEAD` as `candidate_commit` into each driver result. Missing
values are never inferred from the current checkout.

The comparison reads only what every Harbor agent writes (reward and the
agent's own reported usage), so it works against a downloaded community job too.
A missing Stella adapter result is reported as "no evidence contract", never as
a failed one.

## Pi UTF-8 recovery and the archived k=5 rerun

Harbor 0.21.0's installed `Pi.populate_context_post_run()` calls
`Path.read_text()` on `pi.txt`. A truncated final UTF-8 sequence therefore raises
`UnicodeDecodeError` while Harbor is syncing the agent result, after the agent
has already run. The `PiGateway` subclass now decodes the file strictly first,
then uses replacement decoding only for usage parsing. A damaged file keeps its
original bytes in the Harbor logs and records `agent_result.metadata.pi_output_decode`
with the UTF-8 error count, whether the error was an EOF truncation, and the file
size. The warning is visible in the trial log, so recovery cannot silently turn a
damaged trial into clean evidence.

A local adapter override is deliberate here instead of patching Harbor in place.
It is the smallest change that protects this controlled Pi baseline immediately,
without forking or replacing Harbor for every installed agent. An upstream Harbor
patch remains the right long-term fix, but it must first be accepted and released;
when that happens, remove this override after verifying the released behavior.

To reproduce the archived Stella baseline's Pi configuration after this fix,
human operators must provide the exact same gateway endpoint used by the archived
run and a credential for it. Do not substitute the default OpenAI endpoint:

```bash
export OPENAI_BASE_URL='<the exact endpoint used by the archived baseline>'
export OPENAI_API_KEY='<credential for that endpoint>'

cat >/tmp/stella-pi-luna-k5.json <<'JSON'
{
  "job_name": "pi-luna-k5-rerun",
  "jobs_dir": "dist/evals/jobs/pi-luna-p1",
  "n_attempts": 1,
  "agent_timeout_multiplier": 1.0,
  "n_concurrent_trials": 16,
  "quiet": true,
  "agents": [
    {
      "name": "stella_harbor.pi_gateway:PiGateway",
      "model_name": "gateway/gpt-5.6-luna",
      "kwargs": {
        "cost_input": 0.20,
        "cost_output": 1.20,
        "cost_cache_read": 0.02,
        "cost_cache_write": 0.25
      }
    }
  ],
  "datasets": [
    {
      "name": "terminal-bench/terminal-bench-2-1",
      "ref": "sha256:7d7bdc1cbedad549fc1140404bd4dc45e5fd0ea7c4186773687d177ad3a0699a"
    }
  ]
}
JSON

uv run --project test/evals/harbor harbor run \
  --config /tmp/stella-pi-luna-k5.json
```

`n_attempts` is 1 on purpose: run this five times with `jobs_dir` counting up to
`pi-luna-p5` and merge the passes, for the memory reason in
[Running a full baseline](#running-a-full-baseline). The prices belong in
`kwargs` so Harbor records them in each trial's `config.json`; they are the
gpt-5.6-luna prices the archived run used, per million tokens.

Run it on the same `c7i.8xlarge` class as the archived baseline. The command
above preserves the required 89-task dataset, concurrency `16`, agent timeout
multiplier `1.0`, model `gateway/gpt-5.6-luna`, and dataset SHA-256; five passes
of it are the archived `k=5`.
The endpoint and API credential are intentionally not stored in this repository.

## Running a full baseline

Everything above is one trial on a laptop. A reportable baseline is 89 tasks
times k attempts, and at that size the harness, not the agent, is what usually
ends the run. This section is the whole recipe; the numbers are what the
2026-08-20 and 2026-08-21 Terminal-Bench 2.1 runs actually used.

**Host.** An AWS `c7i.8xlarge` (32 vCPU, 64 GB) at `-n 16`. Concurrency above 16
does not finish faster: the tasks are mostly IO- and container-bound, and every
extra worker costs about 4 GB of headroom you will need later.

**Docker first, or the run dies at trial 60.** The default address pool runs out
of `/24` networks at `-n 16` and Harbor starts failing to create task networks.
Fix it before the first trial and restart Docker:

```json
// /etc/docker/daemon.json
{
  "default-address-pools": [
    { "base": "10.201.0.0/16", "size": 24 },
    { "base": "10.202.0.0/16", "size": 24 }
  ]
}
```

**Never run one `-k 5` job.** Harbor grows roughly 160 MB of RSS per completed
trial and does not give it back. A 445-trial job reached 62 GB and was
OOM-killed at trial 378, taking six hours of work with it. Run k separate `-k 1`
passes into separate job directories instead. A pass is then also the recovery
unit: a crash costs 89 trials, not the run.

```bash
for pass in 1 2 3 4 5; do
  out="dist/evals/jobs/base-p${pass}"
  [ -d "$out" ] && continue          # resume without redoing finished passes
  uv run --project test/evals/harbor harbor run \
    -d terminal-bench/terminal-bench-2-1 \
    -a stella_harbor.agent:StellaAgent \
    -k 1 -n 16 -q -o "$out"
  docker rm -f $(docker ps -aq) >/dev/null 2>&1
  docker network prune -f >/dev/null 2>&1
done
```

Run it detached (`nohup setsid`) with its output in a log file. A pass takes 45
to 105 minutes depending on how many long tasks land in it, so a five-pass run
is six to eight hours.

**Watch it without disturbing it.** The run-level counters and the per-trial
rewards are both in the job tree, so progress needs no extra instrumentation:

```bash
d=$(ls -d dist/evals/jobs/base-p1/*/ | head -1)
jq -c '.stats | {n_completed_trials, n_errored_trials, n_pending_trials}' "$d/result.json"
jq -s '[.[] | select(.verifier_result.rewards.reward == 1)] | length' "$d"*/result.json
free -g | sed -n 2p                  # RSS headroom is the thing that kills runs
```

A pass that sits at 88 of 89 for an hour is usually not stuck: one long task,
`train-fasttext` in this dataset, routinely runs past 90 minutes. Check that its
container is still up before concluding anything.

**Then finish the job.** Merging the passes, rendering the report, archiving the
evidence, and adding the scoreboard row are one procedure, written down once in
[`results/README.md`](results/README.md). Follow it there. In short: merge the
passes into one directory of exactly k trials per task, render the report,
archive with `stella_harbor.archive --include-trajectories`, record the digests,
and add the row only if every planned trial produced valid evidence.

**Terminate the host.** An idle `c7i.8xlarge` costs more per day than the run
did. Record the instance id in a file when you create it, and terminate it as
the last step of the run, not the next morning.

### Flags that fail silently

- `-i` and `-x` need the dataset-qualified name, `terminal-bench/regex-log`, not
  `regex-log`. A bare name matches nothing, and Harbor's answer to matching
  nothing is to run all 89 tasks. This has cost a full rerun; it is the single
  easiest way to waste six hours here.
- `-t/--task` takes a registry `org/name` only. It is not a filter for a dataset
  you already passed with `-d`.
- Prices reach a trial through `--agent-kwarg`, and cost is computed when the
  trial ends. There is no recomputing it afterwards, so a wrong price is
  permanent and a missing one is not free, it is unknown.

## Creating an evidence archive

Archive a completed Harbor job without changing its gitignored source files:

```bash
uv run --project test/evals/harbor python -m stella_harbor.archive \
  dist/evals/jobs/pi-luna-k5-rerun --output \
  test/evals/harbor/results/terminal-bench-2.1/2026-08-21-pi-luna-k5
```

By default this produces the public-safe payload: every `result.json` and
`config.json`, `manifest.json`, and `SHA256SUMS`, with no trajectory files. Use
`--include-trajectories` only for a private diagnostic bundle:

```bash
uv run --project test/evals/harbor python -m stella_harbor.archive \
  dist/evals/jobs/pi-luna-k5-rerun --include-trajectories --output \
  /private/path/pi-luna-k5-diagnostic
```

With that explicit switch, the command adds a redacted transcript for every
trial that has one, whatever agent produced it: Stella's `trajectory.json` and
upstream pi's JSON-lines `pi.txt`. Passing trials are included too, because a
run that passed is the evidence for how it passed, and the redaction is
content-based, so the verdict never decided how safe a transcript was. The
source job, including its unredacted transcripts, stays under `dist/` and must
be retained securely outside the repository. It is the artifact for later
failure attribution. A public archive is for score and evidence verification,
not for publishing the model's solution path.

Known credential shapes are replaced with the `[redacted_secret]` marker:
private-key headers, `ghp_`/`github_pat_`/`sk-` tokens, secret assignments,
credential-bearing URL userinfo, valid Bearer/Basic Authorization headers,
JWTs, high-entropy mixed-case tokens, and the trial's bridge nonce.

A value that looks like a credential but matches no known shape is dropped
whole, and its JSON path is listed in the manifest. That is the fail-closed
case: an unclassified shape is exactly where the redactor cannot tell where the
secret ends, so trimming it is not safe and keeping it is worse. A transcript
that cannot be parsed at all, a truncated pi stream for instance, is excluded
entirely rather than copied raw.

The same rules apply to `result.json` and `config.json`. They have to: a
Terminal-Bench task whose goal is recovering a password puts that password in
the agent's own commands, and those commands are recorded in `result.json`.
Only a file that is not valid JSON stops the archive, before anything is
written.

`manifest.json` records whether transcripts were requested, the redaction rules
version, per-trial classification, one entry per transcript (`kind`, status of
`disabled`, `included`, or `excluded`, exclusion reason, redaction count,
dropped locations, and source hash), and the same redaction counts and dropped
locations for every payload file. `SHA256SUMS` checks every archived
payload file plus the manifest. The command refuses a non-empty output directory
and refuses an output path inside the source job. Do not run it against the
append-only `results/` directory as the source, and do not try to repair
historical archives whose trajectories were never preserved.

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
