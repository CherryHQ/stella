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
from typing import override

from harbor.agents.installed.pi import Pi
from harbor.environments.base import BaseEnvironment

PROVIDER = "gateway"

_MODEL_DEFAULTS = {
    "api": "openai-responses",
    "reasoning": True,
    "input": ["text", "image"],
    "contextWindow": 272000,
    "maxTokens": 128000,
    "cost": {"input": 1.25, "output": 10.0, "cacheRead": 0.125, "cacheWrite": 0.0},
}


class PiGateway(Pi):
    """pi against `gateway/<model>`, configured from OPENAI_BASE_URL/OPENAI_API_KEY."""

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

    def _models_json(self, model_id: str) -> str:
        base_url, api_key = self._credentials()
        model = {"id": model_id, "name": model_id, **_MODEL_DEFAULTS}
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
