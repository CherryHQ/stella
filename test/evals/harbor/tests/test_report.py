import json

from stella_harbor.agent import bridge_stats
from stella_harbor.report import collect, render


def write_trial(job_dir, task, harbor, adapter):
    trial = job_dir / "2026-08-19__10-00-00" / f"{task}__abc"
    (trial / "agent" / "stella").mkdir(parents=True)
    (trial / "result.json").write_text(json.dumps(harbor))
    (trial / "agent" / "stella" / "result.json").write_text(json.dumps(adapter))


def test_report_reads_metrics_and_flags_invalid_trials(tmp_path):
    write_trial(tmp_path, "regex-log",
                {"verifier_result": {"rewards": {"reward": 1.0}}},
                {"valid": True, "turn_terminal_state": "completed", "predicate_violations": [],
                 "metrics": {"turns": 3, "tool_call_total": 2, "tool_error_total": 0,
                             "tokens": {"total": 450}, "tools": {"bash": {"calls": 2, "errors": 0, "total_ms": 3000, "max_ms": 2000}},
                             "timing_ms": {"total": 20000, "model": 12000, "tool": 5000},
                             "bridge": {"total_ms": 400}}})
    write_trial(tmp_path, "broken",
                {"verifier_result": {"rewards": {"reward": 1.0}}},
                {"valid": False, "turn_terminal_state": "completed",
                 "predicate_violations": ["bridge nonce does not match"], "metrics": {}})

    out = render(collect(tmp_path))

    assert "regex-log" in out and "20.0s" in out and "450" in out
    # A rewarded but invalid trial must never be counted as scoreable.
    assert "1/2 trials scoreable" in out
    assert "bridge nonce does not match" in out


def test_report_survives_a_trial_that_raised_before_producing_metrics(tmp_path):
    write_trial(tmp_path, "crashed", {"exception_info": {"exception_type": "FileNotFoundError"}}, {})
    out = render(collect(tmp_path))
    assert "crashed" in out and "exception" in out and "0/1 trials scoreable" in out


def test_bridge_stats_group_operations_and_count_failures():
    stats = bridge_stats([
        {"op": "exec", "ok": True, "elapsed_ms": 100},
        {"op": "exec", "ok": False, "elapsed_ms": 300},
        {"op": "write_file", "ok": True, "elapsed_ms": 50},
    ])
    assert stats["total_ms"] == 450
    assert stats["operations"]["exec"] == {"calls": 2, "total_ms": 400, "max_ms": 300, "failures": 1}
