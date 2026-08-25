# Harbor evaluation adapter

Run a Stella trial from the host while its sandbox tools operate in the Harbor
task container through the bridge backend. The core set is `bash` and
`view_image`, plus `vllm` when the deployment configured a vision model.

For the fix-iteration loop (small task sets, same-machine before/after,
verdict tiers), read [`PROTOCOL.md`](PROTOCOL.md); the default task set is
[`tasksets/loop.yaml`](tasksets/loop.yaml).

## The loop in one command

```bash
set -a; . ./.env; set +a          # OPENAI_BASE_URL and OPENAI_API_KEY
mise run eval:loop                # the 12-task loop set at k=3
```

That builds `stellad` and the eval driver, starts a disposable bridge-backend
testbed, provisions the provider, model, and provisioning token over the API,
runs Harbor, prints the report, and writes a manifest next to the job
directory. The testbed is always stopped and the cookie jar always deleted, on
success and on failure alike; a failed run keeps its job directory and says
where.

```bash
mise run eval:loop -- --plan                                   # print the steps, run nothing
mise run eval:loop -- -i terminal-bench/build-cython-ext -k 5  # one task, k=5
mise run eval:loop -- --against dist/evals/jobs/loop-<earlier> # compare when it finishes
mise run eval:loop -- --tier quick                             # six tasks, k=1, fastest signal
mise run eval:loop -- --excluded-tools view_image              # tool ablation
```

`loop.sh` consumes its own flags (`--tier`, `--otel` / `--no-otel`,
`--excluded-tools`, `--reuse-testbed`, `--against`, `--plan`) and everything
else after `--` reaches `harbor run`, alongside the `-a`, `-m`, `-o`, and `-n`
it supplies itself. Task names must be
dataset-qualified (`terminal-bench/regex-log`): a bare name matches nothing and
silently runs all 89 tasks. Harbor refuses a task filter on top of a config
file, so passing `-i` switches the source from `tasksets/loop.yaml` to the
Terminal-Bench 2.1 dataset; passing your own `-c`, `-d`, `--task`, or `--path`
leaves the source entirely to you.

`--against` puts the new run first, which is what the comparator wants: the
first argument is always the candidate. The comparison is advisory here and
never fails the command; run `stella_harbor.compare` directly for a
confirmation.

The testbed takes a free port from the kernel rather than a fixed one, so a dev
server or an installed `stellad` on this machine keeps its own; set
`STELLA_TESTBED_PORT` to pin it.

Credentials are yours to export and the script never reads `.env`, never
prints a secret, and never puts one in a process argument. The gateway key
reaches the API from a private file and bearer tokens from a curl config on
stdin.

The manifest sits next to the job directory as `<job>.manifest.json` and
records what the comparator's fingerprint cannot derive from the artifacts
alone: commit and dirty flag, the taskset path, the task names when Harbor
records them, k, concurrency,
model, the canonical credential-free gateway endpoint, the Harbor flag **names**
without their values, the OTel setting, the canonical per-run `excluded_tools`
list, and the UTC creation time. Values are deliberately dropped: an argument
can carry a credential or a private path. The driver result carries the same exclusion list in the run
fingerprint, so same-agent runs with different toolsets are rejected by the
comparator.

The first run of a task pays the image pull; Harbor has no separate prefetch,
so a cold machine is slower on its first loop and comparable afterwards.

## Tiers

`--tier` picks the task set, and the task set owns k and concurrency.

| Tier             | Tasks                                            | k         | Concurrency      | Answers                         |
| ---------------- | ------------------------------------------------ | --------- | ---------------- | ------------------------------- |
| `quick`          | 6 ([`tasksets/quick.yaml`](tasksets/quick.yaml)) | 1         | 6                | did I obviously break something |
| `full` (default) | 12 ([`tasksets/loop.yaml`](tasksets/loop.yaml))  | 3         | 4                | did behavior move               |
| targeted         | your `-i` list                                   | your `-k` | from the taskset | did this specific fix work      |

Quick's six tasks were each selected to run under two minutes per trial, so the
tier lands in roughly five minutes on a warm machine. Full is 36 trials at
concurrency 4 and takes substantially longer; time it on your own host rather
than trusting a number here, because the first run of any task also pays the
image pull.

