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
                           "orchestration_tool_call_total": 2,
                           "execution_tool_call_total": 2,
                           "execution_tool_error_total": 0,
                           "execution_command_nonzero_total": 0,
                           "execution_tools": {"bash": {"calls": 2, "errors": 0, "command_nonzero": 0, "total_ms": 3000, "max_ms": 2000}},
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


def test_report_keeps_orchestration_and_execution_calls_distinct(tmp_path):
    adapter = passing()
    adapter["metrics"].update({"orchestration_tool_call_total": 1, "execution_tool_call_total": 4})
    write_trial(tmp_path, "code", {"verifier_result": {"rewards": {"reward": 1.0}}}, adapter)
    rows = collect(tmp_path)
    assert rows[0]["orchestration_calls"] == 1
    assert rows[0]["execution_calls"] == 4
    out = render(rows)
    assert "orch" in out and "exec" in out


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


def test_report_recounts_command_exits_for_a_job_that_predates_the_counts(tmp_path):
    """An old job kept its ledger; the counts must come back out of it."""
    write_trial(tmp_path, "probe", {"verifier_result": {"rewards": {"reward": 0.0}}}, {
        "valid": True,
        "turn_terminal_state": "stopped",
        "metrics": {
            "tool_call_total": 4,
            "tool_error_total": 3,
            # no command_nonzero here: this job ran before the driver counted it
            "bridge": {"total_ms": 10, "operations": {}, "adapter_faults": []},
        },
        "bridge_ledger": [
            {"op": "exec", "ok": True, "return_code": 1},
            {"op": "exec", "ok": True, "return_code": -1},
            {"op": "exec", "ok": True, "return_code": 0},
            {"op": "read", "ok": False, "code": "not_found"},
        ],
    })

    row = collect(tmp_path)[0]

    assert row["command_nonzero"] == 1
    assert row["command_timeout"] == 1
    assert row["tool_faults"] == 1


def test_report_falls_back_to_harbor_usage_for_non_stella_agents(tmp_path):
    """A pi trial has no Stella adapter file, but the provider still priced it."""
    trial = tmp_path / "2026-08-21__08-00-00" / "regex-log__xyz"
    trial.mkdir(parents=True)
    (trial / "result.json").write_text(json.dumps({
        "verifier_result": {"rewards": {"reward": 1.0}},
        "agent_result": {"n_input_tokens": 31000, "n_output_tokens": 480, "cost_usd": 0.0421},
    }))

    out = render(collect(tmp_path))

    assert "31000" in out and "480" in out and "$0.0421" in out
    assert "no cost" not in out
    # Harbor's reward is the only validity evidence such a trial has; dropping it
    # as "invalid" would report a complete run as unscoreable.
    assert "1/1 trials" in out and "invalid" not in out


def test_report_keeps_the_drivers_split_without_subtracting_twice(tmp_path):
    """A trial from the split driver is already correct; do not correct it again."""
    write_trial(tmp_path, "probe", {"verifier_result": {"rewards": {"reward": 0.0}}}, {
        "valid": True,
        "turn_terminal_state": "completed",
        "metrics": {
            "tool_call_total": 10,
            # already excludes the nonzero exits
            "tool_error_total": 2,
            "command_nonzero_total": 7,
            "tools": {"bash": {"calls": 8, "errors": 0, "command_nonzero": 7, "total_ms": 10, "max_ms": 5},
                      "edit": {"calls": 2, "errors": 2, "command_nonzero": 0, "total_ms": 4, "max_ms": 3}},
            "bridge": {"total_ms": 10, "operations": {}, "adapter_faults": [],
                       "command_nonzero": 7, "command_timeout": 0},
        },
    })

    row = collect(tmp_path)[0]

    assert row["command_nonzero_total"] == 7
    assert row["tool_errors"] == 2
    # Not 2 - 7 clamped to 0: the driver already took the exits out.
    assert row["tool_faults"] == 2

    out = render([row])
    assert "cmd!0" in out
    assert "7 command(s) that ran and exited nonzero" in out


