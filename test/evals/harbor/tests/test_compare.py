import json

from stella_harbor.compare import load, main, render, summarize
from stella_harbor.fingerprint import (
    collect_fingerprint,
    collect_fingerprint_details,
    fingerprint_mismatches,
)


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
        "agent_name": "stella_harbor.agent:StellaAgent",
        "tool_strategy": None,
        "capability_profile_digest": "capability-a",
        "candidate_commit": "commit-left",
    }
    issues = fingerprint_mismatches(fingerprint, collect_fingerprint(right))
    assert not any(issue["reject"] for issue in issues)
    assert main([str(left), str(right), "--names", "left", "right"]) == 0
    output = capsys.readouterr().out
    assert "left  vs  right" in output
    assert "SAME-AGENT" in output


def test_a_single_fingerprint_mismatch_is_rejected_with_both_values(tmp_path, capsys):
    left = write_fingerprinted_job(tmp_path, "left", n_concurrent_trials=16, candidate_commit="left")
    right = write_fingerprinted_job(tmp_path, "right", n_concurrent_trials=8, candidate_commit="right")

    assert main([str(left), str(right)]) == 2
    message = capsys.readouterr().err
    assert "REFUSING COMPARISON" in message
    assert "concurrency" in message
    assert "16" in message and "8" in message


def test_all_fingerprint_mismatches_are_reported(tmp_path, capsys):
    left = write_fingerprinted_job(
        tmp_path, "left", n_attempts=5, n_concurrent_trials=16,
        agent_timeout_multiplier=1.0, candidate_commit="left",
    )
    right = write_fingerprinted_job(
        tmp_path, "right", n_attempts=3, n_concurrent_trials=8,
        agent_timeout_multiplier=2.0, candidate_commit="right",
    )

    assert main([str(left), str(right)]) == 2
    message = capsys.readouterr().err
    assert "budget" in message and "concurrency" in message and "timeout_multiplier" in message
    assert "5" in message and "3" in message and "16" in message and "8" in message
    assert "1.0" in message and "2.0" in message


def test_both_missing_fields_are_rejected_as_unverifiable(tmp_path, capsys):
    agents = [{"name": "stella_harbor.agent:StellaAgent", "model_name": None}]
    left = write_fingerprinted_job(tmp_path, "left", agents=agents)
    right = write_fingerprinted_job(tmp_path, "right", agents=agents)

    assert main([str(left), str(right)]) == 2
    message = capsys.readouterr().err
    assert "CANNOT VERIFY CONFIGURATION:" in message
    assert "model" in message and "candidate_commit" in message
    assert "driver result.json: model" in message
    assert "driver result.json: candidate_commit" in message
    assert "CONFIGURATION DIFFERENT:" not in message


def test_missing_and_different_fields_are_reported_separately(tmp_path, capsys):
    agents = [{"name": "stella_harbor.agent:StellaAgent", "model_name": None}]
    left = write_fingerprinted_job(tmp_path, "left", agents=agents, candidate_commit="left")
    right = write_fingerprinted_job(tmp_path, "right", n_concurrent_trials=8, candidate_commit="right")

    assert main([str(left), str(right)]) == 2
    message = capsys.readouterr().err
    assert "CONFIGURATION DIFFERENT:" in message
    assert "CANNOT VERIFY CONFIGURATION:" in message
    assert "concurrency" in message and "model" in message
    assert "expected at driver result.json: model" in message


def test_missing_agent_fields_are_reported_but_do_not_block(tmp_path, capsys):
    left = write_fingerprinted_job(tmp_path, "left")
    right = write_fingerprinted_job(tmp_path, "right")

    assert main([str(left), str(right)]) == 0
    message = capsys.readouterr().out
    assert "AGENT IDENTITY INCOMPLETE (reported, not blocking):" in message
    assert "candidate_commit" in message and "tool_strategy" in message
    assert "CANNOT VERIFY CONFIGURATION:" not in message


def test_driver_result_fields_are_read_into_the_fingerprint(tmp_path):
    agents = [{"name": "stella_harbor.agent:StellaAgent", "model_name": None}]
    job = write_fingerprinted_job(tmp_path, "job", agents=agents)
    result_path = job / "2026-08-19__10-00-00" / "t__a" / "result.json"
    result = json.loads(result_path.read_text())
    result.update({"model": "gateway/actual", "candidate_commit": "driver-commit"})
    result_path.write_text(json.dumps(result))

    fingerprint = collect_fingerprint(job)

    assert fingerprint["model"] == "gateway/actual"
    assert fingerprint["candidate_commit"] == "driver-commit"


