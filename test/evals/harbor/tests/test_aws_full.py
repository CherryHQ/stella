import importlib.util
import json
import os
import subprocess
from pathlib import Path

import pytest

from stella_harbor.aws_merge import EXPECTED_TASKS, inventory, merge, topup_config

ROOT = Path(__file__).parents[4]
REMOTE = ROOT / "test/evals/harbor/aws_runner.sh"
CONTROLLER = ROOT / "test/evals/harbor/aws_full.py"
SMOKE_TASKSET = ROOT / "test/evals/harbor/tasksets/aws-smoke.yaml"
TASK = ROOT / ".mise/tasks/eval/tb21/aws"

_spec = importlib.util.spec_from_file_location("stella_aws_full", CONTROLLER)
assert _spec and _spec.loader
_aws_full = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_aws_full)


def test_journal_survives_closed_stdout_and_shutdown(tmp_path: Path):
    script = """
import importlib.util
import os
import sys
from pathlib import Path
spec = importlib.util.spec_from_file_location('controller', sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
reader, writer = os.pipe()
os.close(reader)
os.dup2(writer, sys.stdout.fileno())
os.close(writer)
journal = module.RunJournal(Path(sys.argv[2]))
journal.record('remote-progress', phase='warmup/topup-running')
journal.record('run-complete')
"""
    result = subprocess.run(
        [_aws_full.sys.executable, "-c", script, str(CONTROLLER), str(tmp_path)],
        capture_output=True,
        text=True,
    )
    assert result.returncode == 0, result.stderr
    assert result.stderr == ""
    entries = [json.loads(line) for line in (tmp_path / "journal.ndjson").read_text().splitlines()]
    assert [entry["event"] for entry in entries] == ["remote-progress", "run-complete"]


def test_capacity_is_a_separate_full_dataset_mode():
    args = _aws_full.parse_args(["--capacity", "--warmup", "environment", "--max-topup-rounds", "0"])
    assert args.capacity and not args.pilot and not args.smoke
    remote = REMOTE.read_text()
    assert "capacity_workers=(16 32 48 64 16)" in remote
    assert "--minimum-memory-gib 8" in remote
    block = remote.split('# Capacity failures', 1)[1].split('# Each logical pass', 1)[0]
    assert "run_topups" not in block
    assert 'exit 0' in block
    assert "capacity_stop_reasons" in block


@pytest.mark.parametrize("mode,workers,tasks,concurrencies,minutes", [
    ("queued", 32, 89, [32], 0),
    ("throughput", 32, 89, [32], 10),
    ("capacity", 16, 89, [16, 32, 48, 64, 16], 0),
    ("pilot", 4, 5, [1, 4, 4, 1], 0),
    ("smoke", 8, 5, [8], 0),
    ("full", 8, 89, [8] * 5, 0),
])
def test_plan_records_actual_queues(tmp_path, monkeypatch, mode, workers, tasks, concurrencies, minutes):
    monkeypatch.setattr(_aws_full, "repository_root", lambda: tmp_path)
    monkeypatch.setattr(_aws_full, "require_environment", lambda: ("us-east-1", {"OPENAI_MODEL": "test"}))
    sources = []
    monkeypatch.setattr(_aws_full, "local_preflight", lambda root, commit, concurrency, smoke, journal: sources.append(smoke) or "candidate")
    monkeypatch.setattr(_aws_full, "sha256", lambda *args: "digest")
    monkeypatch.setattr(_aws_full.subprocess, "run", lambda *args, **kwargs: subprocess.CompletedProcess([], 0, "controller\n"))
    mode_flags = [] if mode == "full" else [f"--{mode}"]
    if mode == "queued":
        mode_flags.extend(["--trial-limit", "128"])
    assert _aws_full.main(["--plan", *mode_flags, "--warmup", "environment", "--max-topup-rounds", "0", "--concurrency", str(workers)]) == 0
    state = json.loads(next(tmp_path.glob("dist/evals/aws/*/state.json")).read_text())
    assert sources == [mode in {"smoke", "pilot"}]
    assert state["expected_tasks"] == tasks
    assert state["passes"] == len(concurrencies)
    assert state["pass_concurrencies"] == concurrencies
    assert state["sample_minutes"] == minutes
    assert state["run_mode"] == mode
    assert state["trial_limit"] == (128 if mode == "queued" else 0)