Quick is the edit-run-check cycle and **cannot** tell you a fix worked: one
attempt per task has no pass^k, so a change and a coin flip look identical.
Its job is to catch the break you did not intend, in the time it takes to read
the diff again. Selection is by measured wall time, not difficulty, so a task
being in quick says nothing about how hard it is.

OTel defaults on for quick and off for full, because quick is small enough that
per-turn telemetry is worth its overhead. Override either way with `--otel` /
`--no-otel`.

`--reuse-testbed` skips copying the binaries, downloading Postgres, and
starting the server, which is most of quick's fixed cost. It still runs
`eval:build` every time. It requires an already-running healthy bridge testbed
with OTel off, and `STELLA_URL`, `STELLA_TESTBED_CREDENTIALS`, and the original
`STELLA_EVAL_BRIDGE_DIR` still exported. It cannot retrofit OTel into a testbed
that started without it.

> **A reused testbed keeps serving the `stellad` it started with.** `eval:build`
> refreshes `dist/bin/stellad` in the repo, but nothing copies it into a testbed
> that is already up, and the health check only compares the HEAD commit. So an
> uncommitted edit rebuilds, passes the check, and is **not** what ran. Reuse the
> testbed for reference runs and reruns of unchanged code; restart it after any
> edit to server code you intend to measure.

> **Never set `OTEL_STELLA_RECORD_TOOL_IO` for an eval run.** Terminal-Bench
> ships tasks whose goal is recovering a synthetic secret, and the agent puts
> that secret in its own commands. Recording tool IO writes it to telemetry.

## Evaluating a change

The loop's mechanics are above; this is the order to use them in.

**Every comparison needs its own matched reference.** The comparator judges a
task only when both sides hold exactly k scoreable trials for it, and refuses a
candidate missing any task its reference declares. So a quick reference cannot
back a k=5 confirmation, and a single-task candidate cannot be compared against
a 6-task reference. Three questions, three matched A/B pairs:

| Question              | Both sides run   |
| --------------------- | ---------------- |
| did I break something | `--tier quick`   |
| did behavior move     | `--tier full`    |
| did this fix work     | `-i <task> -k 5` |

A reference always runs the **pre-change build**, on the same machine, model,
and gateway. _When_ in wall-clock time you run it differs by tier: quick and
full references are cheap to take up front and cost a base-commit checkout to
reconstruct afterwards, so take them first. The k=5 confirmation reference is
the exception, and [`PROTOCOL.md`](PROTOCOL.md) fixes its order: candidate
first, then reference, so gateway drift is not free to flatter the change.

**Warm the images before the first side of any pair.** [`PROTOCOL.md`](PROTOCOL.md)
requires it and Harbor has no prefetch step, so whichever side runs first
otherwise pays the cold pull and its trial durations are not comparable to the
other side's. A throwaway run of the same task set is the warm-up; discard the
job directory and never cite it.

```bash
set -a; . ./.env; set +a
mise run eval:loop -- --tier quick                     # quick's six images
mise run eval:loop -- --tier full                      # the full tier's twelve
mise run eval:loop -- -i terminal-bench/<task> -k 1    # one task's image
```

Warm only the tiers the pair will use. On a machine that has run those tasks
recently the images are already warm and this is a no-op you can skip.

**1. Take the quick and full references**, from the commit your PR branches
off.

```bash
set -a; . ./.env; set +a
mise run eval:loop -- --tier quick
mise run eval:loop -- --tier full   # skip only if you will take it in step 4
```

`eval:loop` names each job itself, `dist/evals/jobs/quick-<UTC timestamp>` or
`loop-<UTC timestamp>` for the full tier, and prints the path. **Write them down.** Do not pass your own `-o`: the script
already passes one, and nothing here ever defaults to "the latest directory".

Every run on both sides must come from a clean tree. The manifest records a
`dirty` flag, and a dirty run is not evidence: nobody, including you next
week, can say what code produced it. Commit before you measure.

