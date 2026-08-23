"""Harbor adapter for a single Stella evaluation trial."""

from __future__ import annotations

import asyncio
import contextlib
import json
import os
import subprocess
from pathlib import Path
from typing import Any, Mapping

from harbor.agents.installed.base import BaseInstalledAgent
from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext

from .bridge import BridgeServer

EXIT_ADAPTER = 10
EXIT_PRODUCT = 11
EXIT_TIMEOUT = 12

# Bridge error codes that mean the harness broke, not the agent misbehaved.
ADAPTER_FAULT_CODES = {"internal", "bad_nonce", "bad_request"}

# Harbor's trusted native/code treatment is deliberately a bash-only ceiling.
# The bridge cannot prove which child caused a read_file, so view_image and vllm
# are excluded for every run and any such audit entry is invalid evidence.
HARNESS_EXECUTION_TOOL = "bash"

# The task container is controlled through BaseEnvironment, never the host
# process environment. Keep the separately spawned host driver equally narrow:
# it needs runtime basics plus these two scoped credentials, not gateway keys or
# arbitrary Harbor/operator variables.
HOST_CHILD_INHERITED_ENV = ("HOME", "LANG", "LC_ALL", "PATH", "SSL_CERT_DIR", "SSL_CERT_FILE", "TEMP", "TMP", "TMPDIR", "TZ")


def host_child_environment(environ: Mapping[str, str], provisioning_token_env: str, provider_evidence_token_env: str) -> dict[str, str]:
    child = {name: environ[name] for name in HOST_CHILD_INHERITED_ENV if environ.get(name)}
    for source, target in (
        (provisioning_token_env, "STELLA_EVAL_ADMIN_TOKEN"),
        (provider_evidence_token_env, "STELLA_EVAL_PROVIDER_EVIDENCE_TOKEN"),
    ):
        if token := environ.get(source):
            child[target] = token
    return child


def _ledger(path: Path) -> list[dict[str, Any]]:
    if not path.exists():
        return []
    return [json.loads(line) for line in path.read_text().splitlines() if line.strip()]


def bridge_stats(ledger: list[dict[str, Any]]) -> dict[str, Any]:
    """Summarize time actually spent inside the trial container.

    This is the authoritative in-container cost. The driver's tool timing is
    measured from message timestamps and therefore also carries Stella's own
    dispatch overhead; the difference between the two is that overhead.
    """
    ops: dict[str, dict[str, Any]] = {}
    total = 0
    nonzero = 0
    timeouts = 0
    faults: list[dict[str, Any]] = []
    for entry in ledger:
        op = entry.get("op") or "unknown"
        elapsed = int(entry.get("elapsed_ms") or 0)
        stat = ops.setdefault(op, {"calls": 0, "total_ms": 0, "max_ms": 0, "failures": 0})
        stat["calls"] += 1
        stat["total_ms"] += elapsed
        stat["max_ms"] = max(stat["max_ms"], elapsed)
        # A command that ran and exited nonzero is the container answering, not
        # a tool failing: probing for a binary, a test suite failing before the
        # fix, a grep that matched nothing. The ledger is the only structured
        # place that difference survives, so count it here rather than guessing
        # from the message text later.
        if entry.get("ok") and op == "exec":
            code = entry.get("return_code")
            if code == -1:
                timeouts += 1
            elif isinstance(code, int) and code != 0:
                nonzero += 1
        if not entry.get("ok"):
            stat["failures"] += 1
            if entry.get("code") in ADAPTER_FAULT_CODES:
                # not_found and is_dir are the agent asking for the wrong thing.
                # internal and bad_nonce are the bridge itself breaking, which an
                # agent can work around, so reward hides it. Name it separately.
                faults.append({"seq": entry.get("seq"), "op": op, "code": entry.get("code"),
                               "path": entry.get("path")})
        total += elapsed
    return {"total_ms": total, "operations": ops, "adapter_faults": faults,
            "command_nonzero": nonzero, "command_timeout": timeouts}