@pytest.mark.parametrize("extra", [["--sample-minutes", "0"], ["--sample-minutes", "31"], ["--max-topup-rounds", "1"], ["--warmup", "legacy"]])
def test_throughput_rejects_unbounded_or_retrying_samples(extra):
    with pytest.raises(RuntimeError, match="--throughput requires"):
        _aws_full.main(["--throughput", "--warmup", "environment", "--max-topup-rounds", "0", *extra])


@pytest.mark.parametrize("extra", [["--trial-limit", "88"], ["--trial-limit", "446"], ["--max-topup-rounds", "1"], ["--warmup", "legacy"]])
def test_queued_rejects_missing_coverage_or_extra_paid_work(extra):
    with pytest.raises(RuntimeError, match="--queued requires"):
        _aws_full.main(["--queued", "--warmup", "environment", "--max-topup-rounds", "0", *extra])


def test_trial_limit_cannot_silently_apply_to_another_mode():
    with pytest.raises(RuntimeError, match="--trial-limit requires --queued"):
        _aws_full.main(["--trial-limit", "128"])


@pytest.mark.parametrize("mode", ["throughput", "queued"])
@pytest.mark.parametrize("incomplete,reasons,expected", [(True, [], "sampled"), (False, [], "completed"), (False, ["low_memory"], "stopped")])
def test_performance_worker_runs_only_one_requested_sample(tmp_path, incomplete, reasons, expected, mode):
    body = REMOTE.read_text().split("  pass=1\n  capacity_status=running", 1)[1].split("\n  FINAL_PHASE=complete", 1)[0]
    script = r'''
set -euo pipefail
ROOT=$1
REPO=$1
PASSES=1
DATASET_SPEC=pinned-dataset
capacity_workers=(32)
pass=1
capacity_status=running
run_eval() {
  printf 'run %s\n' "$*"
  mkdir -p "$ROOT/metrics/$1"
  printf '%s' "$RESULT" > "$ROOT/metrics/$1.json"
}
as_eval() { :; }
journal() { :; }
capacity_artifacts() { printf 'status %s\n' "$1"; }
'''
    env = os.environ | {"RUN_MODE": mode, "RESULT": json.dumps({"capacity_stop_reasons": reasons, "incomplete_sample": incomplete})}
    result = subprocess.run(["bash", "-c", script + body, "_", str(tmp_path)], env=env, capture_output=True, text=True, check=True)
    source = f"-c {tmp_path}/metrics/queue.yaml" if mode == "queued" else "-d pinned-dataset -k 1"
    assert result.stdout.splitlines() == [f"run pass-01/00-main {source} -n 32", f"status {expected}"]


@pytest.mark.parametrize("failure_site", ["download_remote_journal", "run-failed"])
def test_cleanup_runs_when_failure_reporting_raises(tmp_path: Path, monkeypatch, failure_site):
    monkeypatch.setattr(_aws_full, "repository_root", lambda: tmp_path)
    monkeypatch.setattr(_aws_full, "require_environment", lambda: ("us-east-1", {"OPENAI_MODEL": "test"}))
    monkeypatch.setattr(_aws_full, "local_preflight", lambda *args: "candidate")
    monkeypatch.setattr(_aws_full, "sha256", lambda *args: "digest")
    monkeypatch.setattr(_aws_full.signal, "signal", lambda *args: None)
    monkeypatch.setattr(
        _aws_full.subprocess, "run", lambda *args, **kwargs: subprocess.CompletedProcess([], 0, "candidate\n")
    )

    def provision_failure(*args):
        raise RuntimeError("provision failed")

    def reporting_failure(*args, **kwargs):
        raise OSError("reporting failed")

    monkeypatch.setattr(_aws_full, "provision", provision_failure)
    monkeypatch.setattr(_aws_full, "download_remote_journal", lambda *args: None)
    if failure_site == "download_remote_journal":
        monkeypatch.setattr(_aws_full, failure_site, reporting_failure)
    else:
        original_record = _aws_full.RunJournal.record

        def record(self, event, **fields):
            if event == "run-failed":
                reporting_failure()
            original_record(self, event, **fields)

        monkeypatch.setattr(_aws_full.RunJournal, "record", record)
    cleaned = []
    monkeypatch.setattr(_aws_full, "cleanup", lambda *args: cleaned.append(True))
    with pytest.raises(OSError, match="reporting failed"):
        _aws_full.main([])
    assert cleaned == [True]