**2. Iterate on quick.** Fast enough to run on every meaningful edit. Compare
quick against quick and read it as a break detector, not as evidence.

```bash
mise run eval:loop -- --tier quick --against dist/evals/jobs/<quick reference>
```

**3. Confirm the fix at single-task k=5 — candidate first, then reference.**
The reference here is taken _now_, by checking out the base commit and running
the same command again. That ordering is the protocol's, not a convenience.

```bash
# Commit the change first; both runs must report dirty: false.
mise run eval:loop -- -i terminal-bench/<the task your fix targets> -k 5
git checkout <base commit>       # or run this half in a second checkout
mise run eval:loop -- -i terminal-bench/<the same task> -k 5
git checkout -
uv run --project test/evals/harbor python -m stella_harbor.compare \
  dist/evals/jobs/<candidate> dist/evals/jobs/<reference> \
  --names after before --confirm
```

If you are reusing a testbed, restart it between those two runs: a running
testbed keeps serving the binary it started with, so both runs would measure
the same code.

The first path is always the candidate. `--against` runs the same comparison
inline, but it is advisory and never fails the command; run `compare --confirm`
yourself for a verdict.

`--confirm` applies the frozen predicates in [`PROTOCOL.md`](PROTOCOL.md): two
or more resolved apart either way, anything weaker is `DISMISSED`. A rise at
loop k that never takes this step is a `SIGNAL`, and calling it an improvement
in a PR is a claim the evidence does not support.

**4. Run the full tier on both sides before opening the PR**, so the guards get
their k=3. If you skipped the full reference in step 1, take it now from the
base commit; that is a checkout and a rerun, not an excuse to skip it. Then,
with both sides in hand:

```bash
mise run eval:loop -- --tier full --against dist/evals/jobs/<full reference>
```

**5. Read the run before reading the score.** A number from a broken run is
worse than no number. Confirm, for both sides:

- the comparator reported no blocking fingerprint mismatch, and you did not
  reach for `--allow-mismatch`
- every task holds exactly k scoreable trials, no `INSUFFICIENT_EVIDENCE`
- no invalid trials and no bridge adapter faults
- no task marked `UNTRUSTED` by a timeout-class flip
- both manifests say `dirty: false`

**6. Put the evidence in the PR.** A table, both sides named by job and commit:

The confirmation from step 3 is what backs a claim, so lead with it:

```markdown
**Confirmation** (`terminal-bench/<task>`, k=5 both sides, `--confirm`):
CONFIRMED_IMPROVEMENT, 1/5 → 4/5 resolved.

| Metric      | Reference (`<base commit>`) | Candidate (`<head commit>`) |
| ----------- | --------------------------- | --------------------------- |
| Resolved    | 1/5                         | 4/5                         |
| Turns       | 61                          | 38                          |
| Tool calls  | 58                          | 34                          |
| Tool errors | 9                           | 1                           |
| Cost        | $0.0412                     | $0.0330                     |

Jobs: `dist/evals/jobs/<ref>` and `dist/evals/jobs/<cand>`. Same host,
`gpt-5.6-luna`, both `dirty: false`.

**Full tier** (12 tasks, k=3): no SUSPECTED_REGRESSION, no guard dropped
below 3/3. Jobs: `dist/evals/jobs/loop-<ref>` and `dist/evals/jobs/loop-<cand>`.
```

Paste the comparator's own output alongside it, not just your summary of it.
State the tier, k, host, and model: a number without them is not reproducible
and will be read as a benchmark result, which it is not.

**7. When a measurement is invalidated, mark it superseded, do not delete it.**
A configuration change or a broken host makes earlier numbers void, not
nonexistent. Keep them in the PR under a heading that says why they no longer
apply. A reviewer who saw the first table needs to know what happened to it;
silently swapping in better numbers is the same shape as fabricating them.

## What the loop cannot see

The loop measures the task set and nothing else. Its verdicts are only as
broad as the tasks that ran.

Binary and image _files_ do appear: `pytorch-model-cli` ships an `image.png`
and a `model.pth`, `pytorch-model-recovery` three `.pt` files. What no task
requires is the agent **understanding** them. The images are program input, so
a trial passes without ever looking at one. Unexercised, therefore, and worth
naming precisely:

