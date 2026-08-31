import json
from pathlib import Path

from stella_harbor.aws_merge import EXPECTED_TASKS, inventory, merge

ROOT = Path(__file__).parents[4]
REMOTE = ROOT / "test/evals/harbor/aws_runner.sh"
TASK = ROOT / ".mise/tasks/eval/tb21/aws"


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


def test_merge_selects_first_k_scoreable_without_selecting_on_outcome(tmp_path: Path):
    source = tmp_path / "jobs"
    for number in range(EXPECTED_TASKS):
        task = f"task-{number:02d}"
        trial(source, "pass-01", task, "p1", valid=True, reward=0.0)
        trial(source, "pass-02", task, "p2", valid=True, reward=1.0)
    # This invalid trial sorts before the reportable passes and must be ignored.
    trial(source, "pass-00", "task-00", "invalid", valid=False, reward=None)

    state = inventory(source, 2)
    assert state["tasks"] == EXPECTED_TASKS
    assert state["invalid"] == 1
    assert state["missing"] == {}

    output = tmp_path / "merged"
    result = merge(source, output, 2)
    assert result["copied"] == EXPECTED_TASKS * 2
    selected = sorted(output.glob("*/*/result.json"))
    assert len(selected) == EXPECTED_TASKS * 2
    task_zero = [json.loads(path.read_text())["marker"] for path in selected if "task-00__" in str(path)]
    assert task_zero == ["p1", "p2"]


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
    assert "unset OTEL_STELLA_RECORD_TOOL_IO" in source
    assert "warmup-discarded" in source
    assert "systemctl start --no-block stella-tb21.service" in (ROOT / "test/evals/harbor/aws_full.py").read_text()
    assert "shutdown -h" in source


def test_mise_task_is_the_single_entrypoint():
    source = TASK.read_text()
    assert "source ./.env" in source
    assert 'exec python3 test/evals/harbor/aws_full.py "$@"' in source
