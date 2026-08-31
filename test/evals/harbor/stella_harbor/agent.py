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

# Harbor's treatment is deliberately a bash-only ceiling. The bridge cannot
# prove which child caused a read_file, so view_image and vllm are excluded for
# every run and any such audit entry is invalid evidence.
HARNESS_EXECUTION_TOOL = "bash"

# The task container is controlled through BaseEnvironment, never the host
# process environment. Keep the separately spawned host driver equally narrow:
# it needs runtime basics, the provisioning credential, and a safe evidence
# file path, never gateway keys, admin credentials, or arbitrary variables.
HOST_CHILD_INHERITED_ENV = ("HOME", "LANG", "LC_ALL", "PATH", "SSL_CERT_DIR", "SSL_CERT_FILE", "TEMP", "TMP", "TMPDIR", "TZ")


def host_child_environment(environ: Mapping[str, str], provisioning_token_env: str, provider_evidence_file_env: str) -> dict[str, str]:
    child = {name: environ[name] for name in HOST_CHILD_INHERITED_ENV if environ.get(name)}
    for source, target in (
        (provisioning_token_env, "STELLA_EVAL_ADMIN_TOKEN"),
        (provider_evidence_file_env, "STELLA_EVAL_PROVIDER_EVIDENCE_FILE"),
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


def _ordered_children(result: dict[str, Any]) -> list[dict[str, Any]]:
    calls = result.get("stella_tool_calls") or []
    nested = [child for call in calls for child in (call.get("children") or [])]
    if nested:
        return nested
    # Backward-compatible fallback for archived driver results that predate
    # per-outer-call child correlation.
    children = result.get("child_tool_calls") or []
    return children if isinstance(children, list) else []


def _ordered_bash_calls(result: dict[str, Any]) -> list[dict[str, Any]]:
    ordered: list[dict[str, Any]] = []
    for call in result.get("stella_tool_calls") or []:
        if call.get("name") == "bash":
            ordered.append(call)
        elif call.get("name") == "code":
            ordered.extend(child for child in (call.get("children") or []) if child.get("name") == "bash")
    return ordered


def execution_metrics(result: dict[str, Any], ledger: list[dict[str, Any]]) -> list[str]:
    """Attach comparable execution metrics and return evidence violations.

    Execution counters cover provider-visible bash and audited Code-child
    bash. Code Mode may use every other Stella capability too; those calls are
    orchestration, not attributable task-container execution. The bridge ledger
    supplies command exit/timeout counts for the combined bash stream.
    """
    metrics = result.get("metrics")
    if not isinstance(metrics, dict):
        metrics = {}
        result["metrics"] = metrics
    orchestration = metrics.get("tool_call_total")
    metrics["orchestration_tool_call_total"] = orchestration
    # An outer `code` call that fails is an orchestration fault, not a bash
    # fault, so it must not land in the execution counters. Counting it nowhere
    # would make the run look error-free.
    metrics["orchestration_tool_error_total"] = metrics.get("tool_error_total")

    children = _ordered_children(result)
    failures: list[str] = []
    if not isinstance(children, list):
        return ["code child audit is not an array"]
    child_bash = [child for child in children if child.get("name") == "bash"]
    tool_metrics = metrics.get("tools")
    direct = tool_metrics.get("bash") if isinstance(tool_metrics, dict) else None
    direct_calls = direct.get("calls", 0) if isinstance(direct, dict) else 0
    direct_errors = direct.get("errors", 0) if isinstance(direct, dict) else 0
    child_errors = sum(
        1 for child in child_bash
        if child.get("is_error") and child.get("error_kind") not in {"command_nonzero", "command_timeout"}
    )
    bridge = bridge_stats(ledger)
    bash = {
        "calls": direct_calls + len(child_bash),
        "errors": direct_errors + child_errors,
        "command_nonzero": bridge["command_nonzero"],
        "command_timeout": bridge["command_timeout"],
    }
    metrics["execution_tools"] = {"bash": bash} if bash["calls"] else {}
    metrics["execution_tool_call_total"] = bash["calls"]
    metrics["execution_tool_error_total"] = bash["errors"]
    metrics["execution_command_nonzero_total"] = bash["command_nonzero"]
    metrics["execution_command_timeout_total"] = bash["command_timeout"]
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
    excluded = result.get("excluded_tools") or []
    if not isinstance(excluded, list) or not {"view_image", "vllm"}.issubset(excluded):
        failures.append("required core exclusions are missing")
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
    # Match direct and per-Code-call child bash outcomes to exec records in the
    # exact transcript order before handling legacy file tools.
    bash_calls = _ordered_bash_calls(result)
    exec_entries = [entry for entry in ledger if entry.get("op") == "exec"]
    exec_index = 0
    for call_index, call in enumerate(bash_calls):
        kind = call.get("error_kind") if call.get("is_error") else "success"
        mandatory_after = sum(
            1 for later in bash_calls[call_index + 1:]
            if not later.get("is_error") or later.get("error_kind") in {"command_nonzero", "command_timeout"}
        )
        mandatory = kind in {"success", "command_nonzero", "command_timeout"}
        if not mandatory:
            # tool_error may happen before admission. Consume an exec only when
            # doing so cannot steal evidence from a later mandatory outcome.
            if len(exec_entries) - exec_index > mandatory_after:
                exec_index += 1
            continue
        if exec_index >= len(exec_entries):
            failures.append(f"{kind} bash tool call has no matching exec bridge record")
            continue
        entry = exec_entries[exec_index]
        exec_index += 1
        if entry.get("ok") is not True:
            failures.append(f"{kind} bash tool call matched failed exec bridge record")
            continue
        return_code = entry.get("return_code")
        if kind == "success" and return_code != 0:
            failures.append(f"successful bash tool call matched exec return code {return_code!r}")
        elif kind == "command_nonzero" and (not isinstance(return_code, int) or return_code <= 0):
            failures.append(f"command_nonzero bash tool call matched exec return code {return_code!r}")
        elif kind == "command_timeout" and return_code != -1:
            failures.append(f"command_timeout bash tool call matched exec return code {return_code!r}")
    if exec_index < len(exec_entries):
        failures.append("bridge has unaccounted exec operation")

    index = 0
    expected = {"read": ("read_file", "read_dir"), "write": ("write_file",), "edit": ("write_file",)}
    for call in calls:
        name = call.get("name")
        if name not in expected:
            continue
        if call.get("is_error"):
            continue
        wanted = expected[name]
        path = _path(call.get("arguments") or {})
        found = False
        resume = index
        while index < len(ledger):
            entry = ledger[index]
            index += 1
            if entry.get("op") not in wanted:
                continue
            if path is None or _same_file(entry.get("path"), path):
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
                 provider_evidence_file_env: str = "STELLA_EVAL_PROVIDER_EVIDENCE_FILE",
                 deadline_margin_sec: int = 15, eval_agent_bin: str | None = None,
                 binding_dir: str | None = None, excluded_tools: str | None = None,
                 code_tool_surface: str | None = None,
                 **kwargs: Any) -> None:
        super().__init__(logs_dir, *args, **kwargs)
        self.stella_url = stella_url or os.environ.get("STELLA_URL", "")
        self.admin_token_env = admin_token_env
        self.provider_evidence_file_env = provider_evidence_file_env
        self.stella_model = model or self.model_name or os.environ.get("STELLA_EVAL_MODEL", "")
        self.deadline_margin_sec = deadline_margin_sec
        self.stop_confirm_sec = self.STOP_CONFIRM_SEC
        self.eval_agent_bin = eval_agent_bin or os.environ.get("STELLA_EVAL_AGENT_BIN", "stella-eval-agent")
        self.binding_dir = binding_dir or os.environ.get("STELLA_EVAL_BRIDGE_DIR", "")
        self.excluded_tools = excluded_tools if excluded_tools is not None else os.environ.get("STELLA_EVAL_EXCLUDED_TOOLS", "")
        # The server reads this from the same process environment the loop
        # exported before starting the testbed, so recording it here states the
        # run's tool surface. Unset means the production default, Hot.
        self.code_tool_surface = code_tool_surface or os.environ.get("STELLA_EVAL_CODE_TOOL_SURFACE", "") or "hot"
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
                   "--deadline-seconds", str(deadline), "--stop-confirm-seconds", str(confirm), "--bundle-digest", self.bundle_digest, "--output", str(result_path),
                   "--trajectory", str(trial_dir / "trajectory.json")]
        if self.excluded_tools:
            command.extend(["--excluded-tools", self.excluded_tools])
        command.extend(["--code-tool-surface", self.code_tool_surface])
        child_env = host_child_environment(os.environ, self.admin_token_env, self.provider_evidence_file_env)
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
        if not isinstance(result.get("metrics"), dict):
            result["metrics"] = {}
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