- model-facing pixel understanding (`view_image`, `vllm`)
- document extraction (pdf, docx, xlsx, pptx, epub)
- CRLF and trailing-newline fidelity through a read-modify-write
- non-UTF-8 encodings
- a binary read as if it were text, which is the failure the size budget makes
  expensive

A change to any of those can measure as a clean improvement here while being
broken in exactly the way the task set cannot express. During the #1132 /
#1134 tool work every defect that mattered was found by review and local
reproduction, not by the loop, while the score went up.

So: **if your change touches a surface no task exercises, say so in the PR**
rather than letting the score imply coverage it does not have. Adding a task
is better than the disclaimer, and the disclaimer is much better than nothing.

One concrete gap to know about: [`build_tool_bundle.sh`](build_tool_bundle.sh)
installs only `rg` and `fd`, so `xberg` is not present in any trial. The
`xberg extract` guidance in bash's tool description has therefore never
executed in an eval run.

## Native specialized-tools lane

`mise run eval:loop -- --tier specialized-tools` runs the frozen local three-task
Native lane at `k=3`, concurrency `1`, with `view_image,vllm` explicitly
excluded. Every trial, including the Skill and Memory/Library tasks, uses the
same mode-0600 fixture config and creates one same-named, per-trial MCP
registration, opaque route, and cleanup lease. The admitted surface is fixed:
`bash`, `skills`, `memory`, `library_search`, `recally`, plus the exact 53-tool
MCP catalog. Admission explicitly sets each fresh agent's `user_agent` policy:
when a catalog builtin (`skills`, `memory`, `library_search`, or `recally`) is
disabled it is enabled, and every other non-core tool is explicitly disabled;
core `bash` is never patched. The driver then re-reads the inventory and
attests the full union in the actual runtime catalog before any per-task
verifier runs. The provider-surface, runtime-catalog, and capability-profile
digests are each fixed across all three tasks. Share is not part of this lane.

Each trial provisions a fresh user. The host, not the task container, creates
and verifies its fixtures and writes the nonce-bound verdict after the turn is
terminal. `STELLA_EVAL_FIXTURE_PLAN_SEED` is required for this lane: the
mode-0600 host fixture config carries it, each task's private token is derived
by HMAC, and results record only the plan digest. Reuse the same seed for both
sides of a matched pair, never put it in task files or logs. A wrong artifact,
missing fact, duplicate Recally write, or incomplete MCP chain is valid with
reward `0`; an API, bridge, lease, inspection, catalog-admission, or
surface-attestation failure is invalid. The Memory/Library task deliberately
uses `/workspace/evidence.txt`; its verifier reads the exact container artifact
rather than a Share snapshot.

Baseline qualification is a sequential A/A gate, defined in
[`PROTOCOL.md`](PROTOCOL.md): Job A must resolve every task `3/3` before Job B
may run. The task set owns the exact `k=3` and concurrency `1` conditions.
Record both job ids and per-task paired provider-cost deltas; do not replace an
attempt after a rerun. A task change creates a new identity, commit, and
fixture-plan seed, and restarts qualification at Job A.

### Baseline stop-line: 2026-08-25 Native Job A

`dist/evals/jobs/specialized-tools-20260825T155754Z`
(`2026-08-25__23-58-18`, commit `2274eacf4406e1ee0e0524a7712d9a4ce0654a0e`)
is **valid harness evidence but baseline-ineligible**. All `9/9` trials were
valid and scoreable under the matched Native-lane conditions: clean commit,
`k=3`, concurrency `1`, `view_image,vllm` excluded, a 58-tool runtime surface,
and the 53-tool MCP catalog. It recorded no failure-class or cleanup anomaly.

Skill resolved `3/3`; MCP resolved `1/3` (`2/3` reward `0`); Memory resolved
`0/3` (`3/3` reward `0`). The Memory cumulative diagnostic is `0/1 + 0/3 =
0/4`. Job A therefore failed the frozen gate, and Job B was intentionally not
run: no B result can make Job A's Memory outcome `3/3`, and running it would
change the rule after seeing A. The baseline remains incomplete. Retain Skill;
redesign or remove MCP and Memory, then use a new identity, commit, and
fixture-plan seed to restart from Job A.

