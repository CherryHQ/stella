"""Harbor adapter for a single Stella evaluation trial."""

from __future__ import annotations

import asyncio
import contextlib
import json
import tempfile
import os
import subprocess
import urllib.request
from pathlib import Path
from typing import Any

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

from harbor.agents.installed.base import BaseInstalledAgent
from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext

from .bridge import BridgeServer
from .runtime_identity import (
    FIXTURE_SPEC_DIGEST,
    NO_FIXTURE_PLAN_DIGEST,
    gateway_endpoint,
    price_digest,
    prices_from_env,
)

EXIT_ADAPTER = 10
EXIT_PRODUCT = 11
EXIT_TIMEOUT = 12


# Bridge error codes that mean the harness broke, not the agent misbehaved.
ADAPTER_FAULT_CODES = {"internal", "bad_nonce", "bad_request"}


async def await_finalization_within(deadline: float, operation: Any) -> Any:
    """Await one coordinator operation without extending the child's wall."""
    remaining = deadline - asyncio.get_running_loop().time()
    if remaining <= 0:
        close = getattr(operation, "close", None)
        if close is not None:
            close()
        raise TimeoutError("timeout finalization wall exhausted")
    return await asyncio.wait_for(operation, timeout=remaining)


async def terminate_child(proc: asyncio.subprocess.Process, reap_sec: int) -> bool:
    """TERM a stuck evaluator, never spending the reap budget twice."""
    loop = asyncio.get_running_loop()
    deadline = loop.time() + reap_sec
    proc.terminate()
    try:
        # Reserve half the margin for the SIGKILL reap before TERM can spend it.
        await asyncio.wait_for(proc.wait(), max(0.0, reap_sec / 2))
        return False
    except asyncio.TimeoutError:
        proc.kill()
        remaining = max(0.0, deadline - loop.time())
        if remaining > 0:
            with contextlib.suppress(asyncio.TimeoutError):
                await asyncio.wait_for(proc.wait(), remaining)
        else:
            await asyncio.sleep(0)
        return True


VERDICT_DIR = "/root/.stella-harbor-verdict"


async def publish_signed_verdict(environment: BaseEnvironment, verdict: dict[str, Any], trial: str) -> None:
    """Publish a post-exit signed envelope only root verifier can read.

    Agent containers run as `agent`; this directory is created after their host
    process exits and is root-owned 0700. The public key is intentionally not
    available during the model turn, so a background watcher cannot replace the
    verifier material with its own key.
    """
    required = ("version", "task_id", "valid", "reward", "nonce")
    if any(key not in verdict for key in required):
        raise RuntimeError("host verdict is incomplete")
    payload = {
        "version": verdict["version"], "task_id": verdict["task_id"], "trial": trial,
        "valid": verdict["valid"], "reward": verdict["reward"],
        "reasons": verdict.get("reasons", []), "nonce": verdict["nonce"],
    }
    payload_bytes = json.dumps(payload, sort_keys=True, separators=(",", ":")).encode()
    key = Ed25519PrivateKey.generate()
    signature = key.sign(payload_bytes)
    public = key.public_key().public_bytes(serialization.Encoding.PEM, serialization.PublicFormat.SubjectPublicKeyInfo)
    setup = await environment.exec(f"rm -rf {VERDICT_DIR} && install -d -m 700 -o root -g root {VERDICT_DIR}", user="root")
    if setup.return_code != 0:
        raise RuntimeError("cannot establish root-only verdict directory")
    with tempfile.TemporaryDirectory(prefix="stella-verdict-") as td:
        root = Path(td)
        (root / "payload.json").write_bytes(payload_bytes)
        (root / "signature.bin").write_bytes(signature)
        (root / "public.pem").write_bytes(public)
        for name in ("payload.json", "signature.bin", "public.pem"):
            await environment.upload_file(root / name, f"{VERDICT_DIR}/{name}")
    sealed = await environment.exec(
        f"chown root:root {VERDICT_DIR}/* && chmod 700 {VERDICT_DIR} && chmod 600 {VERDICT_DIR}/* && "
        f"test \"$(id -u)\" = 0 && test \"$(stat -c '%u %a' {VERDICT_DIR})\" = '0 700'",
        user="root",
    )
    if sealed.return_code != 0:
        raise RuntimeError("root-only verdict directory could not be verified")