def execution_metrics(result: dict[str, Any], ledger: list[dict[str, Any]]) -> list[str]:
    """Attach comparable execution metrics and return evidence violations.

    Native mode's transcript is the authoritative core invocation record. Code
    mode intentionally keeps only its synthetic outer `code` call there, so its
    execution record comes from the host-issued child audit. Under Harbor's
    bash-only ceiling, the nonce-bound ledger only corroborates `exec` in audit
    order. It never guesses a tool from a low-level file operation.
    """
    metrics = result.setdefault("metrics", {})
    strategy = result.get("tool_strategy")
    orchestration = metrics.get("tool_call_total")
    metrics["orchestration_tool_call_total"] = orchestration
    if strategy == "native":
        metrics["execution_tool_call_total"] = orchestration
        metrics["execution_tool_error_total"] = metrics.get("tool_error_total")
        metrics["execution_command_nonzero_total"] = metrics.get("command_nonzero_total")
        metrics["execution_tools"] = metrics.get("tools")
        return []
    if strategy != "code":
        return [f"tool strategy is {strategy!r}, want native or code"]

    calls = result.get("stella_tool_calls") or []
    code_activity = any(call.get("name") == "code" for call in calls)
    children = result.get("child_tool_calls") or []
    failures: list[str] = []
    if not isinstance(children, list):
        return ["code child audit is not an array"]
    if children and not code_activity:
        failures.append("code child audit has no outer code activity")
    exec_entries = [entry for entry in ledger if entry.get("op") == "exec"]
    if exec_entries and not children:
        failures.append("code exec ledger has no child audit")
    tools: dict[str, dict[str, int]] = {}
    expected_exec: list[str] = []
    for child in children:
        child_id = child.get("id")
        name = child.get("name")
        is_error = child.get("is_error")
        if not isinstance(child_id, str) or not child_id:
            failures.append("code child audit has no call id")
            continue
        if not isinstance(name, str) or not name:
            failures.append("code child audit has no tool name")
            continue
        if not isinstance(is_error, bool):
            failures.append("code child audit has no error verdict")
            continue
        if name != HARNESS_EXECUTION_TOOL:
            failures.append(f"code child tool {name!r} exceeds Harbor's bash-only capability ceiling")
            continue
        error_kind = child.get("error_kind")
        if is_error and error_kind not in {"tool_error", "command_nonzero"}:
            failures.append("code child audit has no classified error")
            continue
        if not is_error and error_kind is not None:
            failures.append("successful code child audit has an error kind")
            continue
        # A nonzero bash command did reach the bridge and must consume its exec
        # record. A tool_error can happen before bridge admission, so it has no
        # required record; a stray exec remains fatal after matching below.
        if not is_error or error_kind == "command_nonzero":
            expected_exec.append("command_nonzero" if error_kind == "command_nonzero" else "success")
        stat = tools.setdefault(name, {"calls": 0, "errors": 0, "command_nonzero": 0})
        stat["calls"] += 1
        if not is_error:
            continue
        if error_kind == "command_nonzero":
            stat["command_nonzero"] += 1
        else:
            stat["errors"] += 1

    for index, expected in enumerate(expected_exec):
        if index >= len(exec_entries):
            failures.append(f"{expected} bash child has no matching exec bridge record")
            continue
        entry = exec_entries[index]
        if entry.get("ok") is not True:
            failures.append(f"{expected} bash child matched failed exec bridge record")
            continue
        return_code = entry.get("return_code")
        if expected == "success" and return_code != 0:
            failures.append(f"successful bash child matched exec return code {return_code!r}")
        if expected == "command_nonzero" and (not isinstance(return_code, int) or return_code == 0):
            failures.append(f"command_nonzero bash child matched exec return code {return_code!r}")
    if children and len(exec_entries) > len(expected_exec):
        failures.append("code bridge has unaccounted exec operation")
    metrics["execution_tools"] = tools
    metrics["execution_tool_call_total"] = sum(stat["calls"] for stat in tools.values())
    metrics["execution_tool_error_total"] = sum(stat["errors"] for stat in tools.values())
    metrics["execution_command_nonzero_total"] = sum(stat["command_nonzero"] for stat in tools.values())
    return failures


