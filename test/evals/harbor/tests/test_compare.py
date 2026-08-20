import json

from stella_harbor.compare import load, render, summarize


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


def test_render_names_both_runs_and_every_task(tmp_path):
    a, b = tmp_path / "a", tmp_path / "b"
    write(a, "2026-08-19__10-00-00", "shared", 1.0, 0.01)
    (a / "2026-08-19__10-00-00" / "result.json").write_text("{}")
    write(b, "2026-08-19__10-00-00", "only-right", 0.0, 0.02)
    (b / "2026-08-19__10-00-00" / "result.json").write_text("{}")

    out = render(load(a), load(b), ("stella", "pi"))

    assert "shared" in out and "only-right" in out
    assert "stella" in out and "pi" in out
