import argparse
import asyncio
import json
from pathlib import Path

import pytest

from stella_harbor.harness_contract import verify_concurrent


@pytest.mark.parametrize("failed_worker", [None, 1])
def test_concurrent_gate_requires_every_worker_and_preserves_evidence(monkeypatch, tmp_path, failed_worker):
    commands = []

    async def start(*command, **kwargs):
        commands.append(command)
        output = Path(command[command.index("--output") + 1])
        worker = int(output.name.split("-")[-1])
        passed = worker != failed_worker
        (output / "contract.json").write_text(json.dumps({"passed": passed, "requests": [{"model": "m"}]}))

        class Process:
            async def wait(self):
                return 0 if passed else 1

        return Process()

    monkeypatch.setattr(asyncio, "create_subprocess_exec", start)
    args = argparse.Namespace(output=tmp_path, agent="hermes", version="v1", model="m",
                              thinking="max", context_window=1000000, max_tokens=384000,
                              image="debian:13", concurrency=2, live=False)
    if failed_worker is None:
        asyncio.run(verify_concurrent(args))
    else:
        with pytest.raises(RuntimeError, match="concurrent native installation"):
            asyncio.run(verify_concurrent(args))
    result = json.loads((tmp_path / "contract.json").read_text())
    assert result["passed"] is (failed_worker is None)
    assert len(result["workers"]) == 2
    assert all(worker["contract"]["requests"] for worker in result["workers"])
    assert all("--live" not in command and "--concurrency" not in command for command in commands)