def _path(arguments: dict[str, Any]) -> str | None:
    for name in ("path", "file_path", "filePath"):
        value = arguments.get(name)
        if isinstance(value, str):
            return value
    return None


def _same_file(ledger_path: Any, call_path: str) -> bool:
    """Whether a ledger entry refers to the file a tool call named.

    The two spellings legitimately differ: Stella expands `$TMPDIR` and resolves
    relative paths before the call reaches the bridge, so the transcript can say
    `$TMPDIR/nginx.conf` where the ledger says `/tmp/stella-eval-5d37/nginx.conf`.
    Requiring string equality turned that into a false evidence failure. The file
    name still has to match, which is what the predicate is actually asserting:
    that this work happened inside the container.
    """
    if not isinstance(ledger_path, str):
        return False
    if ledger_path == call_path:
        return True
    name = call_path.rsplit("/", 1)[-1]
    return bool(name) and ledger_path.rsplit("/", 1)[-1] == name


def verify_evidence(result: dict[str, Any], ledger: list[dict[str, Any]], nonce: str) -> list[str]:
    """Return fail-closed predicate violations from ADR §9.

    The bridge predates tool-call IDs, so matching uses the serial tool stream
    and the bridge operation it necessarily emits. Incidental `stat` calls are
    intentionally ignored; they are implementation detail of read/edit.
    """
    failures: list[str] = []
    if result.get("bridge_nonce") != nonce:
        failures.append("bridge nonce does not match")
    if result.get("turn_terminal_state") not in {"completed", "stopped", "errored"}:
        failures.append("turn terminal state is missing")
    if result.get("disabled_tools_count", 0) < 1:
        failures.append("no non-core tools were disabled")
    calls = result.get("stella_tool_calls") or []
    if not calls and not result.get("token_count") and not result.get("timed_out"):
        # A turn with no tokens and no tool calls did nothing; it is a run to
        # investigate, never an attempt the verifier may score as a Stella result.
        failures.append("turn shows no model activity")
    tool_ops = {"exec", "read_file", "read_dir", "write_file"}
    if not calls and any(entry.get("op") in tool_ops for entry in ledger):
        # ping, stat and project are session setup traffic; only real tool
        # operations count as unexplained container access.
        failures.append("bridge ledger has tool operations but Stella reported no core tool calls")
    index = 0
    expected = {"bash": ("exec",), "read": ("read_file", "read_dir"), "write": ("write_file",), "edit": ("write_file",)}
    for call in calls:
        name = call.get("name")
        if name not in expected:
            continue
        if call.get("is_error"):
            # A call that failed may never have reached the sandbox, so it leaves
            # no ledger entry by construction. Demanding one turned a task whose
            # agent hit a few bad edits into a trial with no evidence at all.
            continue
        wanted = expected[name]
        path = _path(call.get("arguments") or {})
        found = False
        # A failed scan must not consume the ledger: otherwise one unmatched
        # call cascades and every later call reports missing too.
        resume = index
        while index < len(ledger):
            entry = ledger[index]
            index += 1
            if entry.get("op") not in wanted:
                continue
            if name == "bash" or path is None or _same_file(entry.get("path"), path):
                found = True
                break
        if not found:
            index = resume
            failures.append(f"{name} tool call has no matching bridge ledger entry")
    return failures