def test_same_agent_capability_difference_is_rejected(tmp_path, capsys):
    left = write_fingerprinted_job(tmp_path, "left", candidate_commit="left")
    right = write_fingerprinted_job(tmp_path, "right", candidate_commit="right")
    adapter_path = tmp_path / "right" / "2026-08-19__10-00-00" / "t__a" / "agent" / "stella" / "result.json"
    adapter_path.write_text(json.dumps({"capability_profile_digest": "capability-b"}))

    assert main([str(left), str(right)]) == 2
    message = capsys.readouterr().err
    assert "CONFIGURATION DIFFERENT:" in message
    assert "capability_profile_digest" in message


def test_cross_agent_comparison_passes_and_reports_both_identities(tmp_path, capsys):
    left = write_fingerprinted_job(tmp_path, "left", candidate_commit="left")
    right = write_fingerprinted_job(
        tmp_path,
        "right",
        agents=[{"name": "stella_harbor.pi_gateway:PiGateway", "model_name": "gateway/test"}],
        candidate_commit="right",
    )

    assert main([str(left), str(right)]) == 0
    message = capsys.readouterr().out
    assert "CROSS-AGENT COMPARISON" in message
    assert "stella_harbor.agent:StellaAgent" in message
    assert "stella_harbor.pi_gateway:PiGateway" in message


def test_cross_agent_condition_mismatch_is_rejected(tmp_path, capsys):
    left = write_fingerprinted_job(tmp_path, "left", candidate_commit="left")
    right = write_fingerprinted_job(
        tmp_path,
        "right",
        agents=[{"name": "stella_harbor.pi_gateway:PiGateway", "model_name": "other"}],
        candidate_commit="right",
    )

    assert main([str(left), str(right)]) == 2
    message = capsys.readouterr().err
    assert "CONFIGURATION DIFFERENT:" in message
    assert "model" in message


def test_partial_capability_coverage_is_reported(tmp_path, capsys):
    job = tmp_path / "partial"
    run = "2026-08-19__10-00-00"
    write_run_config(job, run, n_attempts=5, n_concurrent_trials=16, candidate_commit="commit")
    (job / run / "result.json").write_text(json.dumps({"n_total_trials": 5}))
    for index in range(5):
        adapter = {"capability_profile_digest": "capability-a"} if index < 2 else None
        write(job, run, f"task-{index}", 1.0, 0.01, adapter=adapter)

    details = collect_fingerprint_details(job)
    evidence = details["evidence"]["capability_profile_digest"]
    assert evidence["status"] == "partial"
    assert evidence["present"] == 2 and evidence["total"] == 5

    other = write_fingerprinted_job(tmp_path, "other", candidate_commit="commit")
    assert main([str(job), str(other)]) == 0
    message = capsys.readouterr().out
    assert "capability_profile_digest" in message and "[2/5]" in message


def test_internal_capability_inconsistency_blocks_and_is_reported(tmp_path, capsys):
    job = tmp_path / "inconsistent"
    run = "2026-08-19__10-00-00"
    write_run_config(job, run, n_total_trials=2, candidate_commit="commit")
    (job / run / "result.json").write_text(json.dumps({"n_total_trials": 2}))
    write(job, run, "task-a", 1.0, 0.01, adapter={"capability_profile_digest": "capability-a"})
    write(job, run, "task-b", 1.0, 0.01, adapter={"capability_profile_digest": "capability-b"})
    other = write_fingerprinted_job(tmp_path, "other", candidate_commit="commit")

    assert main([str(job), str(other)]) == 2
    message = capsys.readouterr().err
    assert "INTERNALLY INCONSISTENT RUN:" in message
    assert "capability_profile_digest" in message
    assert "[2/2]" in message


def test_allow_mismatch_renders_a_persistent_untrusted_marker(tmp_path, capsys):
    left = write_fingerprinted_job(tmp_path, "left", n_concurrent_trials=16, candidate_commit="left")
    right = write_fingerprinted_job(tmp_path, "right", n_concurrent_trials=8, candidate_commit="right")

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