async def cleanup_fixture_lease(config_path: str, state_path: Path, *, action: str = "cleanup") -> None:
    """Ask the testbed-owned cleanup server to consume one opaque lease.

    Neither artifact contains the provisioned user's token. The lease retains
    it until the coordinator has separately completed admin deactivation.
    """
    config = json.loads(Path(config_path).read_text())
    state = json.loads(state_path.read_text())
    socket = config.get("cleanup_socket")
    lease = state.get("lease")
    if not isinstance(socket, str) or not isinstance(lease, str) or not lease:
        raise RuntimeError("fixture cleanup state is invalid")
    reader, writer = await asyncio.open_unix_connection(socket)
    try:
        writer.write(json.dumps({"action": action, "lease": lease}).encode() + b"\n")
        await writer.drain()
        response = json.loads((await reader.readline()).decode())
        if response.get("error"):
            raise RuntimeError("fixture cleanup rejected")
    finally:
        writer.close()
        await writer.wait_closed()


async def deactivate_fixture_user(state_path: Path, stella_url: str, admin_token: str) -> None:
    """Deactivate only after the retryable user-PAT cleanup has completed."""
    state = json.loads(state_path.read_text())
    provisioned_user_id = state.get("provisioned_user_id")
    if not isinstance(provisioned_user_id, str) or not provisioned_user_id or not admin_token:
        raise RuntimeError("fixture cleanup provisioning state is invalid")
    request = urllib.request.Request(
        stella_url.rstrip("/") + "/api/provisioned-users/" + provisioned_user_id + "/deactivate",
        method="POST", headers={"Authorization": "Bearer " + admin_token}, data=b"",
    )

    def deactivate() -> None:
        with urllib.request.urlopen(request, timeout=15) as response:
            if response.status not in (200, 204):
                raise RuntimeError("fixture cleanup deactivation rejected")

    await asyncio.to_thread(deactivate)


def cleanup_is_complete(result: dict[str, Any]) -> bool:
    """Whether the driver completed the three phases a retained lease covers."""
    phases = result.get("cleanup")
    if not isinstance(phases, list):
        return False
    outcomes: dict[str, str] = {}
    for phase in phases:
        if not isinstance(phase, dict):
            return False
        name, outcome = phase.get("phase"), phase.get("outcome")
        if not isinstance(name, str) or not isinstance(outcome, str) or name in outcomes:
            return False
        outcomes[name] = outcome
    return all(outcomes.get(phase) == "completed" for phase in (
        "mcp_registration", "agent", "provisioned_user",
    ))


async def finalize_fixture_cleanup(config_path: str, state_path: Path, stella_url: str,
                                   admin_token: str, returncode: int | None,
                                   result: dict[str, Any] | None) -> dict[str, str]:
    """Consume the fallback lease only after the driver's typed outcome is known."""
    if result is not None and result.get("fixture_lease_released") is True:
        return {"outcome": "released"}
    needs_retry = returncode is None or returncode < 0 or result is None or not cleanup_is_complete(result)
    if needs_retry:
        # The retained PAT is still live while cleanup runs. Deactivation revokes
        # it, so release follows only after both user-scoped cleanup and admin
        # deactivation succeed. Any failure leaves the lease retryable.
        await cleanup_fixture_lease(config_path, state_path)
        await deactivate_fixture_user(state_path, stella_url, admin_token)
        await cleanup_fixture_lease(config_path, state_path, action="release")
        return {"outcome": "recovered"}
    await cleanup_fixture_lease(config_path, state_path, action="release")
    return {"outcome": "released"}


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
        if entry.get("verifier") is True:
            continue
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
    if not calls and any(entry.get("op") in tool_ops and entry.get("verifier") is not True for entry in ledger):
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