def split_trial_budget(limit: int, margin: int, confirm: int) -> tuple[int, int]:
    """Divide Harbor's trial limit into working time and stop confirmation.

    Harbor kills the trial at `limit`, so working time, the confirmation that
    follows it, and the evidence export all have to fit inside that one number.
    The margin covers process spawn and exit. A limit too small to hold the
    requested confirmation shrinks it rather than starving the work: a quarter
    of the wall is the floor either side can rely on.
    """
    wall = max(1, limit - margin)
    confirm = max(1, min(confirm, wall // 4))
    return max(1, wall - confirm), confirm


class StellaAgent(BaseInstalledAgent):
    """Run Stella on the host while its core tools execute in Harbor's container."""

    def __init__(self, logs_dir: Path, *args: Any, stella_url: str | None = None,
                 admin_token_env: str = "STELLA_EVAL_ADMIN_TOKEN", model: str | None = None,
                 provider_evidence_token_env: str = "STELLA_EVAL_PROVIDER_EVIDENCE_TOKEN",
                 deadline_margin_sec: int = 15, eval_agent_bin: str | None = None,
                 binding_dir: str | None = None, excluded_tools: str | None = None,
                 tool_mode: str | None = None,
                 **kwargs: Any) -> None:
        super().__init__(logs_dir, *args, **kwargs)
        self.stella_url = stella_url or os.environ.get("STELLA_URL", "")
        self.admin_token_env = admin_token_env
        self.provider_evidence_token_env = provider_evidence_token_env
        self.stella_model = model or self.model_name or os.environ.get("STELLA_EVAL_MODEL", "")
        self.deadline_margin_sec = deadline_margin_sec
        self.stop_confirm_sec = self.STOP_CONFIRM_SEC
        self.eval_agent_bin = eval_agent_bin or os.environ.get("STELLA_EVAL_AGENT_BIN", "stella-eval-agent")
        self.binding_dir = binding_dir or os.environ.get("STELLA_EVAL_BRIDGE_DIR", "")
        self.excluded_tools = excluded_tools if excluded_tools is not None else os.environ.get("STELLA_EVAL_EXCLUDED_TOOLS", "")
        self.tool_mode = tool_mode or os.environ.get("STELLA_EVAL_TOOL_MODE", "native")
        self.bundle_digest = ""

    # Cancellation budgets. Both are deliberately small: they run after the
    # trial is already over, and Harbor is waiting on them.
    CHILD_REAP_SEC = 10
    CLOSE_BUDGET_SEC = 20
    # Time reserved after the working deadline for the session to confirm it
    # stopped. Commands are clamped to the working deadline, so this only has to
    # cover the kill and the turn teardown, not the longest tool call.
    STOP_CONFIRM_SEC = 60

    @staticmethod
    def name() -> str:
        return "stella"

    def version(self) -> str | None:
        return "eval-adapter-v1"

    async def install(self, environment: BaseEnvironment) -> None:
        script = Path(__file__).parents[1] / "build_tool_bundle.sh"
        destination = self.logs_dir / "stella-tool-bundle"
        subprocess.run([str(script), "--output", str(destination)], check=True)
        self.bundle_digest = (destination / "capability_profile.sha256").read_text().strip()
        await environment.upload_dir(destination, "/installed-agent/stella")

    async def run(self, instruction: str, environment: BaseEnvironment, context: AgentContext) -> None:
        if not self.stella_url or not self.stella_model or not self.binding_dir:
            raise RuntimeError("Stella adapter needs stella_url, model, and STELLA_EVAL_BRIDGE_DIR")
        trial = str(self.context_id or self.session_id or "harbor-trial")
        trial_dir = self.logs_dir / "stella"
        trial_dir.mkdir(parents=True, exist_ok=True)
        workdir_result = await environment.exec("pwd")
        if workdir_result.return_code != 0:
            raise RuntimeError(f"discover task workdir: {workdir_result.stderr}")
        workdir = (workdir_result.stdout or "").strip() or "/"
        # One wall clock, and the arithmetic that divides it lives here. Harbor
        # kills the trial at HARBOR_AGENT_TIMEOUT_SEC, so working time, the stop
        # confirmation that follows it, and the evidence export all have to fit
        # inside that number. The margin covers process spawn and exit only.
        # Every command the agent runs is clamped to `deadline` too, so nothing
        # is still executing when the confirmation starts.
        deadline, confirm = split_trial_budget(
            int(os.environ.get("HARBOR_AGENT_TIMEOUT_SEC", "900")), self.deadline_margin_sec, self.stop_confirm_sec
        )
        server = BridgeServer(environment, workdir, trial_dir / "bridge.sock", trial_dir / "bridge-ledger.jsonl", tool_path_prepend="/installed-agent/stella/bin", budget_sec=deadline)
        binding = await server.start()
        result_path = trial_dir / "result.json"
        template_path = trial_dir / "binding-template.json"
        template_path.write_text(json.dumps(binding.__dict__))
        instruction_path = trial_dir / "instruction.txt"
        instruction_path.write_text(instruction)
        bundle_digest = (trial_dir / "bundle.sha256")
        bundle_digest.write_text("")
        command = [self.eval_agent_bin, "--stella-url", self.stella_url, "--instruction-file", str(instruction_path), "--binding-template", str(template_path),
                   "--binding-dir", self.binding_dir, "--model", self.stella_model, "--user-id", trial,
                   "--tool-mode", self.tool_mode,
                   "--deadline-seconds", str(deadline), "--stop-confirm-seconds", str(confirm), "--bundle-digest", self.bundle_digest, "--output", str(result_path),
                   "--trajectory", str(trial_dir / "trajectory.json")]
        if self.excluded_tools:
            command.extend(["--excluded-tools", self.excluded_tools])
        child_env = host_child_environment(os.environ, self.admin_token_env, self.provider_evidence_token_env)
        proc = await asyncio.create_subprocess_exec(*command, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE, env=child_env)
        try:
            stdout, stderr = await proc.communicate()
        except asyncio.CancelledError:
            # Harbor's trial deadline cancels this coroutine. Nothing else stops
            # the child, and while it lives it keeps issuing bridge calls into a
            # container Harbor is trying to tear down. Reap it here, bounded, so
            # a cancelled trial ends when it is cancelled.
            proc.kill()
            with contextlib.suppress(asyncio.TimeoutError):
                await asyncio.wait_for(proc.wait(), self.CHILD_REAP_SEC)
            raise
        finally:
            # An exec may still be in flight against a container that is gone or
            # wedged; waiting on it here is how one runaway command used to stall
            # the entire job. Give the close a budget and abandon what is left.
            closing = asyncio.ensure_future(server.close())
            await asyncio.wait([closing], timeout=self.CLOSE_BUDGET_SEC)
            if not closing.done():
                closing.cancel()
        if not result_path.exists():
            raise RuntimeError(f"stella-eval-agent did not write result (stderr: {stderr.decode(errors='replace')[-1000:]})")
        result = json.loads(result_path.read_text())
        ledger = _ledger(trial_dir / "bridge-ledger.jsonl")
        violations = verify_evidence(result, ledger, binding.nonce)
        violations.extend(execution_metrics(result, ledger))
        bridge = bridge_stats(ledger)
        result.setdefault("metrics", {})["bridge"] = bridge
        for fault in bridge["adapter_faults"]:
            violations.append(f"bridge adapter fault {fault.get('code')!r} at seq {fault.get('seq')!r}")
        result["bridge_ledger"] = ledger
        result["valid"] = not violations
        result["predicate_violations"] = violations
        result_path.write_text(json.dumps(result, indent=2) + "\n")
        # Only provider-reported usage reaches Harbor's token and cost fields. A
        # leaderboard reads them as ground truth, and Stella's per-message
        # token_count is a character estimate (len/4), so it is never a fallback:
        # absent is the honest value when the provider reported nothing.
        usage = (result.get("metrics") or {}).get("usage") or {}
        context.n_input_tokens = usage.get("input_tokens")
        context.n_output_tokens = usage.get("output_tokens")
        context.n_cache_tokens = usage.get("cache_read_tokens")
        context.cost_usd = usage.get("cost_usd")
        context.metadata = {"stella_result": result, "stella_exit_code": proc.returncode, "stella_stdout": stdout.decode(errors="replace")[-1000:]}
        if proc.returncode == EXIT_ADAPTER or violations:
            raise RuntimeError("Stella adapter evidence failure: " + "; ".join(violations or result.get("errors", [])))
        # Harbor should still run its verifier for product errors and timeouts.
        if proc.returncode not in (0, EXIT_PRODUCT, EXIT_TIMEOUT):
            raise RuntimeError(f"stella-eval-agent exited {proc.returncode}: {stderr.decode(errors='replace')[-1000:]}")