This is not a superseded or broken-harness classification. The earlier
superseded diagnostics below retain their stated invalidity and causes.

The no-model watchdog seam is deliberately separate from a Harbor score run:

```bash
STELLA_RUN_REAL_EVAL_SEAM=1 \
  uv run --project test/evals/harbor pytest \
  test/evals/harbor/tests/test_agent.py::test_real_no_model_specialized_watchdog_seam -q
```

It starts the real testbed subprocess and embedded database, builds the eval
child, admits the real HMAC MCP fixture and Memory/Library lease, and uses only
a loopback scripted OpenAI Responses SSE endpoint. It freezes the child after
its durable `stream_start` breadcrumb so the adapter's actual direct-kill
watchdog can prove the final durable phase. The phase journal is diagnosis
only, never a Harbor verdict or reward.

### Superseded diagnostic: 2026-08-24 Native Job A

`dist/evals/jobs/specialized-tools-20260824T125957Z` (`2026-08-24__21-00-24`)
is **invalid and superseded**, never a baseline: all 9 trials stopped before
the turn because the lane checked default-disabled catalog tools instead of
setting its policy. Its first recorded error, `lane catalog tool "skills" is
disabled or absent`, is the causal failure.

The three cleanup-recovery HTTP 500s occurred after that pre-turn admission
failure, so this archived diagnostic cannot establish an independent cleanup
fault. Normal pre-turn cleanup and early-exit lease recovery are covered by
adapter tests; a future cleanup failure remains harness-invalid and must not be
masked by task reward.

### Superseded diagnostic: 2026-08-24 Native Job B

`dist/evals/jobs/specialized-tools-20260824T133420Z` (`2026-08-24__21-34-51`)
is **invalid and superseded**, never a baseline: all 9 trials stopped before
the turn because the fresh testbed tool inventory omitted the real runtime
`skills` builtin, making lane policy configuration fail with `lane catalog tool
"skills" is absent`. The subsequent memory-library cleanup HTTP 500 exposed a
separate fixture teardown defect: Library files must be deleted through the
Library lifecycle before their Agent, whose database relationship deliberately
restricts direct deletion.

### Superseded diagnostic: 2026-08-24 Native Job C

`dist/evals/jobs/specialized-tools-20260824T151753Z` is **invalid and
superseded**, never a baseline: the runtime MCP tool surface was missing after
the turn, so the lane could not attest its required 53-tool catalog. This is an
adapter-evidence failure, not a task reward or a measurement.

The system coverage added in `4f2446a5b` was a **false green** for this defect:
it rebuilt a separate stateless `/mcp` fixture and derived its expected catalog
from the same shared names. It therefore never exercised the testbed's HMAC
trial route, stateful session ID, or the Go SDK's mandatory
`notifications/initialized` request. The replacement test uses the actual
fixture transport and route handler, requires `initialize` →
`notifications/initialized` → `tools/list` evidence, and attests the final
58-tool session surface against its fixed digest. Job C remains superseded.

### Superseded diagnostic: 2026-08-25 Native Job `033115Z`

`dist/evals/jobs/specialized-tools-20260825T033115Z` is **invalid and
superseded**, never a baseline. Its three MCP trials are scoreable evidence of
the admitted task surface, but the job also recorded six bounded-wall
exceptions during timeout finalization. A job with any such exception cannot
support a lane measurement.

The bridge reset observed during those exceptions was recovery behavior, not
the root cause. The causal defect was finalization spending a separate timeout
on stop confirmation, surface/admission inspection, evidence export, and
cleanup, so the child could outlive Harbor's one trial wall. #1140 gives that
whole sequence one bounded finalization context; no A/A result or Job B is
needed to supersede this diagnostic.

### Superseded diagnostic: 2026-08-25 Native Job `075047Z`

