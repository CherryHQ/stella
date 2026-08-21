import json

from stella_harbor.compare import load, main, render, summarize
from stella_harbor.fingerprint import collect_fingerprint, fingerprint_mismatches


def write(job, run, task, reward, cost, suffix="a", adapter=None):
    trial = job / run / f"{task}__{suffix}"
    trial.mkdir(parents=True)
    (trial / "result.json").write_text(json.dumps({
        "verifier_result": {"rewards": {"reward": reward}},
        "agent_result": {"cost_usd": cost, "n_input_tokens": 10, "n_output_tokens": 5},
    }))
    if adapter is not None:
        (trial / "agent" / "stella").mkdir(parents=True)
        (trial / "agent" / "stella" / "result.json").write_text(json.dumps(adapter))


def write_run_config(job, run, **overrides):
    config = {
        "n_attempts": 5,
        "n_concurrent_trials": 16,
        "agent_timeout_multiplier": 1.0,
        "agents": [{"name": "stella_harbor.agent:StellaAgent", "model_name": "gateway/test"}],
        "datasets": [{"name": "terminal-bench/test", "ref": "sha256:dataset"}],
        **overrides,
    }
    path = job / run
    path.mkdir(parents=True, exist_ok=True)
    (path / "config.json").write_text(json.dumps(config))


def write_fingerprinted_job(tmp_path, name, **overrides):
    job = tmp_path / name
    run = "2026-08-19__10-00-00"
    write_run_config(job, run, **overrides)
    (job / run / "result.json").write_text("{}")
    write(
        job,
        run,
        "t",
        1.0,
        0.01,
        adapter={"capability_profile_digest": "capability-a"},
    )
    (job / run / "t__a" / "result.json").write_text(json.dumps({
        "verifier_result": {"rewards": {"reward": 1.0}},
        "agent_result": {"cost_usd": 0.01, "n_input_tokens": 10, "n_output_tokens": 5},
    }))
    return job


def test_compare_reads_a_job_without_the_stella_adapter(tmp_path):
    job = tmp_path / "pi"
    write(job, "2026-08-19__10-00-00", "regex-log", 1.0, 0.02)
    (job / "2026-08-19__10-00-00" / "result.json").write_text("{}")

    rows = load(job)

    assert rows == [{"task": "regex-log", "reward": 1.0, "cost_usd": 0.02,
                     "input_tokens": 10, "output_tokens": 5, "valid": None}]
    assert summarize(rows)["resolved"] == 1


# An agent with no evidence contract must not be scored as if it failed one.
def test_a_missing_adapter_result_is_not_an_invalid_trial(tmp_path):
    job = tmp_path / "pi"
    write(job, "2026-08-19__10-00-00", "t", 1.0, 0.01)
    (job / "2026-08-19__10-00-00" / "result.json").write_text("{}")
    assert summarize(load(job))["invalid"] == 0


def test_an_invalid_stella_trial_still_leaves_the_denominator(tmp_path):
    job = tmp_path / "stella"
    write(job, "2026-08-19__10-00-00", "t", 1.0, 0.01, adapter={"valid": False})
    (job / "2026-08-19__10-00-00" / "result.json").write_text("{}")
    stats = summarize(load(job))
    assert stats["invalid"] == 1 and stats["scoreable"] == 0


def test_matching_fingerprints_are_comparable(tmp_path, capsys):
    left = write_fingerprinted_job(tmp_path, "left", candidate_commit="commit-left")
    right = write_fingerprinted_job(tmp_path, "right", candidate_commit="commit-right")

    fingerprint = collect_fingerprint(left)
    assert fingerprint == {
        "dataset_id": "terminal-bench/test",
        "dataset_hash": "sha256:dataset",
        "model": "gateway/test",
        "budget": 5,
        "concurrency": 16,
        "timeout_multiplier": 1.0,
        "tool_strategy": "stella_harbor.agent:StellaAgent",
        "capability_profile_digest": "capability-a",
        "candidate_commit": "commit-left",
    }
    assert fingerprint_mismatches(fingerprint, collect_fingerprint(right)) == []
    assert main([str(left), str(right), "--names", "left", "right"]) == 0
    assert "left  vs  right" in capsys.readouterr().out


def test_a_single_fingerprint_mismatch_is_rejected_with_both_values(tmp_path, capsys):
    left = write_fingerprinted_job(tmp_path, "left", n_concurrent_trials=16)
    right = write_fingerprinted_job(tmp_path, "right", n_concurrent_trials=8)

    assert main([str(left), str(right)]) == 2
    message = capsys.readouterr().err
    assert "REFUSING COMPARISON" in message
    assert "concurrency" in message
    assert "16" in message and "8" in message


def test_all_fingerprint_mismatches_are_reported(tmp_path, capsys):
    left = write_fingerprinted_job(
        tmp_path, "left", n_attempts=5, n_concurrent_trials=16,
        agent_timeout_multiplier=1.0,
    )
    right = write_fingerprinted_job(
        tmp_path, "right", n_attempts=3, n_concurrent_trials=8,
        agent_timeout_multiplier=2.0,
    )

    assert main([str(left), str(right)]) == 2
    message = capsys.readouterr().err
    assert "budget" in message and "concurrency" in message and "timeout_multiplier" in message
    assert "5" in message and "3" in message and "16" in message and "8" in message
    assert "1.0" in message and "2.0" in message


def test_allow_mismatch_renders_a_persistent_untrusted_marker(tmp_path, capsys):
    left = write_fingerprinted_job(tmp_path, "left", n_concurrent_trials=16)
    right = write_fingerprinted_job(tmp_path, "right", n_concurrent_trials=8)

    assert main([str(left), str(right), "--allow-mismatch"]) == 0
    output = capsys.readouterr().out
    assert "[UNTRUSTWORTHY COMPARISON]" in output
    assert output.count("[UNTRUSTWORTHY COMPARISON]") >= 6
    assert "concurrency" in output and "16" in output and "8" in output


def test_render_names_both_runs_and_every_task(tmp_path):
    a, b = tmp_path / "a", tmp_path / "b"
    write(a, "2026-08-19__10-00-00", "shared", 1.0, 0.01)
    (a / "2026-08-19__10-00-00" / "result.json").write_text("{}")
    write(b, "2026-08-19__10-00-00", "only-right", 0.0, 0.02)
    (b / "2026-08-19__10-00-00" / "result.json").write_text("{}")

    out = render(load(a), load(b), ("stella", "pi"))

    assert "shared" in out and "only-right" in out
    assert "stella" in out and "pi" in out