def test_environment_accepts_the_configured_model(monkeypatch):
    monkeypatch.setattr(
        os,
        "environ",
        {
            "AWS_REGION": "us-east-1",
            "OPENAI_BASE_URL": "https://gateway.example.invalid/v1",
            "OPENAI_API_KEY": "secret",
            "OPENAI_MODEL": "different-model",
            "EVAL_COST_INPUT": "0.22",
            "EVAL_COST_OUTPUT": "0.66",
            "EVAL_COST_CACHE_READ": "0.007",
            "EVAL_COST_CACHE_WRITE": "0",
        },
    )

    region, provider = _aws_full.require_environment()

    assert region == "us-east-1"
    assert provider["OPENAI_MODEL"] == "different-model"
    assert provider["EVAL_COST_INPUT"] == "0.22"


def trial(root: Path, group: str, task: str, marker: str, *, valid: bool, reward: float | None) -> Path:
    directory = root / group / "harbor-job" / "run" / f"{task}__{marker}"
    (directory / "agent/stella").mkdir(parents=True)
    (directory / "config.json").write_text(json.dumps({"trial_name": directory.name}))
    result = {"marker": marker}
    if reward is not None:
        result["verifier_result"] = {"rewards": {"reward": reward}}
    (directory / "result.json").write_text(json.dumps(result))
    (directory / "agent/stella/result.json").write_text(
        json.dumps(
            {
                "valid": valid,
                "bridge_nonce": f"nonce-{marker}",
                "predicate_violations": [],
                "metrics": {"bridge": {"adapter_faults": []}},
            }
        )
    )
    return directory


def test_cleanup_accepts_an_empty_ec2_reservation_after_janitor_termination(tmp_path: Path):
    class EmptyReservationAws:
        def run(self, service: str, operation: str, *args: str, **kwargs: object) -> dict[str, object]:
            assert (service, operation) == ("ec2", "describe-instances")
            return {"Reservations": []}

    state_path = tmp_path / "state.json"
    state = {"instance_id": "i-terminated"}
    state_path.write_text(json.dumps(state))
    _aws_full.cleanup(EmptyReservationAws(), state_path, state, _aws_full.RunJournal(tmp_path))
    assert state["instance_deleted"] is True
    assert state["cleaned_at"]


def test_source_bundle_clones_the_exact_candidate(tmp_path: Path):
    source = tmp_path / "source"
    source.mkdir()
    subprocess.run(["git", "init", "-q"], cwd=source, check=True)
    subprocess.run(["git", "config", "user.email", "eval@example.invalid"], cwd=source, check=True)
    subprocess.run(["git", "config", "user.name", "Eval Test"], cwd=source, check=True)
    (source / "evidence.txt").write_text("candidate\n")
    subprocess.run(["git", "add", "evidence.txt"], cwd=source, check=True)
    subprocess.run(["git", "commit", "-qm", "candidate"], cwd=source, check=True)
    commit = subprocess.run(
        ["git", "rev-parse", "HEAD"], cwd=source, text=True, capture_output=True, check=True
    ).stdout.strip()

    bundle = tmp_path / "source.bundle"
    _aws_full.create_source_bundle(source, bundle, commit, "test-run")
    clone = tmp_path / "clone"
    subprocess.run(["git", "clone", "-q", str(bundle), str(clone)], check=True)
    assert subprocess.run(
        ["git", "rev-parse", "HEAD"], cwd=clone, text=True, capture_output=True, check=True
    ).stdout.strip() == commit
    assert (clone / "evidence.txt").read_text() == "candidate\n"


