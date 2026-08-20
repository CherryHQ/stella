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
                           "usage": {"call_count": 3, "reported_call_count": 3, "priced_call_count": 3,
                                     "input_tokens": 1200, "output_tokens": 340,
                                     "cache_read_tokens": 900, "cache_write_tokens": 0,
                                     "cost_usd": 0.0123},
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

    assert "regex-log" in out and "20.0s" in out
    assert "1200" in out and "340" in out and "$0.0123" in out
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


def test_html_report_is_self_contained_and_escapes_task_names(tmp_path):
    from stella_harbor.htmlreport import render_html

    rows = [{
        "task": "<script>x</script>", "reward": 1.0, "valid": True, "state": "completed",
        "wall_ms": 1000, "model_ms": 600, "tool_ms": 300, "bridge_ms": 280, "turns": 2,
        "calls": 1, "tool_errors": 0, "est_tokens": 10, "timed_out": False,
        "tools": {"bash": {"calls": 1, "errors": 0, "total_ms": 300, "max_ms": 300}},
        "violations": [], "adapter_faults": [{"seq": 7, "op": "read_file", "path": "/etc/x", "code": "internal"}],
        "metrics": {"timing_ms": {"total": 1000, "model": 600, "tool": 300, "setup": 5},
                    "tools": {"bash": {"calls": 1, "errors": 0, "total_ms": 300, "max_ms": 300}},
                    "bridge": {"total_ms": 280, "operations": {"exec": {"calls": 1, "failures": 0, "total_ms": 280, "max_ms": 280}}}},
        "ledger": [{"seq": 1, "op": "exec", "command": "ls", "ok": True, "elapsed_ms": 280}],
    }]

    out = render_html(rows, "jobs/demo")

    assert "<script>x</script>" not in out  # escaped, not injected
    assert "&lt;script&gt;" in out
    assert "http://" not in out and "https://" not in out  # nothing fetched at open time
    assert "bridge adapter fault" in out
    assert "100.0%" in out


def test_html_report_survives_a_trial_with_no_adapter_result(tmp_path):
    from stella_harbor.htmlreport import render_html

    rows = [{
        "task": "boom", "reward": None, "valid": None, "state": "exception", "wall_ms": None,
        "model_ms": None, "tool_ms": None, "bridge_ms": None, "turns": None, "calls": None,
        "tool_errors": None, "est_tokens": None, "timed_out": None, "tools": {}, "usage": {},
        "violations": [], "adapter_faults": [], "metrics": {}, "ledger": [],
    }]

    out = render_html(rows, "jobs/demo")

    assert "no scoreable trials" in out


def test_a_trial_without_reported_usage_shows_no_cost_rather_than_zero(tmp_path):
    # A provider that reported nothing, or a model with no configured price, is
    # not free. Printing $0.00 there would understate a run's cost silently.
    adapter = passing()
    adapter["metrics"]["usage"] = {"call_count": 2, "reported_call_count": 0, "priced_call_count": 0,
                                   "input_tokens": None, "output_tokens": None, "cost_usd": None}
    write_trial(tmp_path, "unpriced", {"verifier_result": {"rewards": {"reward": 1.0}}}, adapter)

    out = render(collect(tmp_path))

    assert "$0.00" not in out
    assert "1 trial(s) have no cost" in out


def test_taxonomy_separates_the_three_failure_modes():
    from stella_harbor.taxonomy import classify

    finished_wrong = {"valid": True, "reward": 0.0, "state": "completed", "calls": 6, "tool_errors": 0}
    assert classify(finished_wrong)[0] == "verification"

    gave_up = {"valid": True, "reward": 0.0, "state": "completed", "calls": 0, "tool_errors": 0}
    assert classify(gave_up)[0] == "coherence"

    broke = {"valid": True, "reward": 0.0, "state": "errored", "calls": 3, "tool_errors": 1}
    assert classify(broke)[0] == "execution"

    ran_out = {"valid": True, "reward": 0.0, "state": "stopped", "calls": 4, "timed_out": True}
    assert classify(ran_out)[0] == "timeout"


def test_taxonomy_refuses_to_guess():
    # An unexplained failure has to stay visible. Folding it into whichever
    # bucket looks plausible is how a failure breakdown stops being evidence.
    from stella_harbor.taxonomy import classify

    label, why = classify({"valid": True, "reward": None, "state": "stopped", "calls": 2})
    assert label == "unclassified" and "no reward" in why


def test_a_resolved_trial_is_not_a_failure():
    from stella_harbor.taxonomy import breakdown

    rows = [{"valid": True, "reward": 1.0, "state": "completed", "calls": 2, "task": "t"}]
    assert [b["label"] for b in breakdown(rows)] == ["resolved"]


def test_html_report_explains_why_trials_failed():
    from stella_harbor.htmlreport import render_html

    row = {"task": "t", "reward": 0.0, "valid": True, "state": "completed", "wall_ms": 1000,
           "model_ms": 600, "tool_ms": 300, "bridge_ms": 280, "turns": 2, "calls": 6,
           "tool_errors": 0, "est_tokens": 10, "timed_out": False, "tools": {},
           "violations": [], "adapter_faults": [], "metrics": {}, "ledger": [], "usage": {}}

    out = render_html([row], "jobs/demo")

    assert "Why trials failed" in out
    assert "verification" in out
    assert "How to read this" in out


def test_taxonomy_does_not_call_a_probing_run_an_execution_failure():
    # Every tool call "errored" only because every command exited nonzero while
    # the agent probed the image. That is exploration, not machinery failing.
    from stella_harbor.taxonomy import classify

    row = {"valid": True, "reward": 0.0, "state": "completed", "calls": 4,
           "tool_errors": 4, "tool_faults": 0, "command_nonzero": 4}

    label, _ = classify(row)

    assert label == "verification"


def test_taxonomy_still_reports_execution_when_the_tools_really_failed():
    from stella_harbor.taxonomy import classify

    row = {"valid": True, "reward": 0.0, "state": "completed", "calls": 3,
           "tool_errors": 3, "tool_faults": 3, "command_nonzero": 0}

    label, why = classify(row)

    assert label == "execution"
    assert "3 tool calls failed" in why