`dist/evals/jobs/specialized-tools-20260825T075047Z` is **invalid and
superseded**, never a baseline. It predates the unified absolute
`finalize-by` deadline, so its relative child budget could not prove that Go
finalization, result writing, and process exit all fit inside Harbor's parent
watchdog. It remains archived diagnostic evidence only; it does not establish a
successful replacement or a Native baseline.

### Superseded diagnostic: 2026-08-25 Native Job `104347Z`

`dist/evals/jobs/specialized-tools-20260825T104347Z` is **invalid and
superseded**, never a baseline. Its SSE observer returned a clean `finish` plus
`[DONE]` at the working cutoff while the server-owned session remained
`working`. The driver treated that clean stream as completion and spent the
post-work finalization budget passively waiting for a terminal state instead of
issuing `/stop`; Harbor's parent watchdog therefore ended the child before it
could persist a result. This is deadline-driver failure evidence, not a task
outcome or a successful replacement run.

### Superseded diagnostic: 2026-08-25 Native Job `113003Z`

`dist/evals/jobs/specialized-tools-20260825T113003Z` is **invalid and
superseded**, never a baseline. Its watchdog termination has no safe durable
phase attribution in the archived evidence. **Attribution is unknown pending
the phase journal**; do not infer or repair a root cause from this job.

### Superseded diagnostic: 2026-08-25 Native Job `133210Z`

`dist/evals/jobs/specialized-tools-20260825T133210Z` is **invalid and
superseded**, never a baseline. The adapter correctly rejected the trial before
spawning the child: its 180s outer wall tried to fit a 180s Go finalization
budget plus a 72s post-Go reserve, an impossible 252s allocation. **No model
traffic occurred.** This is allocation diagnosis only, not a task outcome or a
successful replacement run.

## Prerequisites

- Docker and `uv`
- `OPENAI_BASE_URL` and `OPENAI_API_KEY` for the eval gateway, exported

## Manual setup

`mise run eval:loop` does all of this. It is written out because it is what to
step through when the loop itself is what is broken.

- Docker and `uv`
- A running Stella testbed with `STELLA_SANDBOX_BACKEND=bridge` and a private
  `STELLA_EVAL_BRIDGE_DIR`
- `STELLA_EVAL_ADMIN_TOKEN`, a provisioning token for that testbed
- A configured Stella provider and model, for example `openai/gpt-5.6-terra`

Build the two host binaries:

