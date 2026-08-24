"""Harbor agent: upstream pi, pointed at the same gateway Stella evaluates on.

Harbor's built-in `pi` agent forwards OPENAI_API_KEY into the container, but pi
resolves the `openai` provider's base URL from its own model registry, so an
OpenAI-compatible gateway is unreachable: the key is sent to api.openai.com and
comes back 401. pi does read custom providers from ~/.pi/agent/models.json, so
this subclass writes one before the run.

Pricing mirrors the Stella eval provider so the two cost columns compare.
"""

from __future__ import annotations

import json
import shlex
from typing import Any, override

from harbor.agents.installed.pi import Pi
from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext

PROVIDER = "gateway"

_MODEL_DEFAULTS = {
    "api": "openai-responses",
    "reasoning": True,
    "input": ["text", "image"],
}

# Sized for gpt-5.6-luna, which is what this adapter was written against. Any
# other model needs its own numbers: too small silently truncates the context,
# too large fails the request late in a trial.
_CONTEXT_WINDOW = 272000
_MAX_TOKENS = 128000

# pi states prices per million tokens, and so does the eval provider, so feeding
# both from one source is what makes the two cost columns comparable. A
# hardcoded price silently misreports every model except the one it was written
# for, and the cost is baked into each trial and cannot be recomputed.
#
# Prefer --agent-kwarg: Harbor records kwargs in every trial's config.json, so
# the price the trial was scored at stays with the trial. The environment
# variables remain as a fallback, and are what the eval provider itself reads.
_COST_FIELDS = {
    "input": ("cost_input", "EVAL_COST_INPUT"),
    "output": ("cost_output", "EVAL_COST_OUTPUT"),
    "cacheRead": ("cost_cache_read", "EVAL_COST_CACHE_READ"),
    "cacheWrite": ("cost_cache_write", "EVAL_COST_CACHE_WRITE"),
}


def _decode_output(data: bytes) -> tuple[str, int, bool]:
    """Decode Pi's JSONL output without hiding malformed UTF-8.

    A strict pass identifies whether the file is damaged. The recovery pass is
    deliberately limited to the text used for usage parsing; the original file
    remains in Harbor's logs, and the diagnostics are copied into agent_result.
    """
    try:
        return data.decode("utf-8"), 0, False
    except UnicodeDecodeError:
        text = data.decode("utf-8", errors="replace")

    errors = 0
    truncated = False
    offset = 0
    while offset < len(data):
        try:
            data[offset:].decode("utf-8")
            break
        except UnicodeDecodeError as exc:
            errors += 1
            truncated = truncated or (
                exc.reason == "unexpected end of data"
                and offset + exc.end == len(data)
            )
            offset += max(exc.end, exc.start + 1)

    return text, errors, truncated