def test_merge_selects_first_k_scoreable_without_selecting_on_outcome(tmp_path: Path):
    source = tmp_path / "jobs"
    for number in range(EXPECTED_TASKS):
        task = f"task-{number:02d}"
        trial(source, "pass-01", task, "p1", valid=True, reward=0.0)
        trial(source, "pass-02", task, "p2", valid=True, reward=1.0)
    # This invalid trial sorts before the reportable passes and must be ignored.
    trial(source, "pass-00", "task-00", "invalid", valid=False, reward=None)
    # Harbor jobs also have config/result files. They are summaries, not trials.
    summary = source / "pass-00" / "harbor-job"
    summary.mkdir(parents=True, exist_ok=True)
    (summary / "config.json").write_text("{}")
    (summary / "result.json").write_text("{}")

    state = inventory(source, 2)
    assert state["tasks"] == EXPECTED_TASKS
    assert state["invalid"] == 1
    assert state["invalid_reasons"] == {"adapter_invalid": 1}
    assert "harbor-job" not in state["task_names"]
    assert state["missing"] == {}

    output = tmp_path / "merged"
    result = merge(source, output, 2)
    assert result["copied"] == EXPECTED_TASKS * 2
    config = json.loads((output / "config.json").read_text())
    assert config["n_attempts"] == 2
    assert config["selected_trial_count"] == EXPECTED_TASKS * 2
    selected = sorted(output.glob("*/*/result.json"))
    assert len(selected) == EXPECTED_TASKS * 2
    task_zero = [json.loads(path.read_text())["marker"] for path in selected if "task-00__" in str(path)]
    assert task_zero == ["p1", "p2"]


def test_inventory_classifies_missing_adapter_exceptions_without_messages(tmp_path: Path):
    source = tmp_path / "jobs"
    crashed = trial(source, "pass-01", "task-a", "crashed", valid=True, reward=None)
    (crashed / "agent/stella/result.json").unlink()
    result = json.loads((crashed / "result.json").read_text())
    result["exception_info"] = {
        "exception_type": "RuntimeError",
        "exception_message": "stella-eval-agent did not write result: permission denied secret-123",
    }
    (crashed / "result.json").write_text(json.dumps(result))

    state = inventory(source, 1)
    assert state["exception_types"] == {"RuntimeError": 1}
    assert state["exception_categories"] == {
        "agent_result_missing": 1,
        "permission_denied": 1,
    }
    assert state["exception_signatures"] == {"stella_agent_not_result_permission_denied": 1}
    assert "secret-123" not in json.dumps(state)


def test_inventory_retains_only_identifier_attribute_name(tmp_path: Path):
    source = tmp_path / "jobs"
    crashed = trial(source, "pass-01", "task-a", "crashed", valid=True, reward=None)
    (crashed / "agent/stella/result.json").unlink()
    result = json.loads((crashed / "result.json").read_text())
    result["exception_info"] = {
        "exception_type": "AttributeError",
        "exception_message": "'dict' object has no attribute 'tool_catalog'; secret-123",
    }
    (crashed / "result.json").write_text(json.dumps(result))

    state = inventory(source, 1)
    assert state["exception_attributes"] == {"tool_catalog": 1}
    assert state["exception_receivers"] == {"dict": 1}
    assert "secret-123" not in json.dumps(state)


def test_inventory_reports_only_missing_scoreable_attempts(tmp_path: Path):
    source = tmp_path / "jobs"
    trial(source, "pass-01", "task-a", "valid", valid=True, reward=0.0)
    invalid = trial(source, "pass-02", "task-a", "invalid", valid=True, reward=1.0)
    adapter = json.loads((invalid / "agent/stella/result.json").read_text())
    adapter["metrics"]["bridge"]["adapter_faults"] = [{"code": "bridge-fault"}]
    (invalid / "agent/stella/result.json").write_text(json.dumps(adapter))
    assert inventory(source, 2)["missing"] == {"task-a": 1}


