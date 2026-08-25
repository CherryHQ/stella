"""Harbor adapter for a single Stella evaluation trial."""

from __future__ import annotations

import asyncio
import contextlib
import datetime
import json
import tempfile
import os
import stat
import subprocess
import time
import urllib.request
from dataclasses import dataclass
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


ADAPTER_PHASES = frozenset({"run_start", "setup_return", "child_spawn", "watchdog_fire", "term", "kill", "reap", "result_seen"})
DRIVER_PHASES = frozenset({
    "driver_start", "stream_start", "stream_return", "timeout_start", "stop_start", "stop_return",
    "surface_start", "surface_return", "admission_start", "admission_return", "verification_start",
    "verification_return", "evidence_start", "evidence_return", "cleanup_start", "cleanup_return", "result_defer_start", "result_write_start",
    "result_write_return", "driver_exit",
})


class PhaseJournal:
    """Append fixed crash breadcrumbs without accepting an unsafe artifact path."""

    def __init__(self, path: Path, phases: frozenset[str]) -> None:
        self.path = path
        self.phases = phases
        self.fd: int | None = None
        self.failed = False
        try:
            try:
                existing = os.lstat(path)
            except FileNotFoundError:
                existing = None
            if existing is not None:
                raise OSError("journal exists")
            self.fd = os.open(path, os.O_WRONLY | os.O_APPEND | os.O_CREAT | os.O_EXCL, 0o600)
            info = os.fstat(self.fd)
            if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o600:
                raise OSError("unsafe journal")
        except OSError:
            self.failed = True
            if self.fd is not None:
                os.close(self.fd)
                self.fd = None

    def append(self, phase: str) -> None:
        if phase not in self.phases or self.fd is None:
            self.failed = True
            return
        entry = json.dumps({"version": 1, "phase": phase,
                            "timestamp": datetime.datetime.now(datetime.UTC).isoformat().replace("+00:00", "Z")},
                           separators=(",", ":")).encode() + b"\n"
        try:
            written = 0
            while written < len(entry):
                count = os.write(self.fd, entry[written:])
                if count <= 0:
                    raise OSError("short write")
                written += count
            os.fsync(self.fd)
        except OSError:
            self.failed = True

    def close(self) -> None:
        if self.fd is not None:
            with contextlib.suppress(OSError):
                os.close(self.fd)
            self.fd = None


def read_phase_journal(path: Path, phases: frozenset[str]) -> list[dict[str, Any]] | None:
    """Return only the fixed journal schema, never an unsafe file or its error."""
    try:
        before = os.lstat(path)
        if stat.S_ISLNK(before.st_mode) or not stat.S_ISREG(before.st_mode) or stat.S_IMODE(before.st_mode) != 0o600:
            return None
        with path.open("rb") as source:
            after = os.fstat(source.fileno())
            if (before.st_dev, before.st_ino) != (after.st_dev, after.st_ino):
                return None
            lines = source.read().splitlines()
    except OSError:
        return None
    entries: list[dict[str, Any]] = []
    for line in lines:
        try:
            entry = json.loads(line)
            if set(entry) != {"version", "phase", "timestamp"} or entry["version"] != 1 or entry["phase"] not in phases:
                return None
            timestamp = entry["timestamp"]
            if not isinstance(timestamp, str) or not timestamp.endswith("Z"):
                return None
            datetime.datetime.fromisoformat(timestamp.removesuffix("Z") + "+00:00")
        except (TypeError, ValueError, json.JSONDecodeError):
            return None
        entries.append(entry)
    return entries or None


async def kill_child(proc: asyncio.subprocess.Process, journal: PhaseJournal | None = None,
                     *, reap_deadline: float | None = None) -> None:
    """Directly SIGKILL, then reap before cleanup can consume its wall."""
    if journal is not None:
        journal.append("kill")
    proc.kill()
    if reap_deadline is None:
        with contextlib.suppress(asyncio.TimeoutError):
            await asyncio.wait_for(proc.wait(), timeout=0.1)
    else:
        await await_finalization_within(reap_deadline, proc.wait())
    if proc.returncode is None:
        raise TimeoutError("child did not reap before fallback recovery")
    if journal is not None:
        journal.append("reap")


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


class PreSpawnDeadlineError(RuntimeError):
    """The remaining Harbor wall cannot safely start an eval child."""


@dataclass(frozen=True)
class ParentDeadlines:
    """One absolute wall split across the parent and the eval child."""

    work_deadline: float
    go_finalize_by: float
    child_exit_deadline: float
    recovery_deadline: float
    go_finalize_by_unix_ms: int


