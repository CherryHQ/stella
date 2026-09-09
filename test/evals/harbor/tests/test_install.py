from types import SimpleNamespace

from stella_harbor import external_loop
from stella_harbor.install import SETUP_TIMEOUT_SEC, HARBOR_SETUP_TIMEOUT_SEC
from harbor.trial.trial import Trial


def test_external_run_changes_only_installation_budget(monkeypatch, tmp_path):
    assert Trial._AGENT_SETUP_TIMEOUT_SEC == HARBOR_SETUP_TIMEOUT_SEC
    captured = []
    monkeypatch.setenv("OPENAI_MODEL", "test-model")
    monkeypatch.setattr("sys.argv", ["external_loop", "--agent", "hermes", "--version", "v2026.9.7", "--output", str(tmp_path), "--", "-k", "1"])
    monkeypatch.setattr(external_loop.subprocess, "run", lambda command, **kwargs: captured.append(command) or SimpleNamespace(returncode=0))
    assert external_loop.main() == 0
    command = captured[0]
    multiplier = float(command[command.index("--agent-setup-timeout-multiplier") + 1])
    assert multiplier * HARBOR_SETUP_TIMEOUT_SEC == SETUP_TIMEOUT_SEC
    assert "--timeout-multiplier" not in command
    assert "--agent-timeout-multiplier" not in command