def test_remote_checkout_uses_normal_umask_and_credentials_stay_private():
    source = REMOTE.read_text()
    assert source.index("umask 022") < source.index("git clone")
    assert source.index("git clone") < source.index("umask 077")
    assert 'chmod 600 "$REPO/.env"' in source
    assert '"EVAL_COST_INPUT"' in source
    assert '"EVAL_COST_OUTPUT"' in source
    assert '"EVAL_COST_CACHE_READ"' in source
    assert '"EVAL_COST_CACHE_WRITE"' in source
    assert "unset OTEL_STELLA_RECORD_TOOL_IO" in source
    assert "mise use --global uv@0.12.6" in source
    assert "mise exec -- uv --version" in source
    assert "docker-compose-v2" in source
    assert "docker compose version >/dev/null" in source
    assert "useradd --create-home --shell /bin/bash stella-eval" in source
    assert "as_eval mise run eval:loop" in source
    assert 'chown -R stella-eval:stella-eval "$ROOT/merged"' in source
    assert "warmup-discarded" in source
    assert "warmup-inventory" in source
    assert "warmup-topup-$warmup_round" in source
    assert 'run_topups "$ROOT/jobs/warmup"' in source
    assert "modes=$(stat -c" in source
    assert "systemctl start --no-block stella-tb21.service" in CONTROLLER.read_text()
    assert "shutdown -h" in source


def test_smoke_gate_is_fixed_to_five_tasks_at_k1():
    remote = REMOTE.read_text()
    taskset = SMOKE_TASKSET.read_text()
    assert 'run_eval "$pass_name/00-main" -c "$ROOT/aws-smoke.yaml"' in remote
    assert taskset.count("      - terminal-bench/") == 5
    assert "n_attempts: 1" in taskset


def test_mise_task_is_the_single_entrypoint():
    source = TASK.read_text()
    assert "source ./.env" in source
    assert 'exec python3 test/evals/harbor/aws_full.py "$@"' in source


def test_topup_batch_excludes_scoreable_failures_and_pins_dataset(tmp_path):
    source = tmp_path / "jobs"
    trial(source, "pass-01", "task-a", "failed", valid=True, reward=0.0)
    trial(source, "pass-01", "task-b", "invalid", valid=False, reward=1.0)
    trial(source, "pass-01", "task-c", "missing", valid=True, reward=None)
    config = topup_config(inventory(source, 1), 2)
    assert "terminal-bench/task-a" not in config
    assert "terminal-bench/task-b" in config
    assert "terminal-bench/task-c" in config
    assert "n_attempts: 1" in config
    assert "n_concurrent_trials: 2" in config
    assert "ref: sha256:7d7bdc" in config


@pytest.mark.parametrize("missing", [{}, {"task-a": 2}, {"task-a\nmalformed": 1}])
def test_topup_batch_rejects_ambiguous_or_unsafe_selection(missing):
    with pytest.raises(ValueError):
        topup_config({"missing": missing}, 4)


@pytest.mark.parametrize("args", [
    ["--pilot"],
    ["--pilot", "--warmup", "environment", "--concurrency", "16"],
    ["--warmup-concurrency", "5"],
    ["--concurrency", "4", "--topup-concurrency", "5"],
])
def test_invalid_pilot_configuration_fails_before_cloud_calls(monkeypatch, args):
    monkeypatch.setattr(_aws_full, "repository_root", lambda: ROOT)
    monkeypatch.setattr(_aws_full, "require_environment", lambda: pytest.fail("must fail before accessing credentials"))
    with pytest.raises(RuntimeError):
        _aws_full.main(args)


def test_pilot_preserves_legacy_defaults_and_is_explicit():
    defaults = _aws_full.parse_args([])
    assert defaults.warmup == "legacy"
    assert defaults.topup_concurrency == 1
    assert not defaults.pilot
    args = _aws_full.parse_args(["--pilot", "--warmup", "environment", "--concurrency", "4"])
    assert args.pilot and not args.smoke
    source = REMOTE.read_text()
    assert 'ACTIVE_CONCURRENCY=1' in source
    assert 'config.pop("n_concurrent_trials", None)' in source
    assert '"pass_concurrencies"' in source
    assert '"performance_pilot"' in source
    assert '"$ROOT/performance.json"' in source