def test_report_shows_a_dash_when_the_trial_predates_the_split(tmp_path):
    """None is not zero: an old trial never measured the new field."""
    write_trial(tmp_path, "legacy", {"verifier_result": {"rewards": {"reward": 0.0}}}, {
        "valid": True,
        "turn_terminal_state": "completed",
        "metrics": {
            "tool_call_total": 4,
            "tool_error_total": 3,
            "tools": {"bash": {"calls": 4, "errors": 3, "total_ms": 10, "max_ms": 5}},
            "bridge": {"total_ms": 10, "operations": {}, "adapter_faults": []},
        },
        "bridge_ledger": [{"op": "exec", "ok": True, "return_code": 1}],
    })

    rows = collect(tmp_path)

    assert rows[0]["command_nonzero_total"] is None
    # Unchanged behavior for archived data: the ledger recount is still applied.
    assert rows[0]["tool_faults"] == 2

    out = render(rows)
    trial_line = next(line for line in out.splitlines() if line.startswith("legacy"))
    # errs then cmd!0: three real errors, and no claim at all about the split.
    assert " 3     -  " in trial_line
    assert "predate the split" in out


def test_taxonomy_reads_the_drivers_split_count_directly():
    from stella_harbor.taxonomy import classify

    # Every call failed, all of them tool faults: still an execution failure.
    label, why = classify({"valid": True, "reward": 0.0, "state": "completed", "calls": 3,
                           "tool_errors": 3, "tool_faults": 3, "command_nonzero_total": 0})
    assert label == "execution"
    assert "3 tool calls failed" in why

    # Same trial shape, but the failures were commands exiting nonzero, which
    # the driver already kept out of tool_error_total.
    label, _ = classify({"valid": True, "reward": 0.0, "state": "completed", "calls": 3,
                         "tool_errors": 0, "tool_faults": 0, "command_nonzero_total": 3})
    assert label == "verification"


def test_report_will_not_total_a_tool_column_one_trial_never_measured(tmp_path):
    """Mixed old and new trials: a partial sum is not the total."""
    write_trial(tmp_path, "new", {"verifier_result": {"rewards": {"reward": 0.0}}}, {
        "valid": True, "turn_terminal_state": "completed",
        "metrics": {
            "tool_call_total": 2, "tool_error_total": 0, "command_nonzero_total": 1,
            "tools": {"bash": {"calls": 2, "errors": 0, "command_nonzero": 1, "total_ms": 10, "max_ms": 5},
                      "edit": {"calls": 1, "errors": 1, "command_nonzero": 0, "total_ms": 2, "max_ms": 2}},
            "bridge": {"total_ms": 10, "operations": {}, "adapter_faults": []},
        },
    })
    write_trial(tmp_path, "old", {"verifier_result": {"rewards": {"reward": 0.0}}}, {
        "valid": True, "turn_terminal_state": "completed",
        "metrics": {
            "tool_call_total": 3, "tool_error_total": 2,
            # pre-split: bash carries no per-tool command_nonzero at all
            "tools": {"bash": {"calls": 3, "errors": 2, "total_ms": 20, "max_ms": 9}},
            "bridge": {"total_ms": 20, "operations": {}, "adapter_faults": []},
        },
        "bridge_ledger": [{"op": "exec", "ok": True, "return_code": 1}],
    }, suffix="def")

    out = render(collect(tmp_path))

    bash_line = next(line for line in out.splitlines() if line.startswith("bash "))
    # 5 calls, 2 errors, and no cmd!0 total: one trial never measured it, so
    # the new trial's 1 is not the column's answer.
    assert bash_line.split()[:4] == ["bash", "5", "2", "-"]
    # edit was only ever seen by the split trial, so its count is knowable.
    edit_line = next(line for line in out.splitlines() if line.startswith("edit "))
    assert edit_line.split()[:4] == ["edit", "1", "1", "0"]
    # Both notes appear: the job genuinely holds trials of both kinds.
    assert "1 command(s) that ran and exited nonzero" in out
    assert "1 trial(s) predate the split" in out


