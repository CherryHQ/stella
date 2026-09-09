from stella_harbor.hermes_gateway import HermesGateway
import asyncio
import json
import pytest


def test_gateway_preserves_model_and_explicit_responses_controls(tmp_path):
    agent = HermesGateway(logs_dir=tmp_path, model_name="gateway/deepseek/deepseek-v4-flash",
                          version="v2026.9.7", thinking="max", context_window=1000000, max_tokens=384000)
    config = agent.gateway_config("deepseek/deepseek-v4-flash", "https://gateway.example/v1")
    assert config["model"] == {"provider": "custom:eval_gateway", "default": "deepseek/deepseek-v4-flash", "context_length": 1000000}
    provider = config["providers"]["eval_gateway"]
    assert provider["transport"] == "codex_responses"
    assert provider["extra_body"] == {"reasoning": {"effort": "max"}, "max_output_tokens": 384000}
    assert provider["key_env"] == "OPENAI_API_KEY"
    assert config["agent"]["max_turns"] == 90


def test_setup_cancellation_records_the_incomplete_stage(monkeypatch, tmp_path):
    agent = HermesGateway(logs_dir=tmp_path, version="v2026.9.7")

    async def dependencies(*args):
        pass

    async def execute(environment, command, **kwargs):
        if "--stage python-deps" in command:
            raise asyncio.CancelledError()

    monkeypatch.delenv("HERMES_RELEASE_ARCHIVE", raising=False)
    monkeypatch.setattr(agent, "ensure_system_dependencies", dependencies)
    monkeypatch.setattr(agent, "exec_as_agent", execute)
    with pytest.raises(asyncio.CancelledError):
        asyncio.run(agent.install(None))
    stages = json.loads((tmp_path / "install-stages.json").read_text())
    assert [(stage["stage"], stage["status"]) for stage in stages] == [
        ("prerequisites", "completed"), ("venv", "completed"), ("python-deps", "failed"),
    ]
    assert all(stage["elapsed_sec"] >= 0 for stage in stages)
