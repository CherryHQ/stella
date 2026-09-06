import json
import subprocess
import sys
from types import SimpleNamespace

import pytest

from stella_harbor.aws_prepare import (
    DATASET_REF,
    capacity_metrics,
    measure,
    preparation_inventory,
    prepare_command,
    queue_config,
    write_queue,
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


@pytest.mark.parametrize("limit", [89, 128, 178, 200, 444, 445])
def test_queue_declares_exact_counts_before_any_results(limit, tmp_path):
    names = [f"task-{index:02d}" for index in range(89)]
    names[0] = "install-windows-3.11"
    text, plan = queue_config(names, limit, 32)
    assert queue_config(list(reversed(names)), limit, 32) == (text, plan)
    assert len(plan["task_attempts"]) == 89
    assert sum(plan["task_attempts"].values()) == limit
    assert max(plan["task_attempts"].values()) <= 5
    assert min(plan["task_attempts"].values()) >= 1
    assert plan["full_plan_trials"] == 445
    if limit == 128:
        assert list(plan["task_attempts"].values()).count(2) == 39
        assert list(plan["task_attempts"].values()).count(1) == 50
    if limit == 445:
        assert plan["harbor_n_attempts"] == 5
    config_path = tmp_path / "queue.yaml"
    config_path.write_text(text)
    printed = subprocess.run(
        ["harbor", "run", "-c", str(config_path), "-n", "32", "--print-config"],
        text=True,
        capture_output=True,
        check=True,
    )
    config = json.loads(printed.stdout)
    assert "retry:\n  max_retries: 0" in text
    assert config.get("retry", {}).get("max_retries", 0) == 0
    assert all(dataset["ref"] == DATASET_REF for dataset in config["datasets"])
    assert (
        sum(len(dataset["task_names"]) for dataset in config["datasets"])
        * config.get("n_attempts", 1)
        == limit
    )


@pytest.mark.parametrize("limit", [0, 88, 446])
def test_queue_cannot_exceed_budget_or_omit_task_coverage(limit):
    with pytest.raises(ValueError, match="89-445"):
        queue_config([f"task-{index}" for index in range(89)], limit, 32)


def test_queue_rejects_duplicate_or_unsafe_task_names():
    with pytest.raises(ValueError, match="distinct task"):
        queue_config(["task"] * 89, 128, 32)
    with pytest.raises(ValueError, match="distinct task"):
        queue_config([f"task-{index}" for index in range(88)] + ["bad: task"], 128, 32)


@pytest.mark.parametrize("mismatch", [False, True])
def test_queue_preflight_checks_native_resolution_before_writing(
    tmp_path, monkeypatch, mismatch
):
    from harbor import JobPlan

    job = tmp_path / "warmup/run"
    completed = {
        "started_at": "2026-09-06T00:00:00+00:00",
        "finished_at": "2026-09-06T00:00:01+00:00",
    }
    for index in range(89):
        trial = job / f"task-{index:02d}__warmup"
        trial.mkdir(parents=True)
        (trial / "config.json").write_text(json.dumps({"trial_name": trial.name}))
        (trial / "result.json").write_text(
            json.dumps(
                {
                    **completed,
                    "task_name": f"terminal-bench/task-{index:02d}",
                    "agent_info": {"name": "nop"},
                    "environment_setup": completed,
                    "agent_setup": completed,
                }
            )
        )
    (job / "config.json").write_text("{}")
    (job / "result.json").write_text(
        json.dumps(
            {
                "finished_at": completed["finished_at"],
                "n_total_trials": 89,
                "stats": {"n_completed_trials": 89},
            }
        )
    )

    async def resolve(config):
        assert config.retry.max_retries == 0
        assert config.n_concurrent_trials == 32
        assert config.agents[0].name == "nop"
        names = [
            name.split("/")[-1]
            for dataset in config.datasets
            for name in dataset.task_names
        ]
        if mismatch:
            names = names[:-1]
        return SimpleNamespace(
            trial_configs=[
                SimpleNamespace(
                    task=SimpleNamespace(name=f"terminal-bench/{name}", path=None)
                )
                for name in names
            ]
        )

    monkeypatch.setattr(JobPlan, "from_config", resolve)
    output, config = tmp_path / "plan.json", tmp_path / "queue.yaml"
    if mismatch:
        with pytest.raises(ValueError, match="resolution differs"):
            write_queue(job, output, config, 128, 32)
        assert not output.exists() and not config.exists()
    else:
        write_queue(job, output, config, 128, 32)
        plan = json.loads(output.read_text())
        assert plan["resolved_trials"] == 128
        assert len(plan["trial_order"]) == 128
        assert plan["harbor_version"]
        with pytest.raises(ValueError, match="overwrite"):
            write_queue(job, output, config, 128, 32)


@pytest.mark.parametrize(
    "oom,memory,reason", [(1, 16, "oom_kill"), (0, 7, "available_memory_below_floor")]
)
def test_capacity_resource_guard_stops_child(
    tmp_path, monkeypatch, oom, memory, reason
):
    def sample(gib, kills):
        return {
            "available_memory_bytes": gib * 1024**3,
            "cpu_ticks": 100,
            "idle_ticks": 50,
            "load_1m": 0,
            "oom_kills": kills,
        }

    samples = iter([sample(16, 3), sample(memory, 3 + oom)])
    monkeypatch.setattr(
        "stella_harbor.aws_prepare.sample_resources", lambda: next(samples)
    )
    output = tmp_path / "measurement.json"
    assert (
        measure(
            [sys.executable, "-c", "import time; time.sleep(60)"],
            output,
            32,
            minimum_memory_bytes=8 * 1024**3,
            stop_on_oom=True,
        )
        == 125
    )
    summary = json.loads(output.read_text())
    assert summary["stop_reason"] == reason
    assert summary["oom_kills"] == oom
    assert summary["exit_code"] != 0


def test_memory_guard_records_unattributed_oom_without_stopping(tmp_path, monkeypatch):
    calls = 0

    def sample():
        nonlocal calls
        calls += 1
        return {
            "available_memory_bytes": 50 * 1024**3,
            "cpu_ticks": 100,
            "idle_ticks": 50,
            "load_1m": 0,
            "oom_kills": 0 if calls == 1 else 14,
        }

    monkeypatch.setattr("stella_harbor.aws_prepare.sample_resources", sample)
    output = tmp_path / "metrics.json"
    assert (
        measure(
            [sys.executable, "-c", "pass"], output, 32, minimum_memory_bytes=8 * 1024**3
        )
        == 0
    )
    result = json.loads(output.read_text())
    assert result["oom_kills"] == 14
    assert result["stop_reason"] is None
    assert result["stop_on_oom"] is False


def test_timebox_snapshots_progress_before_interrupting(tmp_path):
    job = tmp_path / "jobs/job/run"
    job.mkdir(parents=True)
    (job / "result.json").write_text(
        json.dumps(
            {
                "stats": {
                    "n_completed_trials": 0,
                    "n_running_trials": 32,
                    "n_pending_trials": 57,
                }
            }
        )
    )
    output = tmp_path / "metrics.json"
    assert (
        measure(
            [sys.executable, "-c", "import time; time.sleep(60)"],
            output,
            32,
            max_seconds=0.05,
            trial_root=tmp_path / "jobs",
        )
        == 124
    )
    result = json.loads(output.read_text())
    assert result["stop_reason"] == "sample_time_limit"
    assert result["wall_seconds"] < 5
    assert result["sample_inventory"]["scoreable"] == 0
    assert result["job_progress"]["n_running_trials"] == 32
    assert result["maximum_running_trials"] == 32
    capacity_metrics(tmp_path / "jobs", output)
    result = json.loads(output.read_text())
    assert result["incomplete_sample"] is True
    assert result["capacity_stop_reasons"] == []
    assert result["scoreable_per_hour"] == 0


@pytest.mark.parametrize("invalid,stopped", [(0, False), (4, False), (5, True)])
def test_capacity_keeps_failed_primaries_and_reports_real_overlap(
    tmp_path, invalid, stopped
):
    job = tmp_path / "jobs"
    for index in range(89):
        trial = job / f"task-{index}__a"
        (trial / "agent/stella").mkdir(parents=True)
        (trial / "config.json").write_text(json.dumps({"trial_name": trial.name}))
        (trial / "result.json").write_text(
            json.dumps(
                {
                    "started_at": "2026-09-06T00:00:00+00:00",
                    "finished_at": "2026-09-06T00:00:01+00:00",
                    "verifier_result": {"rewards": {"reward": 1}},
                }
            )
        )
        (trial / "agent/stella/result.json").write_text(
            json.dumps(
                {
                    "valid": index >= invalid,
                    "bridge_nonce": "nonce",
                    "metrics": {},
                }
            )
        )
    output = tmp_path / "metrics.json"
    output.write_text(
        json.dumps(
            {
                "completed_at": "2026-09-06T00:00:02+00:00",
                "wall_seconds": 2,
                "exit_code": 0,
            }
        )
    )
    capacity_metrics(job, output)
    result = json.loads(output.read_text())
    assert result["inventory"]["trials"] == 89
    assert result["resolved"] == 89 - invalid
    assert result["observed_peak_trial_overlap"] == 89
    assert bool(result["capacity_stop_reasons"]) == stopped
    assert result["scoreable_per_hour"] == (89 - invalid) * 1800
    assert "bridge_nonce" not in output.read_text()


@pytest.mark.parametrize("mismatch", [False, True])
def test_queued_metrics_validate_per_task_counts_not_only_total(tmp_path, mismatch):
    names = [f"task-{index:02d}" for index in range(89)]
    _, plan = queue_config(names, 128, 32)
    queue_plan = tmp_path / "queue-plan.json"
    queue_plan.write_text(json.dumps(plan))
    observed = dict(plan["task_attempts"])
    if mismatch:
        # Keep the total at 128, but repeat one task instead of the declared one.
        repeated = next(name for name, count in observed.items() if count == 2)
        single = next(name for name, count in observed.items() if count == 1)
        observed[repeated] -= 1
        observed[single] += 1
    for name, count in observed.items():
        for attempt in range(count):
            trial = tmp_path / "jobs" / f"{name}__{attempt}"
            (trial / "agent/stella").mkdir(parents=True)
            (trial / "config.json").write_text(json.dumps({"trial_name": trial.name}))
            (trial / "result.json").write_text(
                json.dumps({"verifier_result": {"rewards": {"reward": 0}}})
            )
            (trial / "agent/stella/result.json").write_text(
                json.dumps({"valid": True, "bridge_nonce": "nonce"})
            )
    output = tmp_path / "metrics.json"
    output.write_text(
        json.dumps(
            {
                "completed_at": "2026-09-06T00:01:00+00:00",
                "wall_seconds": 60,
                "exit_code": 0,
            }
        )
    )
    capacity_metrics(tmp_path / "jobs", output, queue_plan)
    result = json.loads(output.read_text())
    assert result["inventory"]["trials"] == 128
    assert result["planned_trials"] == 128
    assert result["resolved"] == 0
    assert result["scoreable_per_hour"] == 128 * 60
    assert result["capacity_stop_reasons"] == (
        ["queue_attempt_counts_mismatch"] if mismatch else []
    )