def test_report_does_not_call_a_non_stella_trial_pre_split(tmp_path):
    """A pi trial has no Stella counters at all; it has nothing to correct."""
    pi = tmp_path / "2026-08-21__08-00-00" / "regex-log__xyz"
    pi.mkdir(parents=True)
    (pi / "result.json").write_text(json.dumps({
        "verifier_result": {"rewards": {"reward": 1.0}},
        "agent_result": {"n_input_tokens": 100, "n_output_tokens": 10, "cost_usd": 0.001},
    }))
    write_trial(tmp_path, "old", {"verifier_result": {"rewards": {"reward": 0.0}}}, {
        "valid": True, "turn_terminal_state": "completed",
        "metrics": {
            "tool_call_total": 3, "tool_error_total": 2,
            "tools": {"bash": {"calls": 3, "errors": 2, "total_ms": 20, "max_ms": 9}},
            "bridge": {"total_ms": 20, "operations": {}, "adapter_faults": []},
        },
        "bridge_ledger": [{"op": "exec", "ok": True, "return_code": 1}],
    }, run="2026-08-21__09-00-00", suffix="def")

    out = render(collect(tmp_path))

    # One trial predates the split; the pi trial is not counted among them.
    assert "1 trial(s) predate the split" in out


def _plain_row(task="alpha", reward=1.0):
    return {"task": task, "reward": reward, "valid": True, "state": "completed",
            "wall_ms": 1000, "model_ms": 600, "tool_ms": 300, "bridge_ms": 280, "turns": 2,
            "calls": 1, "tool_errors": 0, "est_tokens": 10, "timed_out": False,
            "tools": {"bash": {"calls": 1, "errors": 0, "total_ms": 300, "max_ms": 300}},
            "violations": [], "adapter_faults": [],
            "metrics": {"timing_ms": {"total": 1000, "model": 600, "tool": 300}},
            "ledger": []}


def _run(date, agent, resolution, resolved, harness="code-mode", **extra):
    run = {"date": date, "agent": agent, "resolution": resolution, "resolved": resolved,
           "scoreable": 445, "benchmark": "terminal-bench-2.1", "model": "gpt-5.6-luna",
           "k": 5, "harness": harness, "host": "AWS c7i.8xlarge", "pass_k": 0.3,
           "cost_usd": 6.8}
    run.update(extra)
    return run


def _write_timeline(path, runs):
    import csv

    fields = ["date", "agent", "label", "benchmark", "model", "k", "harness", "host",
              "resolved", "scoreable", "resolution", "pass_k", "cost_usd"]
    with path.open("w", newline="") as handle:
        writer = csv.DictWriter(handle, fields, extrasaction="ignore")
        writer.writeheader()
        for run in runs:
            writer.writerow(run)
    return path


def test_timeline_ignores_a_missing_history(tmp_path):
    from stella_harbor import timeline

    assert timeline.load(tmp_path / "absent.csv") == []


def test_timeline_leaves_an_unmeasured_field_empty_rather_than_zero(tmp_path):
    from stella_harbor import timeline

    path = tmp_path / "timeline.csv"
    path.write_text("date,agent,resolution,cost_usd\n2026-08-20,Stella,0.474,\n")
    run = timeline.load(path)[0]
    assert run["resolution"] == 0.474
    assert run["cost_usd"] is None


