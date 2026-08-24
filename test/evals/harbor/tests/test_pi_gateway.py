import json

from harbor.models.agent.context import AgentContext

from stella_harbor.pi_gateway import PiGateway
from stella_harbor.runtime_identity import gateway_endpoint


def _agent(tmp_path):
    return PiGateway(logs_dir=tmp_path, model_name="gateway/gpt-5.6-luna")


def _event(*, input_tokens=11, output_tokens=7, cache_read=3, cost=0.25):
    return {
        "type": "message_end",
        "message": {
            "role": "assistant",
            "content": [{"type": "text", "text": "你好"}],
            "usage": {
                "input": input_tokens,
                "output": output_tokens,
                "cacheRead": cache_read,
                "cacheWrite": 2,
                "cost": {"total": cost},
            },
        },
    }


def test_pi_output_usage_parses_normal_utf8(tmp_path):
    (tmp_path / "pi.txt").write_text(json.dumps(_event(), ensure_ascii=False) + "\n")
    context = AgentContext()

    _agent(tmp_path).populate_context_post_run(context)

    assert context.n_input_tokens == 14
    assert context.n_output_tokens == 7
    assert context.n_cache_tokens == 3
    assert context.cost_usd == 0.25
    assert context.metadata is None


def test_pi_output_recovers_truncated_utf8_and_records_it(tmp_path):
    complete = json.dumps(_event(input_tokens=5), ensure_ascii=False).encode("utf-8")
    truncated = b'{"type":"message_end","message":{"role":"assistant","content":"' + "中".encode("utf-8")[:-1]
    (tmp_path / "pi.txt").write_bytes(complete + b"\n" + truncated)
    context = AgentContext()

    _agent(tmp_path).populate_context_post_run(context)

    assert context.n_input_tokens == 8
    assert context.n_output_tokens == 7
    assert context.metadata == {
        "pi_output_decode": {
            "utf8_decode_errors": 1,
            "truncated_utf8": True,
            "output_bytes": len(complete) + 1 + len(truncated),
        }
    }


def test_pi_output_usage_handles_empty_output(tmp_path):
    (tmp_path / "pi.txt").write_bytes(b"")
    context = AgentContext()

    _agent(tmp_path).populate_context_post_run(context)

    assert context.n_input_tokens == 0
    assert context.n_output_tokens == 0
    assert context.n_cache_tokens == 0
    assert context.cost_usd is None
    assert context.metadata is None
# Prices and model limits: kwargs are recorded in each trial's config.json,
# the environment is the fallback, and neither may be guessed at.

import pytest


class _Env(PiGateway):
    """PiGateway with the environment stubbed, so no Harbor plumbing is needed."""

    def __init__(self, env, **kwargs):
        self._env = env
        self.prices = {k: kwargs.get(k) for k in
                       ("cost_input", "cost_output", "cost_cache_read", "cost_cache_write")}
        self.context_window = int(kwargs.get("context_window") or 272000)
        self.max_tokens = int(kwargs.get("max_tokens") or 128000)

    def _get_env(self, name):  # type: ignore[override]
        return self._env.get(name)


_CREDS = {"OPENAI_BASE_URL": "https://gw.example/v1", "OPENAI_API_KEY": "k"}
_PRICES = {
    "EVAL_COST_INPUT": "0.20",
    "EVAL_COST_OUTPUT": "1.20",
    "EVAL_COST_CACHE_READ": "0.02",
    "EVAL_COST_CACHE_WRITE": "0.25",
}


def _model(agent):
    return json.loads(agent._models_json("m"))["providers"]["gateway"]["models"][0]


def test_models_json_prices_the_model_from_the_environment():
    assert _model(_Env(_CREDS | _PRICES))["cost"] == {
        "input": 0.20,
        "output": 1.20,
        "cacheRead": 0.02,
        "cacheWrite": 0.25,
    }


def test_agent_kwargs_win_over_the_environment():
    """The kwarg is what Harbor writes into config.json, so it must be the price used."""
    agent = _Env(_CREDS | _PRICES, cost_input=0.5, cost_output=2.0,
                 cost_cache_read=0.05, cost_cache_write=0.6)
    assert _model(agent)["cost"] == {
        "input": 0.5,
        "output": 2.0,
        "cacheRead": 0.05,
        "cacheWrite": 0.6,
    }


def test_a_missing_price_fails_loudly_instead_of_guessing():
    agent = _Env(_CREDS | {k: v for k, v in _PRICES.items() if k != "EVAL_COST_OUTPUT"})
    with pytest.raises(ValueError, match="cost_output"):
        agent._models_json("m")


def test_model_limits_default_and_are_overridable():
    assert _model(_Env(_CREDS | _PRICES))["contextWindow"] == 272000
    agent = _Env(_CREDS | _PRICES, context_window=400000, max_tokens=64000)
    model = _model(agent)
    assert model["contextWindow"] == 400000 and model["maxTokens"] == 64000


@pytest.mark.parametrize("endpoint", [
    "https://gateway.example/v1/tenant-a",
    "https://gateway.example/v1/tenant-b",
    "http://gateway.example:8001/v1",
    "https://gateway.example:9443/v1",
])
def test_gateway_endpoint_distinguishes_scheme_port_and_base_path(endpoint):
    identities = {
        gateway_endpoint("https://gateway.example/v1/tenant-a"),
        gateway_endpoint("https://gateway.example/v1/tenant-b"),
        gateway_endpoint("http://gateway.example:8001/v1"),
        gateway_endpoint("https://gateway.example:9443/v1"),
    }
    assert gateway_endpoint(endpoint) in identities
    assert len(identities) == 4


@pytest.mark.parametrize("endpoint", [
    "https://user:password@gateway.example/v1",
    "https://gateway.example/v1?tenant=other",
    "https://gateway.example/v1#other",
])
def test_gateway_endpoint_rejects_credential_bearing_or_ambiguous_urls(endpoint):
    with pytest.raises(ValueError, match="gateway endpoint"):
        gateway_endpoint(endpoint)


def test_gateway_endpoint_normalizes_idna_case_port_and_path():
    assert gateway_endpoint("HTTPS://BÜCHER.example:443/v1/../api/") == "https://xn--bcher-kva.example:443/api"


def test_pi_runtime_identity_attests_its_canonical_price_gateway_and_timeout(monkeypatch):
    monkeypatch.setenv("HARBOR_AGENT_TIMEOUT_SEC", "900")
    identity = _Env(_CREDS | _PRICES)._runtime_identity()

    assert identity == {
        "price_digest": "sha256:1b740edf49f73a0f31f0a4821cd2ea4b21acee4c6238b36c71b88c8532bf279d",
        "provider_type": "openai-responses",
        "gateway_endpoint": "https://gw.example:443/v1",
        "effective_agent_timeout_sec": 900,
        "fixture_spec_digest": "sha256:bd966e2e97148c3e798db8b09d915c718e55e108289192e763af7a14e3ae8b15",
        "fixture_plan_digest": "sha256:3a8025c8cd6effe578d6536d29a972d7a929b9b1b3e5427e9c7d4d4dc0a596a4",
    }
