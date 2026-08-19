import json

from stella_harbor.agent import bridge_stats
from stella_harbor.report import collect, reliability, render, wilson_interval


def write_trial(job_dir, task, harbor, adapter, run="2026-08-19__10-00-00", suffix="abc"):
    trial = job_dir / run / f"{task}__{suffix}"
    (trial / "agent" / "stella").mkdir(parents=True)
    (trial / "result.json").write_text(json.dumps(harbor))
    (trial / "agent" / "stella" / "result.json").write_text(json.dumps(adapter))


def passing(**changes):
    adapter = {"valid": True, "turn_terminal_state": "completed", "predicate_violations": [],
               "metrics": {"turns": 3, "tool_call_total": 2, "tool_error_total": 0,
                           "tokens_estimated": {"total": 450},
                           "tools": {"bash": {"calls": 2, "errors": 0, "total_ms": 3000, "max_ms": 2000}},
                           "timing_ms": {"total": 20000, "model": 12000, "tool": 5000},
                           "bridge": {"total_ms": 400}}}
    adapter.update(changes)
    return adapter


def test_report_reads_metrics_and_flags_invalid_trials(tmp_path):
    write_trial(tmp_path, "regex-log", {"verifier_result": {"rewards": {"reward": 1.0}}}, passing())
    write_trial(tmp_path, "broken", {"verifier_result": {"rewards": {"reward": 1.0}}},
                {"valid": False, "turn_terminal_state": "completed",
                 "predicate_violations": ["bridge nonce does not match"], "metrics": {}},
                suffix="def")

    out = render(collect(tmp_path))

    assert "regex-log" in out and "20.0s" in out and "450" in out
    assert "1/1 trials" in out  # the invalid trial leaves the denominator
    assert "1 trial(s) excluded as invalid" in out
    assert "bridge nonce does not match" in out


def test_report_survives_a_trial_that_raised_before_producing_metrics(tmp_path):
    write_trial(tmp_path, "crashed", {"exception_info": {"exception_type": "FileNotFoundError"}}, {})
    out = render(collect(tmp_path))
    assert "crashed" in out and "exception" in out and "no scoreable trials" in out


# A rewarded but invalid trial must never be counted; that is the whole point of
# the evidence predicate.
def test_invalid_trials_leave_the_denominator(tmp_path):
    write_trial(tmp_path, "t", {"verifier_result": {"rewards": {"reward": 1.0}}}, passing())
    write_trial(tmp_path, "t", {"verifier_result": {"rewards": {"reward": 1.0}}},
                passing(valid=False, predicate_violations=["turn shows no model activity"]), suffix="two")

    stats = reliability(collect(tmp_path))

    assert stats["scoreable"] == 1 and stats["invalid"] == 1
    assert stats["resolution_rate"] == 1.0


def test_pass_hat_k_requires_every_scoreable_trial_to_resolve(tmp_path):
    for i, reward in enumerate([1.0, 1.0, 0.0]):
        write_trial(tmp_path, "flaky", {"verifier_result": {"rewards": {"reward": reward}}}, passing(), suffix=f"s{i}")
    for i in range(3):
        write_trial(tmp_path, "solid", {"verifier_result": {"rewards": {"reward": 1.0}}}, passing(), suffix=f"r{i}")

    stats = reliability(collect(tmp_path))

    by_task = {t["task"]: t for t in stats["tasks"]}
    assert by_task["flaky"]["pass_hat_k"] is False
    assert by_task["solid"]["pass_hat_k"] is True
    assert stats["k"] == 3
    assert stats["pass_hat_k"] == 0.5
    assert "pass^3 50.0%" in render(collect(tmp_path))


def test_single_trial_runs_refuse_to_report_a_pass_hat_k(tmp_path):
    write_trial(tmp_path, "one", {"verifier_result": {"rewards": {"reward": 1.0}}}, passing())
    assert "pass^k unavailable" in render(collect(tmp_path))


# 5/5 must not claim a zero-width interval; that is the reason for Wilson.
def test_wilson_interval_stays_honest_at_the_extremes():
    low, high = wilson_interval(5, 5)
    assert 0.5 < low < 1.0 and high == 1.0
    low, high = wilson_interval(0, 5)
    assert low == 0.0 and 0.0 < high < 0.5
    assert wilson_interval(0, 0) == (0.0, 0.0)


def test_bridge_stats_group_operations_and_count_failures():
    stats = bridge_stats([
        {"op": "exec", "ok": True, "elapsed_ms": 100},
        {"op": "exec", "ok": False, "elapsed_ms": 300},
        {"op": "write_file", "ok": True, "elapsed_ms": 50},
    ])
    assert stats["total_ms"] == 450
    assert stats["operations"]["exec"] == {"calls": 2, "total_ms": 400, "max_ms": 300, "failures": 1}
