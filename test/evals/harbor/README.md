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

The testbed forwards only these two `STELLA_*` variables to `stellad`. Then,
using the credentials file:

- Create a provider with the admin PAT (`POST /api/providers`, for example
  type `openai-response`) and enable one model; the model reference is
  `<provider-id>/<model-id>`.
- Create a provisioning token with an interactive admin session (log in with
  the admin email and password through `POST /api/auth/local/login` and reuse
  the cookie; a PAT gets 403). Export it as `STELLA_EVAL_ADMIN_TOKEN`.
- Export `STELLA_URL`, `STELLA_EVAL_MODEL`, and `STELLA_EVAL_AGENT_BIN`. Keep
  the driver binary outside `dist/bin`; `mise run build` clears that directory.

Run the single Terminal-Bench smoke trial:

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