```bash
mise run build
go build -o dist/bin-eval/stella-eval-agent ./cmd/stella-eval-agent
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
the tool itself failing: a `view_image` on a path that does not exist, a `vllm`
call the vision model rejected. `cmd!0` (`command_nonzero_total`) is a command that
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

### The loop comparator

`PROTOCOL.md` is the authority for what the comparator concludes; this is how to
drive it. The first path is the candidate, the second the reference:

```bash
uv run --project test/evals/harbor python -m stella_harbor.compare   dist/evals/jobs/after dist/evals/jobs/before --names after before
```

- **Coverage is required.** A candidate missing any task its reference declares
  is refused by name. There is no silent intersection. To compare a subset on
  purpose, pass `--tasks a,b`; the flag is echoed in the output, and only the
  task-set dimension is relaxed — model and dataset stay hard.
- **k comes from the run budget** (`n_attempts`); `--k` only fills in a budget
  the artifacts never recorded and is refused when it conflicts with one they
  did. An unrecorded budget is otherwise a blocking fingerprint issue, so `--k`
  is what makes an archived run comparable; it answers that one question and
  no other, and any further mismatch still refuses. A task is judged only when both sides hold exactly k scoreable trials;
  anything else prints `INSUFFICIENT_EVIDENCE` and is excluded from every
  verdict.
- **A side may span several jobs.** Repeat `--candidate-job` / `--reference-job`
  when a side's k trials were topped up in a second run. Every path is still
  named explicitly; nothing defaults to the latest directory. A top-up passes
  the same identity validation as a positional job, the attempt budget being
  the one permitted difference. Every other field is judged in three states:
  both jobs recorded it and the values differ, or one recorded it and the other
  carries no evidence at all, are both refused; a field neither job ever
  recorded is reported as `unrecorded` and does not block, because refusing
  mutual silence would condemn the re-run path a top-up exists to serve. Inside
  one job, partial coverage whose values agree is that job's value with its
  coverage reported; two different values inside one job are refused. The same
  run or trial offered twice is refused rather than counted twice.
- **Verdicts.** Any per-task movement is a `SIGNAL`. A guard (a task the
  reference resolved k of k) falling below k/k, or any task down two or more
  resolved, is a `SUSPECTED_REGRESSION`. Neither gates: the default mode always
  exits 0.
- **Confirmation.** `--confirm` applies the frozen single-task k=5 predicates:
  two or more resolved apart is `CONFIRMED_REGRESSION` or
  `CONFIRMED_IMPROVEMENT`, anything weaker is `DISMISSED` with both counts. Only
  `CONFIRMED_REGRESSION` exits nonzero. An `UNTRUSTED` task confirms nothing,
  `--allow-mismatch` is refused outright, and a top-up carrying an `unrecorded`
  identity field is refused too: an identity nobody records is tolerable in a
  report and not underneath the one verdict that gates.
- **Process metrics** print in the protocol's three trust tiers: behavioral
  (tool calls, per-tool error counts, turns), gateway-reported (tokens, cost),
  and wall time, which is displayed and never judged. Error counts from before
  #1077 are marked `*` and never judged, because they fold nonzero command exits
  in. `EFFICIENCY_SIGNAL` triggers on exactly two metrics, provider cost and
  per-tool error counts, past a 25% paired delta with the resolved count
  unchanged; a reference mean of zero or missing leaves that metric unjudged.
- **Timeout classes** are recorded per trial (`harness_timeout`,
  `agent_deadline`, `command_timeout`, `none`), and a task whose only outcome
  change is a timeout-class flip is marked `UNTRUSTED` and not judged. The flip
  has to point the right way: the timed-out count must move opposite to the
  resolved delta, so an improvement is not thrown away and a regression that
  came with fewer timeouts is not hidden.

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

**Check disk before every run, not after a bad one.** Docker running out of
space does not report itself as a disk problem. The trials come back as
**bridge adapter faults**, and the underlying errors are buried:
`Error setting up pivot dir ... no space left on device` and
`chown ... no space left on device`. It looks exactly like a harness bug, and
it silently voided a complete k=5 baseline before anyone read the container
logs.

```bash
docker system df                     # before the run
df -h /var/lib/docker
```

If a run reports adapter faults, check disk first, before suspecting the
bridge. Reclaiming space between runs:

```bash
docker builder prune -af             # the build cache, usually the largest
docker image prune -f                # dangling layers
docker container prune -f            # every stopped container on this daemon
```

The first two remove nothing a rerun cannot rebuild. **`container prune` is
not in that class**: it deletes every stopped container the daemon has, not
just this run's, so it belongs on a disposable eval host and nowhere else. On a
shared machine list first and remove by name:

```bash
docker ps -a --filter status=exited --format '{{.ID}} {{.Names}} {{.Image}}'
```

Do not add `-a` to `image prune` or touch named volumes without reading what
they hold; task images are expensive to pull again.

**Never run the whole 89-task set as one `-k 5` job.** (A single-task `-k 5`
confirmation is 5 trials and perfectly safe; this is about the 445-trial
baseline.) Harbor grows roughly 160 MB of RSS per completed
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

`xberg` is **not** in the bundle. bash's tool description tells the model it may
be available and to check `command -v xberg` first, so a trial takes the
fallback path every time. That is correct behavior, not a bug, but it means the
extraction path is unmeasured here: no eval run has ever executed it. Adding it
would change `capability_profile_digest`, which the comparator treats as agent
identity, so runs from before and after the change are not comparable to each
other.

The tool set a trial sees is the deployment's core set (`bash`, `view_image`,
and `vllm` when a vision model is configured) minus anything `--excluded-tools`
hid for that run. The exclusion list is recorded in the manifest and in the run
fingerprint, so the comparator refuses to put two same-agent runs with
different toolsets side by side.

Run the adapter-only tests:

```bash
uv run --project test/evals/harbor pytest test/evals/harbor/tests -q
go test ./cmd/stella-eval-agent
```
