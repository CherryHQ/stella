"""Upstream Hermes on the same Responses gateway, with explicit model controls."""
from __future__ import annotations

import re
import shlex
import os
import json
import time
from pathlib import Path
from typing import Any, override

from harbor.agents.installed.base import with_prompt_template
from harbor.agents.installed.hermes import Hermes
from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext
import yaml


class HermesGateway(Hermes):
    def __init__(self, *args: Any, thinking: str = "", context_window: int | str = 0,
                 max_tokens: int | str = 0, **kwargs: Any) -> None:
        super().__init__(*args, **kwargs)
        self.thinking = thinking
        self.context_window = int(context_window)
        self.max_tokens = int(max_tokens)

    @staticmethod
    @override
    def name() -> str:
        return "hermes-gateway"

    @override
    def get_version_command(self) -> str:
        return 'export PATH="$HOME/.local/bin:$PATH"; hermes --version'

    @override
    async def install(self, environment: BaseEnvironment) -> None:
        # Use the release archive: raw.githubusercontent.com and git fetch can
        # be throttled independently of GitHub's archive endpoint.
        if not self._version or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", self._version):
            raise ValueError("Hermes requires a release tag in version")
        await self.ensure_system_dependencies(environment, ("curl", "git", "ripgrep", "xz"))
        url = f"https://codeload.github.com/NousResearch/hermes-agent/tar.gz/refs/tags/{self._version}"
        archive = os.environ.get("HERMES_RELEASE_ARCHIVE")
        if archive:
            await environment.upload_file(Path(archive), "/tmp/hermes-release.tar.gz")
        download = ("" if archive else
                    f"curl -fsSL --retry 3 --retry-delay 5 {shlex.quote(url)} -o /tmp/hermes-release.tar.gz; ")
        await self.exec_as_agent(environment, command=(
            "set -euo pipefail; "
            'install_dir="$HOME/.local/share/hermes-agent"; mkdir -p "$install_dir"; '
            f'{download}tar -xzf /tmp/hermes-release.tar.gz -C "$install_dir" --strip-components=1; '
            'export PATH="$HOME/.local/bin:$PATH"'
        ))
        # Separate calls preserve completed stages and identify the stalled one.
        # The installer otherwise buffers all output until the entire setup ends.
        stages = []
        try:
            for stage in ("prerequisites", "venv", "python-deps", "node-deps", "path", "config"):
                started = time.monotonic()
                record = {"stage": stage, "status": "running"}
                stages.append(record)
                (self.logs_dir / "install-stages.json").write_text(json.dumps(stages, indent=2))
                try:
                    await self.exec_as_agent(environment, command=(
                        'set -euo pipefail; export PATH="$HOME/.local/bin:$PATH"; '
                        'bash "$HOME/.local/share/hermes-agent/scripts/install.sh" '
                        '--dir "$HOME/.local/share/hermes-agent" '
                        f'--skip-setup --non-interactive --stage {stage} '
                        f'2>&1 | tee /logs/agent/install-{stage}.log'
                    ))
                    record["status"] = "completed"
                except BaseException:
                    record["status"] = "failed"
                    raise
                finally:
                    record["elapsed_sec"] = round(time.monotonic() - started, 3)
        finally:
            (self.logs_dir / "install-stages.json").write_text(json.dumps(stages, indent=2))

    def gateway_config(self, model: str, base_url: str) -> dict[str, Any]:
        config = yaml.safe_load(super()._build_config_yaml(model))
        # Session titles are UI metadata; their auxiliary model request does
        # not inherit the declared reasoning/output controls.
        config["auxiliary"] = {"title_generation": {"enabled": False}}
        extra: dict[str, Any] = {}
        if self.thinking:
            extra["reasoning"] = {"effort": self.thinking}
            config["agent"]["reasoning_effort"] = self.thinking
        if self.max_tokens:
            extra["max_output_tokens"] = self.max_tokens
            config["agent"]["max_tokens"] = self.max_tokens
        config.pop("provider", None)
        config["model"] = {"provider": "custom:eval_gateway", "default": model}
        if self.context_window:
            config["model"]["context_length"] = self.context_window
        config["providers"] = {"eval_gateway": {
            "api": base_url.rstrip("/"), "key_env": "OPENAI_API_KEY",
            "transport": "codex_responses", "extra_body": extra,
        }}
        return config

    @override
    @with_prompt_template
    async def run(self, instruction: str, environment: BaseEnvironment, context: AgentContext) -> None:
        if not self.model_name or "/" not in self.model_name:
            raise ValueError("model must be gateway/<model>")
        model = self.model_name.split("/", 1)[1]
        base_url, api_key = self._get_env("OPENAI_BASE_URL"), self._get_env("OPENAI_API_KEY")
        if not base_url or not api_key:
            raise ValueError("OPENAI_BASE_URL and OPENAI_API_KEY must be set")
        env = {"HERMES_HOME": "/tmp/hermes", "TERMINAL_ENV": "local",
               "OPENAI_API_KEY": api_key, "OPENAI_BASE_URL": base_url,
               "HARBOR_INSTRUCTION": instruction}
        # The key itself stays in the container environment.
        config = yaml.safe_dump(self.gateway_config(model, base_url))
        await self.exec_as_agent(environment, command=(
            "umask 077; mkdir -p /tmp/hermes; "
            f"printf %s {shlex.quote(config)} > /tmp/hermes/config.yaml"
        ), env=env, timeout_sec=10)
        for command in (self._build_register_mcp_servers_command(), self._build_register_skills_command()):
            if command:
                await self.exec_as_agent(environment, command=command, env=env, timeout_sec=10)
        command = ('set -o pipefail; export PATH="$HOME/.local/bin:$PATH"; '
                   'hermes --yolo chat -q "$HARBOR_INSTRUCTION" -Q '
                   f'--provider custom:eval_gateway --model {shlex.quote(model)} '
                   '2>&1 | stdbuf -oL tee /logs/agent/hermes.txt')
        if toolsets := self._resolved_flags.get("toolsets"):
            command = command.replace('2>&1 |', f'--toolsets {shlex.quote(str(toolsets))} 2>&1 |')
        try:
            await self.exec_as_agent(environment, command=command, env=env)
        finally:
            try:
                await self.exec_as_agent(environment, command=(
                    'export PATH="$HOME/.local/bin:$PATH"; '
                    'hermes sessions export /logs/agent/hermes-session.jsonl --source cli'
                ), env={"HERMES_HOME": "/tmp/hermes"}, timeout_sec=30)
            except Exception:
                # Preserve the run's original outcome if diagnostic export fails.
                context.metadata = context.metadata or {}
                context.metadata["hermes_session_export_failed"] = True