def child_budget(remaining: float, reap: float, finalization: int) -> tuple[int, int]:
    """Return integer child flags that fit before the absolute reap reserve."""
    wall = int(remaining - reap)
    if wall < 2:
        raise RuntimeError("stella-eval-agent pre-spawn setup exhausted Harbor wall")
    return split_trial_budget(wall, 0, finalization)


def split_trial_budget(limit: int, margin: int, finalization: int) -> tuple[int, int]:
    """Divide Harbor's trial limit into working time and timeout finalization.

    Harbor kills the trial at `limit`, so work, terminal confirmation, runtime
    admission, evidence export, and cleanup all fit inside one number. The
    margin covers process spawn and exit. A limit too small to hold the requested
    finalization shrinks it rather than starving the work: a quarter of the wall
    is the floor either side can rely on.
    """
    wall = max(1, limit - margin)
    finalization = max(1, min(finalization, wall // 4))
    return max(1, wall - finalization), finalization


class StellaAgent(BaseInstalledAgent):
    """Run Stella on the host while its core tools execute in Harbor's container."""

    def __init__(self, logs_dir: Path, *args: Any, stella_url: str | None = None,
                 admin_token_env: str = "STELLA_EVAL_ADMIN_TOKEN", model: str | None = None,
                 deadline_margin_sec: int = 15, eval_agent_bin: str | None = None,
                 binding_dir: str | None = None, excluded_tools: str | None = None,
                 **kwargs: Any) -> None:
        super().__init__(logs_dir, *args, **kwargs)
        self.stella_url = stella_url or os.environ.get("STELLA_URL", "")
        self.admin_token_env = admin_token_env
        self.stella_model = model or self.model_name or os.environ.get("STELLA_EVAL_MODEL", "")
        self.deadline_margin_sec = deadline_margin_sec
        self.stop_confirm_sec = self.STOP_CONFIRM_SEC
        self.eval_agent_bin = eval_agent_bin or os.environ.get("STELLA_EVAL_AGENT_BIN", "stella-eval-agent")
        self.binding_dir = binding_dir or os.environ.get("STELLA_EVAL_BRIDGE_DIR", "")
        self.excluded_tools = excluded_tools if excluded_tools is not None else os.environ.get("STELLA_EVAL_EXCLUDED_TOOLS", "")
        self.fixture_config = os.environ.get("STELLA_EVAL_MCP_FIXTURE_CONFIG", "")
        self.bundle_digest = ""

    # Closing the bridge consumes only what remains of the finalization wall.
    # A wedged child gets TERM→SIGKILL inside the caller-provided margin, never
    # after Harbor's agent timeout.
    CLOSE_BUDGET_SEC = 20
    # One wall after the working deadline for terminal confirmation, admitted
    # surface collection, evidence, and cleanup. Commands are clamped to the
    # working deadline, so this excludes the longest tool call.
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
        agent_timeout_sec = int(os.environ.get("HARBOR_AGENT_TIMEOUT_SEC", "900"))
        outer_deadline = asyncio.get_running_loop().time() + agent_timeout_sec
        if not self.stella_url or not self.stella_model or not self.binding_dir:
            raise RuntimeError("Stella adapter needs stella_url, model, and STELLA_EVAL_BRIDGE_DIR")
        trial = str(self.context_id or self.session_id or "harbor-trial")
        trial_dir = self.logs_dir / "stella"
        trial_dir.mkdir(parents=True, exist_ok=True)
        workdir_result = await await_finalization_within(outer_deadline, environment.exec("pwd"))
        if workdir_result.return_code != 0:
            raise RuntimeError(f"discover task workdir: {workdir_result.stderr}")
        workdir = (workdir_result.stdout or "").strip() or "/"
        # One wall clock, and the arithmetic that divides it lives here. Harbor
        # kills the trial at HARBOR_AGENT_TIMEOUT_SEC, so working time, the stop
        # confirmation that follows it, and the evidence export all have to fit
        # inside that number. The margin covers process spawn and exit only.
        # Every command the agent runs is clamped to `deadline` too, so nothing
        # is still executing when the confirmation starts.
        # Bridge setup is pre-spawn work, not agent working time. Its final
        # cutoff is assigned immediately before spawn from outer remaining time.
        server = BridgeServer(environment, workdir, trial_dir / "bridge.sock", trial_dir / "bridge-ledger.jsonl", tool_path_prepend="/installed-agent/stella/bin")
        binding = await await_finalization_within(outer_deadline, server.start())
        result_path = trial_dir / "result.json"
        cleanup_state = trial_dir / "cleanup-state.json"
        template_path = trial_dir / "binding-template.json"
        template_path.write_text(json.dumps(binding.__dict__))
        instruction_path = trial_dir / "instruction.txt"
        instruction_path.write_text(instruction)
        bundle_digest = (trial_dir / "bundle.sha256")
        bundle_digest.write_text("")
        # Harbor's environment_name is the task's immutable local identity. The
        # host driver refuses an unknown name instead of letting one task's
        # verifier or MCP fixture leak into another task's turn.
        command = [self.eval_agent_bin, "--stella-url", self.stella_url, "--instruction-file", str(instruction_path), "--binding-template", str(template_path),
                   "--binding-dir", self.binding_dir, "--model", self.stella_model, "--user-id", trial, "--task-id", environment.environment_name,
                   "--deadline-seconds", "0", "--stop-confirm-seconds", "0", "--bundle-digest", self.bundle_digest, "--output", str(result_path),
                   "--trajectory", str(trial_dir / "trajectory.json")]
        if self.excluded_tools:
            command.extend(["--excluded-tools", self.excluded_tools])
        if self.fixture_config:
            command.extend(["--mcp-fixture-config", self.fixture_config, "--cleanup-state", str(cleanup_state)])
        child_env = os.environ.copy()
        if token := os.environ.get(self.admin_token_env):
            # The Go process has one fixed secret name, so its env-read surface
            # remains auditable while Harbor callers can choose their injection key.
            child_env["STELLA_EVAL_ADMIN_TOKEN"] = token
        now = asyncio.get_running_loop().time()
        reap_sec = max(0.0, min(float(self.deadline_margin_sec), outer_deadline - now))
        deadline, finalization = child_budget(outer_deadline - now, reap_sec, self.stop_confirm_sec)
        server.set_deadline(asyncio.get_running_loop().time() + deadline)
        command[command.index("--deadline-seconds") + 1] = str(deadline)
        command[command.index("--stop-confirm-seconds") + 1] = str(finalization)
        proc = await asyncio.create_subprocess_exec(*command, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE, env=child_env)
        # The fallback fixture coordinator shares the child's finalization wall.
        trial_wall_deadline = outer_deadline
        cleanup_failure = False
        try:
            # Do not trust one child to enforce Harbor's wall clock. Go must
            # finish and write its typed result inside deadline+finalization;
            # this parent watchdog is only the independent TERM→SIGKILL
            # backstop when an HTTP transport leaves the child wedged.
            stdout, stderr = await asyncio.wait_for(
                proc.communicate(), max(0.0, outer_deadline - asyncio.get_running_loop().time() - reap_sec)
            )
        except asyncio.TimeoutError as exc:
            await terminate_child(proc, reap_sec)
            raise RuntimeError("stella-eval-agent exceeded its bounded trial wall") from exc
        except asyncio.CancelledError:
            # Give the host driver its bounded public-API cleanup path a chance
            # to run first. SIGKILL is only the escalation: it skips Go defers
            # and otherwise leaks a user-scoped MCP registration until the
            # whole testbed dies.
            await terminate_child(proc, reap_sec)
            raise
        finally:
            # An exec may still be in flight against a container that is gone or
            # wedged; waiting on it here is how one runaway command used to stall
            # the entire job. This close and fixture recovery consume only what
            # remains of the child's finalization wall.
            closing = asyncio.ensure_future(server.close())
            remaining = max(0.0, trial_wall_deadline - asyncio.get_running_loop().time())
            await asyncio.wait([closing], timeout=min(remaining, self.CLOSE_BUDGET_SEC))
            if not closing.done():
                closing.cancel()
            if self.fixture_config and cleanup_state.exists():
                # The result is the authority for ordinary cleanup. Do not
                # release its retry lease until its typed phases prove complete.
                driver_result = None
                if result_path.exists():
                    try:
                        parsed = json.loads(result_path.read_text())
                        driver_result = parsed if isinstance(parsed, dict) else None
                    except (OSError, json.JSONDecodeError):
                        driver_result = None
                try:
                    # Go may finish exactly at the wall after releasing the
                    # lease. This check is synchronous, so no zero-remaining
                    # coroutine timeout can turn that complete result invalid.
                    if driver_result is not None and driver_result.get("fixture_lease_released") is True:
                        recovery = {"outcome": "released"}
                    else:
                        recovery = await await_finalization_within(trial_wall_deadline, finalize_fixture_cleanup(
                            self.fixture_config, cleanup_state, self.stella_url,
                            os.environ.get(self.admin_token_env, ""), proc.returncode, driver_result,
                        ))
                    if driver_result is not None:
                        driver_result["cleanup_recovery"] = recovery
                        result_path.write_text(json.dumps(driver_result, indent=2) + "\n")
                except Exception:  # noqa: BLE001 - this is a harness-invalid trial.
                    cleanup_failure = True
                    if driver_result is not None:
                        driver_result["cleanup_recovery"] = {"outcome": "error"}
                        result_path.write_text(json.dumps(driver_result, indent=2) + "\n")
        if cleanup_failure:
            # The adapter result is still written below, retaining the driver's
            # original product outcome alongside this independent harness fault.
            cleanup_violation = "fixture cleanup recovery failed (harness invalid)"
        else:
            cleanup_violation = None
        if not result_path.exists():
            raise RuntimeError(f"stella-eval-agent did not write result (stderr: {stderr.decode(errors='replace')[-1000:]})")
        result = json.loads(result_path.read_text())
        ledger = _ledger(trial_dir / "bridge-ledger.jsonl")
        violations = verify_evidence(result, ledger, binding.nonce)
        if cleanup_violation is not None:
            violations.append(cleanup_violation)
        result.setdefault("metrics", {})["bridge"] = bridge_stats(ledger)
        result["bridge_ledger"] = ledger
        # These are runtime inputs, written into each trial result after the
        # child exits. The comparator never trusts a neighboring manifest for
        # them, because it may describe a different provider or wall budget.
        result.update({
            "price_digest": price_digest(prices_from_env()),
            "provider_type": os.environ.get("STELLA_EVAL_PROVIDER_TYPE"),
            "gateway_endpoint": gateway_endpoint(os.environ.get("OPENAI_BASE_URL", "")),
            "effective_agent_timeout_sec": agent_timeout_sec,
            "fixture_spec_digest": FIXTURE_SPEC_DIGEST,
        })
        result.setdefault("fixture_plan_digest", NO_FIXTURE_PLAN_DIGEST)
        result["valid"] = not violations
        result["predicate_violations"] = violations
        result_path.write_text(json.dumps(result, indent=2) + "\n")
        verdict = result.get("host_verdict")
        if isinstance(verdict, dict):
            await publish_signed_verdict(environment, verdict, trial)
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