def derive_parent_deadlines(*, outer_deadline: float, outer_deadline_unix_ms: int,
                            agent_timeout_sec: int, go_finalization_budget_sec: int,
                            now: float) -> ParentDeadlines:
    """Reserve child exit and lease recovery before spawning the child.

    The post-Go portion scales for short seams but tops out at three minutes for
    production. Its 1:2 split gives the child a result-and-exit reserve, then
    leaves the rest for a killed child's fallback cleanup. Raise the ceiling
    only when public-API cleanup measurements exceed it.
    """
    post_go_reserve_sec = min(
        StellaAgent.PARENT_POST_GO_RESERVE_CEILING_SEC,
        agent_timeout_sec * StellaAgent.PARENT_POST_GO_RESERVE_RATIO,
    )
    child_exit_reserve_sec = post_go_reserve_sec / 3
    go_finalize_by = outer_deadline - post_go_reserve_sec
    child_exit_deadline = go_finalize_by + child_exit_reserve_sec
    work_deadline = go_finalize_by - go_finalization_budget_sec
    if not (now < work_deadline < go_finalize_by < child_exit_deadline < outer_deadline):
        raise PreSpawnDeadlineError("remaining Harbor wall cannot fit working, finalization, exit, and recovery deadlines")
    return ParentDeadlines(
        work_deadline=work_deadline,
        go_finalize_by=go_finalize_by,
        child_exit_deadline=child_exit_deadline,
        recovery_deadline=outer_deadline,
        go_finalize_by_unix_ms=outer_deadline_unix_ms - round(post_go_reserve_sec * 1000),
    )


