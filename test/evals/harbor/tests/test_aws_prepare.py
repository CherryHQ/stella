import json
import subprocess
import sys

import pytest
from stella_harbor.aws_prepare import (
    DATASET_REF,
    measure,
    preparation_inventory,
    prepare_command,
)


def test_prepare_uses_nop_install_only_and_preserves_environment_cache(tmp_path):
    command = prepare_command(tmp_path / "job", 4, None)
    assert f"terminal-bench/terminal-bench-2-1@{DATASET_REF}" in command
    assert command[command.index("-a") + 1] == "nop"
    assert "--install-only" in command
    assert "--no-delete" in command
    assert "-m" not in command
    assert command[command.index("-n") + 1] == "4"


@pytest.mark.parametrize("concurrency", [0, 5])
def test_preparation_concurrency_is_bounded(tmp_path, concurrency):
    with pytest.raises(ValueError):
        prepare_command(tmp_path, concurrency, None)


def test_measure_records_exit_and_timing_but_never_command_or_environment(tmp_path):
    output = tmp_path / "measurement.json"
    status = measure(
        [sys.executable, "-c", "raise SystemExit(7)", "secret-not-for-logs"], output, 4
    )
    assert status == 7
    summary = json.loads(output.read_text())
    assert summary["exit_code"] == 7
    assert summary["concurrency"] == 4
    assert summary["wall_seconds"] > 0
    assert summary["started_at"].endswith("+00:00")
    assert "secret" not in output.read_text()
    with pytest.raises(ValueError, match="overwrite"):
        measure([sys.executable, "-c", "pass"], output, 4)


def test_preparation_rejects_failed_or_missing_environments(tmp_path):
    trial = tmp_path / "job/task"
    trial.mkdir(parents=True)
    (trial / "config.json").write_text(json.dumps({"trial_name": "task"}))
    result = trial / "result.json"
    result.write_text(
        json.dumps({"exception_info": {"exception_type": "TimeoutError"}})
    )
    with pytest.raises(ValueError, match="failed=1"):
        preparation_inventory(tmp_path, 1)
    result.write_text(json.dumps({"exception_info": None}))
    with pytest.raises(ValueError, match="expected=2"):
        preparation_inventory(tmp_path, 2)
    completed = {
        "started_at": "2026-09-06T00:00:00+00:00",
        "finished_at": "2026-09-06T00:00:01+00:00",
    }
    result.write_text(
        json.dumps(
            {
                **completed,
                "task_name": "task",
                "agent_info": {"name": "nop"},
                "environment_setup": completed,
                "agent_setup": completed,
            }
        )
    )
    (trial.parent / "config.json").write_text("{}")
    (trial.parent / "result.json").write_text(
        json.dumps(
            {
                "finished_at": completed["finished_at"],
                "n_total_trials": 1,
                "stats": {"n_completed_trials": 1, "n_errored_trials": 0},
            }
        )
    )
    assert preparation_inventory(tmp_path, 1) == {
        "environments": 1,
        "failed": 0,
        "model_calls": 0,
    }
    result.write_text(
        json.dumps({**json.loads(result.read_text()), "environment_setup": None})
    )
    with pytest.raises(ValueError, match="install-only evidence"):
        preparation_inventory(tmp_path, 1)


def test_real_harbor_print_config_disables_model_and_verifier(tmp_path):
    command = prepare_command(tmp_path / "job", 4, None) + ["--print-config"]
    result = subprocess.run(command, capture_output=True, text=True, check=True)
    config = json.loads(result.stdout)
    assert config["install_only"] is True
    assert config["agents"] == [{"name": "nop"}]
    assert config["verifier"]["disable"] is True
    assert config["environment"]["delete"] is False
    assert config["datasets"][0]["ref"] == DATASET_REF
    assert not (tmp_path / "job").exists()
