"""Canonical runtime identity written by every Harbor adapter trial."""

from __future__ import annotations

import hashlib
import json
import os
from decimal import Decimal, InvalidOperation
from typing import Mapping
from urllib.parse import urlsplit

# This is the frozen specialized-fixture contract, not a task id. Individual
# task plans are attested separately, then aggregated as a task/plan set.
FIXTURE_SPEC_DIGEST = "sha256:bd966e2e97148c3e798db8b09d915c718e55e108289192e763af7a14e3ae8b15"
NO_FIXTURE_PLAN_DIGEST = "sha256:3a8025c8cd6effe578d6536d29a972d7a929b9b1b3e5427e9c7d4d4dc0a596a4"
PRICE_FIELDS = (
    ("input", "cost_input", "EVAL_COST_INPUT"),
    ("output", "cost_output", "EVAL_COST_OUTPUT"),
    ("cache_read", "cost_cache_read", "EVAL_COST_CACHE_READ"),
    ("cache_write", "cost_cache_write", "EVAL_COST_CACHE_WRITE"),
)
PRICE_DEFAULTS = {
    "input": "0.20",
    "output": "1.20",
    "cache_read": "0.02",
    "cache_write": "0.25",
}


def _canonical_decimal(value: object) -> str:
    try:
        decimal = Decimal(str(value))
    except (InvalidOperation, ValueError) as exc:
        raise ValueError(f"invalid eval price {value!r}") from exc
    if not decimal.is_finite() or decimal < 0:
        raise ValueError(f"invalid eval price {value!r}")
    text = format(decimal.normalize(), "f")
    return "0" if text in ("", "-0") else text


def canonical_prices(values: Mapping[str, object]) -> dict[str, str]:
    """Return the one four-price object all adapters and the loop hash."""
    return {field: _canonical_decimal(values[field]) for field, _, _ in PRICE_FIELDS}


def prices_from_env(env: Mapping[str, str] | None = None) -> dict[str, str]:
    env = os.environ if env is None else env
    return canonical_prices({field: env.get(env_name, PRICE_DEFAULTS[field]) for field, _, env_name in PRICE_FIELDS})


def price_digest(prices: Mapping[str, object]) -> str:
    canonical = json.dumps(canonical_prices(prices), sort_keys=True, separators=(",", ":"))
    return "sha256:" + hashlib.sha256(canonical.encode()).hexdigest()


def gateway_host(base_url: str) -> str | None:
    return urlsplit(base_url).hostname or None


def timeout_from_env(env: Mapping[str, str] | None = None) -> int | None:
    env = os.environ if env is None else env
    value = env.get("HARBOR_AGENT_TIMEOUT_SEC")
    try:
        timeout = int(value) if value is not None else None
    except ValueError:
        return None
    return timeout if timeout is not None and timeout > 0 else None