def test_timeline_orders_oldest_first_and_keeps_peers_out_by_default(tmp_path):
    from stella_harbor import timeline

    path = _write_timeline(tmp_path / "timeline.csv", [
        _run("2026-08-31", "Stella", 0.528, 235),
        _run("2026-08-20", "Stella", 0.474, 211, harness="bash-only"),
        _run("2026-08-21", "Pi", 0.582, 259, harness="pi-native"),
    ])
    runs = timeline.load(path)
    assert [r["date"] for r in runs] == ["2026-08-20", "2026-08-21", "2026-08-31"]
    assert [r["agent"] for r in timeline.select(runs)] == ["Stella", "Stella"]
    assert [r["agent"] for r in timeline.select(runs, peers=True)] == ["Stella", "Pi", "Stella"]
    assert timeline.latest_subject(runs)["date"] == "2026-08-31"
    assert timeline.previous_subject(runs)["date"] == "2026-08-20"


def test_a_peer_agent_never_reaches_the_report_unless_asked_for(tmp_path):
    from stella_harbor.htmlreport import render_html

    rows = [_plain_row()]
    history = [_run("2026-08-20", "Stella", 0.474, 211, harness="bash-only"),
               _run("2026-08-21", "Pilot", 0.582, 259, harness="pi-native"),
               _run("2026-08-31", "Stella", 0.528, 235)]
    assert "Pilot" not in render_html(rows, "jobs/demo", history)
    assert "Pilot" in render_html(rows, "jobs/demo", history, peers=True)


def test_the_trend_marks_an_incomparable_release_gap_as_dashed(tmp_path):
    from stella_harbor.htmlreport import render_html

    rows = [_plain_row()]
    changed = [_run("2026-08-20", "Stella", 0.474, 211, harness="bash-only"),
               _run("2026-08-31", "Stella", 0.528, 235)]
    out = render_html(rows, "jobs/demo", changed)
    assert "stroke-dasharray" in out
    assert "descriptive context" in out
    assert "+5.4pp" in out and "configuration changed" in out

    matched = [_run("2026-08-20", "Stella", 0.474, 211),
               _run("2026-08-31", "Stella", 0.528, 235)]
    out = render_html(rows, "jobs/demo", matched)
    assert "stroke-dasharray" not in out
    assert "matched configuration" in out


def test_a_report_without_history_still_renders_and_says_so(tmp_path):
    from stella_harbor.htmlreport import render_html

    out = render_html([_plain_row()], "jobs/demo")
    assert "<svg" not in out
    assert "no earlier Stella run" in out


def test_csv_keeps_raw_values_and_leaves_unmeasured_fields_empty(tmp_path):
    import csv

    from stella_harbor.report import write_csv

    rows = [_plain_row(), _plain_row(task="beta", reward=0.0)]
    rows[0]["usage"] = {"input_tokens": 1200, "output_tokens": 340, "cost_usd": 0.0123}
    rows[0]["command_nonzero_total"] = 4
    rows[1]["usage"] = {}  # the provider never priced this trial

    trials_path, tasks_path = write_csv(rows, tmp_path / "out")
    written = list(csv.DictReader(trials_path.open(newline="")))

    assert [r["task"] for r in written] == ["alpha", "beta"]
    # milliseconds, not "1.0s": a formatted duration is a rendering, not a value
    assert written[0]["wall_ms"] == "1000"
    assert written[0]["cost_usd"] == "0.0123"
    assert written[0]["command_nonzero_total"] == "4"
    assert written[1]["cost_usd"] == ""  # never priced, and never $0
    assert written[1]["command_nonzero_total"] == ""

    tasks = list(csv.DictReader(tasks_path.open(newline="")))
    assert {t["task"] for t in tasks} == {"alpha", "beta"}


def test_the_html_view_leaves_out_per_trial_detail_unless_asked(tmp_path):
    from stella_harbor.htmlreport import render_html

    rows = [_plain_row()]
    lean = render_html(rows, "jobs/demo")
    assert "trials.csv" in lean and "<details>" not in lean
    assert "<details>" in render_html(rows, "jobs/demo", detail=True)