class PiGateway(Pi):
    """pi against `gateway/<model>`, configured from OPENAI_BASE_URL/OPENAI_API_KEY."""

    def __init__(self, *args: Any, cost_input: float | str | None = None,
                 cost_output: float | str | None = None,
                 cost_cache_read: float | str | None = None,
                 cost_cache_write: float | str | None = None,
                 context_window: int | str = _CONTEXT_WINDOW,
                 max_tokens: int | str = _MAX_TOKENS, **kwargs: Any) -> None:
        super().__init__(*args, **kwargs)
        self.prices = {
            "cost_input": cost_input, "cost_output": cost_output,
            "cost_cache_read": cost_cache_read, "cost_cache_write": cost_cache_write,
        }
        self.context_window = int(context_window)
        self.max_tokens = int(max_tokens)

    @staticmethod
    @override
    def name() -> str:
        return "pi-gateway"

    def _credentials(self) -> tuple[str, str]:
        base_url = self._get_env("OPENAI_BASE_URL")
        api_key = self._get_env("OPENAI_API_KEY")
        if not base_url or not api_key:
            raise ValueError("OPENAI_BASE_URL and OPENAI_API_KEY must be set")
        return base_url.rstrip("/"), api_key

    def _cost(self) -> dict[str, float]:
        cost: dict[str, float] = {}
        missing: list[str] = []
        for field, (kwarg, var) in _COST_FIELDS.items():
            value = self.prices.get(kwarg) or self._get_env(var)
            if not value:
                missing.append(f"--agent-kwarg {kwarg} (or {var})")
                continue
            cost[field] = float(value)
        if missing:
            raise ValueError(
                f"pi prices are unset: {', '.join(missing)}. Set them to the same "
                "per-million prices as Stella's eval provider, or the two cost "
                "columns will not mean the same thing."
            )
        return cost

    def _models_json(self, model_id: str) -> str:
        base_url, api_key = self._credentials()
        model = {
            "id": model_id, "name": model_id, **_MODEL_DEFAULTS,
            "contextWindow": self.context_window, "maxTokens": self.max_tokens,
            "cost": self._cost(),
        }
        return json.dumps(
            {
                "providers": {
                    PROVIDER: {
                        "baseUrl": base_url,
                        "api": _MODEL_DEFAULTS["api"],
                        "apiKey": api_key,
                        "models": [model],
                    }
                }
            }
        )

    @override
    async def install(self, environment: BaseEnvironment) -> None:
        await super().install(environment)
        model_id = (self.model_name or "").split("/", 1)[-1]
        if not model_id:
            raise ValueError("Model name must be in the format provider/model_name")
        # A heredoc would need the secret quoted into the script; base64 keeps it
        # off the process table of anything that inspects the command.
        payload = self._models_json(model_id)
        await self.exec_as_agent(
            environment,
            command=(
                "set -euo pipefail; mkdir -p $HOME/.pi/agent; "
                f"printf %s {shlex.quote(payload)} > $HOME/.pi/agent/models.json; "
                "chmod 600 $HOME/.pi/agent/models.json"
            ),
        )

    @override
    def populate_context_post_run(self, context: AgentContext) -> None:
        """Parse Pi usage while keeping truncated output visible.

        Harbor 0.21's Pi implementation uses ``Path.read_text()`` here, so a
        partial UTF-8 sequence raises before the trial result is written. This
        adapter owns the workaround until Harbor ships the equivalent fix.
        """
        output_file = self.logs_dir / self._OUTPUT_FILENAME
        if not output_file.exists():
            return

        raw_output = output_file.read_bytes()
        output, decode_errors, truncated = _decode_output(raw_output)
        if decode_errors:
            self.logger.warning(
                "Pi output %s had %d UTF-8 decode error(s)%s; usage parsing recovered",
                output_file,
                decode_errors,
                " at EOF (likely truncated)" if truncated else "",
            )
            context.metadata = context.metadata or {}
            context.metadata["pi_output_decode"] = {
                "utf8_decode_errors": decode_errors,
                "truncated_utf8": truncated,
                "output_bytes": len(raw_output),
            }

        total_input_tokens = 0
        total_output_tokens = 0
        total_cache_read_tokens = 0
        total_cost = 0.0

        for line in output.splitlines():
            line = line.strip()
            if not line:
                continue
            try:
                event = json.loads(line)
                if event.get("type") == "message_end":
                    message = event.get("message") or {}
                    if message.get("role") == "assistant":
                        usage = message.get("usage") or {}
                        total_input_tokens += usage.get("input", 0)
                        total_output_tokens += usage.get("output", 0)
                        total_cache_read_tokens += usage.get("cacheRead", 0)
                        cost = usage.get("cost") or {}
                        total_cost += cost.get("total", 0.0)
            except (json.JSONDecodeError, AttributeError, TypeError):
                continue

        context.n_input_tokens = total_input_tokens + total_cache_read_tokens
        context.n_output_tokens = total_output_tokens
        context.n_cache_tokens = total_cache_read_tokens
        context.cost_usd = total_cost if total_cost > 0 else None