class StellaAgent(BaseInstalledAgent):
    """Run Stella on the host while its core tools execute in Harbor's container."""

    def __init__(self, logs_dir: Path, *args: Any, stella_url: str | None = None,
                 admin_token_env: str = "STELLA_EVAL_ADMIN_TOKEN", model: str | None = None,
                 eval_agent_bin: str | None = None,
                 binding_dir: str | None = None, excluded_tools: str | None = None,
                 **kwargs: Any) -> None:
        super().__init__(logs_dir, *args, **kwargs)
        self.stella_url = stella_url or os.environ.get("STELLA_URL", "")
        self.admin_token_env = admin_token_env
        self.stella_model = model or self.model_name or os.environ.get("STELLA_EVAL_MODEL", "")
        self.stop_confirm_sec = self.STOP_CONFIRM_SEC
        self.eval_agent_bin = eval_agent_bin or os.environ.get("STELLA_EVAL_AGENT_BIN", "stella-eval-agent")
        self.binding_dir = binding_dir or os.environ.get("STELLA_EVAL_BRIDGE_DIR", "")
        self.excluded_tools = excluded_tools if excluded_tools is not None else os.environ.get("STELLA_EVAL_EXCLUDED_TOOLS", "")
        self.fixture_config = os.environ.get("STELLA_EVAL_MCP_FIXTURE_CONFIG", "")
        self.bundle_digest = ""

    # The Go driver owns this full finalization wall after work ends. A shorter
    # seam may override it, but production needs the same 180s ceiling as Go.
    STOP_CONFIRM_SEC = 180
    # Post-Go parent reserve: at a 15s seam this is 6s, split into 2s for a
    # child result/exit and 4s for kill/reap plus fallback lease recovery. The
    # 180s ceiling keeps a 900s production wall from sacrificing more work.
    PARENT_POST_GO_RESERVE_RATIO = 0.4
    PARENT_POST_GO_RESERVE_CEILING_SEC = 180

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
        loop = asyncio.get_running_loop()
        outer_deadline = loop.time() + agent_timeout_sec
        outer_deadline_unix_ms = time.time_ns() // 1_000_000 + agent_timeout_sec * 1000
        if not self.stella_url or not self.stella_model or not self.binding_dir:
            raise RuntimeError("Stella adapter needs stella_url, model, and STELLA_EVAL_BRIDGE_DIR")
        trial = str(self.context_id or self.session_id or "harbor-trial")
        trial_dir = self.logs_dir / "stella"
        trial_dir.mkdir(parents=True, exist_ok=True)
        adapter_journal = PhaseJournal(trial_dir / "adapter-phase-journal.jsonl", ADAPTER_PHASES)
        adapter_journal.append("run_start")
        driver_journal_path = trial_dir / "driver-phase-journal.jsonl"
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
                   "--finalize-by-unix-ms", "0", "--finalization-budget-seconds", str(self.stop_confirm_sec), "--bundle-digest", self.bundle_digest, "--output", str(result_path),
                   "--trajectory", str(trial_dir / "trajectory.json"), "--phase-journal", str(driver_journal_path)]
        if self.excluded_tools:
            command.extend(["--excluded-tools", self.excluded_tools])
        if self.fixture_config:
            command.extend(["--mcp-fixture-config", self.fixture_config, "--cleanup-state", str(cleanup_state)])
        child_env = os.environ.copy()
        if token := os.environ.get(self.admin_token_env):
            # The Go process has one fixed secret name, so its env-read surface
            # remains auditable while Harbor callers can choose their injection key.
            child_env["STELLA_EVAL_ADMIN_TOKEN"] = token
        # Compute every remaining stage after setup. Go gets an absolute UTC
        # cutoff; the parent keeps a later child-exit watchdog and an independent
        # recovery deadline, so fallback cleanup never inherits an expired Go
        # finalization context.
        try:
            deadlines = derive_parent_deadlines(
                outer_deadline=outer_deadline,
                outer_deadline_unix_ms=outer_deadline_unix_ms,
                agent_timeout_sec=agent_timeout_sec,
                go_finalization_budget_sec=self.stop_confirm_sec,
                now=loop.time(),
            )
        except PreSpawnDeadlineError:
            adapter_journal.close()
            await server.close()
            raise
        server.set_deadline(deadlines.work_deadline)
        command[command.index("--finalize-by-unix-ms") + 1] = str(deadlines.go_finalize_by_unix_ms)
        adapter_journal.append("setup_return")
        proc = await asyncio.create_subprocess_exec(*command, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE, env=child_env)
        adapter_journal.append("child_spawn")
        cleanup_failure = False
        watchdog_error: RuntimeError | None = None
        cancelled = False
        stdout = b""
        stderr = b""
        try:
            # First Go finalizes by its UTC cutoff, then the parent grants only
            # the reserved result-and-exit interval before direct SIGKILL.
            stdout, stderr = await asyncio.wait_for(
                proc.communicate(), max(0.0, deadlines.child_exit_deadline - loop.time())
            )
        except asyncio.TimeoutError:
            adapter_journal.append("watchdog_fire")
            try:
                await kill_child(proc, adapter_journal, reap_deadline=deadlines.recovery_deadline)
            except TimeoutError:
                pass
            watchdog_error = RuntimeError("stella-eval-agent exceeded its absolute parent watchdog")
        except asyncio.CancelledError:
            cancelled = True
            try:
                await asyncio.shield(asyncio.wait_for(
                    proc.communicate(), max(0.0, deadlines.child_exit_deadline - loop.time())
                ))
            except asyncio.TimeoutError:
                adapter_journal.append("watchdog_fire")
                with contextlib.suppress(TimeoutError):
                    await asyncio.shield(kill_child(
                        proc, adapter_journal, reap_deadline=deadlines.recovery_deadline
                    ))
        finally:
            # Bridge closure must not steal the fallback lease's wall. It runs
            # concurrently, while recovery is ordered after child reap and uses
            # its own later absolute deadline.
            closing = asyncio.ensure_future(server.close())
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
                    if driver_result is not None and driver_result.get("fixture_lease_released") is True:
                        recovery = {"outcome": "released"}
                    else:
                        recovery = await await_finalization_within(deadlines.recovery_deadline, finalize_fixture_cleanup(
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
            remaining = max(0.0, deadlines.recovery_deadline - loop.time())
            if remaining > 0:
                await asyncio.wait([closing], timeout=remaining)
            if not closing.done():
                closing.cancel()
        if cancelled:
            adapter_journal.close()
            raise asyncio.CancelledError
        if watchdog_error is not None:
            adapter_journal.close()
            raise watchdog_error
        if cleanup_failure:
            # The adapter result is still written below, retaining the driver's
            # original product outcome alongside this independent harness fault.
            cleanup_violation = "fixture cleanup recovery failed (harness invalid)"
        else:
            cleanup_violation = None
        if not result_path.exists():
            adapter_journal.close()
            raise RuntimeError(f"stella-eval-agent did not write result (stderr: {stderr.decode(errors='replace')[-1000:]})")
        result = json.loads(result_path.read_text())
        adapter_journal.append("result_seen")
        adapter_journal.close()
        adapter_phases = read_phase_journal(trial_dir / "adapter-phase-journal.jsonl", ADAPTER_PHASES)
        driver_phases = read_phase_journal(driver_journal_path, DRIVER_PHASES)
        ledger = _ledger(trial_dir / "bridge-ledger.jsonl")
        violations = verify_evidence(result, ledger, binding.nonce)
        if adapter_journal.failed or adapter_phases is None or driver_phases is None or result.get("phase_journal_ok") is not True:
            violations.append("phase journal unavailable")
        else:
            # Diagnostic-only: these entries neither alter host verdicts nor
            # supply a reward. They locate a killed child without collecting IO.
            result["phase_journal"] = {"adapter": adapter_phases, "driver": driver_phases}
        if cleanup_violation is not None:
            violations.append(cleanup_violation)
        if proc.returncode == EXIT_ADAPTER:
            violations.append("stella-eval-agent reported adapter-invalid")
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
