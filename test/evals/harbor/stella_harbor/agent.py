"""Harbor adapter for a single Stella evaluation trial."""

from __future__ import annotations

import asyncio
import json
import os
import subprocess
from pathlib import Path
from typing import Any

from harbor.agents.installed.base import BaseInstalledAgent
from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext

from .bridge import BridgeServer

EXIT_ADAPTER = 10
EXIT_PRODUCT = 11
EXIT_TIMEOUT = 12


def _ledger(path: Path) -> list[dict[str, Any]]:
    if not path.exists():
        return []
    return [json.loads(line) for line in path.read_text().splitlines() if line.strip()]


def _path(arguments: dict[str, Any]) -> str | None:
    for name in ("path", "file_path", "filePath"):
        value = arguments.get(name)
        if isinstance(value, str):
            return value
    return None


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
    index = 0
    expected = {"bash": ("exec",), "read": ("read_file", "read_dir"), "write": ("write_file",), "edit": ("write_file",)}
    for call in calls:
        name = call.get("name")
        if name not in expected:
            continue
        wanted = expected[name]
        path = _path(call.get("arguments") or {})
        found = False
        while index < len(ledger):
            entry = ledger[index]
            index += 1
            if entry.get("op") not in wanted:
                continue
            if name == "bash" or path is None or entry.get("path") == path:
                found = True
                break
        if not found:
            failures.append(f"{name} tool call has no matching bridge ledger entry")
    return failures


class StellaAgent(BaseInstalledAgent):
    """Run Stella on the host while its core tools execute in Harbor's container."""

    def __init__(self, logs_dir: Path, *args: Any, stella_url: str | None = None,
                 admin_token_env: str = "STELLA_EVAL_ADMIN_TOKEN", model: str | None = None,
                 deadline_margin_sec: int = 15, eval_agent_bin: str | None = None,
                 binding_dir: str | None = None, **kwargs: Any) -> None:
        super().__init__(logs_dir, *args, **kwargs)
        self.stella_url = stella_url or os.environ.get("STELLA_URL", "")
        self.admin_token_env = admin_token_env
        self.stella_model = model or self.model_name or os.environ.get("STELLA_EVAL_MODEL", "")
        self.deadline_margin_sec = deadline_margin_sec
        self.eval_agent_bin = eval_agent_bin or os.environ.get("STELLA_EVAL_AGENT_BIN", "stella-eval-agent")
        self.binding_dir = binding_dir or os.environ.get("STELLA_EVAL_BRIDGE_DIR", "")
        self.bundle_digest = ""

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
        deadline = max(1, int(os.environ.get("HARBOR_AGENT_TIMEOUT_SEC", "900")) - self.deadline_margin_sec)
        server = BridgeServer(environment, workdir, trial_dir / "bridge.sock", trial_dir / "bridge-ledger.jsonl", tool_path_prepend="/installed-agent/stella/bin")
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
                   "--deadline-seconds", str(deadline), "--bundle-digest", self.bundle_digest, "--output", str(result_path)]
        child_env = os.environ.copy()
        if token := os.environ.get(self.admin_token_env):
            # The Go process has one fixed secret name, so its env-read surface
            # remains auditable while Harbor callers can choose their injection key.
            child_env["STELLA_EVAL_ADMIN_TOKEN"] = token
        try:
            proc = await asyncio.create_subprocess_exec(*command, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE, env=child_env)
            stdout, stderr = await proc.communicate()
        finally:
            await server.close()
        if not result_path.exists():
            raise RuntimeError(f"stella-eval-agent did not write result (stderr: {stderr.decode(errors='replace')[-1000:]})")
        result = json.loads(result_path.read_text())
        ledger = _ledger(trial_dir / "bridge-ledger.jsonl")
        violations = verify_evidence(result, ledger, binding.nonce)
        result["bridge_ledger"] = ledger
        result["valid"] = not violations
        result["predicate_violations"] = violations
        result_path.write_text(json.dumps(result, indent=2) + "\n")
        context.n_input_tokens = result.get("token_count")
        context.metadata = {"stella_result": result, "stella_exit_code": proc.returncode, "stella_stdout": stdout.decode(errors="replace")[-1000:]}
        if proc.returncode == EXIT_ADAPTER or violations:
            raise RuntimeError("Stella adapter evidence failure: " + "; ".join(violations or result.get("errors", [])))
        # Harbor should still run its verifier for product errors and timeouts.
        if proc.returncode not in (0, EXIT_PRODUCT, EXIT_TIMEOUT):
            raise RuntimeError(f"stella-eval-agent exited {proc.returncode}: {stderr.decode(errors='replace')[-1000:]}")
