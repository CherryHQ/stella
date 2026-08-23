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


def write_fingerprinted_job(tmp_path, name, *, tool_strategy="native", **overrides):
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
        adapter={
            "capability_profile_digest": "capability-a", "excluded_tools": ["view_image", "vllm"],
            "tool_strategy": tool_strategy, "candidate_commit": overrides.get("candidate_commit"),
            "gateway_endpoint": "https://gateway.example.test/v1", "provider_type": "openai-response",
            "model_price_digest": "price-a", "execution_capability": ["bash"],
        },
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

    assert len(rows) == 1
    row = rows[0]
    assert (row["task"], row["reward"], row["cost_usd"]) == ("regex-log", 1.0, 0.02)
    assert (row["input_tokens"], row["output_tokens"]) == (10, 5)
    # No adapter evidence at all, so the verifier reward is the validity fallback.
    assert row["valid"] is True and row["adapter"] is False
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
        "gateway_endpoint": "https://gateway.example.test/v1",
        "provider_type": "openai-response",
        "model_price_digest": "price-a",
        "agent_name": "stella_harbor.agent:StellaAgent",
        "tool_strategy": "native",
        "excluded_tools": ["view_image", "vllm"],
        "capability_profile_digest": "capability-a",
        "candidate_commit": "commit-left",
        "execution_capability": ["bash"],
    }
    issues = fingerprint_mismatches(fingerprint, collect_fingerprint(right))
    assert not any(issue["reject"] for issue in issues)
    assert main([str(left), str(right), "--names", "left", "right"]) == 0
    output = capsys.readouterr().out
    assert "candidate left  vs  reference right" in output
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
    assert "adapter result.json: candidate_commit" in message
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
    assert "candidate_commit" in message and "tool_strategy" not in message
    assert "CANNOT VERIFY CONFIGURATION:" not in message


def test_driver_result_fields_are_read_into_the_fingerprint(tmp_path):
    agents = [{"name": "stella_harbor.agent:StellaAgent", "model_name": None}]
    job = write_fingerprinted_job(tmp_path, "job", agents=agents)
    result_path = job / "2026-08-19__10-00-00" / "t__a" / "result.json"
    result = json.loads(result_path.read_text())
    result.update({"model": "gateway/actual"})
    result_path.write_text(json.dumps(result))
    adapter_path = job / "2026-08-19__10-00-00" / "t__a" / "agent" / "stella" / "result.json"
    adapter = json.loads(adapter_path.read_text())
    adapter["candidate_commit"] = "driver-commit"
    adapter_path.write_text(json.dumps(adapter))

    fingerprint = collect_fingerprint(job)

    assert fingerprint["model"] == "gateway/actual"
    assert fingerprint["candidate_commit"] == "driver-commit"


def test_root_candidate_commit_only_cross_checks_the_driver_attestation(tmp_path, capsys):
    job = write_fingerprinted_job(tmp_path, "job", candidate_commit="driver-commit")
    write_run_config(job, RUN, candidate_commit="caller-claimed-commit")

    details = collect_fingerprint_details(job)
    assert details["fingerprint"]["candidate_commit"] == "driver-commit"
    assert details["evidence"]["candidate_commit"]["status"] == "inconsistent"
    other = write_fingerprinted_job(tmp_path, "other", candidate_commit="driver-commit")
    assert main([str(job), str(other)]) == 2
    assert "candidate_commit" in capsys.readouterr().err


def test_candidate_commit_missing_from_driver_is_not_filled_from_root_config(tmp_path):
    job = write_fingerprinted_job(tmp_path, "job", candidate_commit="commit")
    adapter_path = job / RUN / "t__a" / "agent" / "stella" / "result.json"
    payload = json.loads(adapter_path.read_text())
    payload.pop("candidate_commit")
    adapter_path.write_text(json.dumps(payload))

    details = collect_fingerprint_details(job)
    assert details["fingerprint"]["candidate_commit"] is None
    assert details["evidence"]["candidate_commit"]["status"] == "missing"


def test_absent_excluded_tools_matches_explicit_empty_list(tmp_path, capsys):
    left = write_fingerprinted_job(tmp_path, "left", candidate_commit="left")
    right = write_fingerprinted_job(tmp_path, "right", candidate_commit="right")
    adapter_path = tmp_path / "left" / "2026-08-19__10-00-00" / "t__a" / "agent" / "stella" / "result.json"
    adapter = json.loads(adapter_path.read_text())
    adapter.pop("excluded_tools")
    adapter_path.write_text(json.dumps(adapter))

    details = collect_fingerprint_details(left)
    assert details["fingerprint"]["excluded_tools"] is None
    assert details["evidence"]["excluded_tools"]["status"] == "missing"
    assert main([str(left), str(right)]) == 0
    assert "excluded_tools" in capsys.readouterr().out


def test_absent_excluded_tools_differs_from_nonempty_list(tmp_path, capsys):
    left = write_fingerprinted_job(tmp_path, "left", candidate_commit="left")
    right = write_fingerprinted_job(tmp_path, "right", candidate_commit="right")
    left_adapter_path = tmp_path / "left" / "2026-08-19__10-00-00" / "t__a" / "agent" / "stella" / "result.json"
    left_adapter = json.loads(left_adapter_path.read_text())
    left_adapter.pop("excluded_tools")
    left_adapter_path.write_text(json.dumps(left_adapter))
    right_adapter_path = tmp_path / "right" / "2026-08-19__10-00-00" / "t__a" / "agent" / "stella" / "result.json"
    right_adapter = json.loads(right_adapter_path.read_text())
    right_adapter["excluded_tools"] = ["edit", "read", "write"]
    right_adapter_path.write_text(json.dumps(right_adapter))

    assert main([str(left), str(right)]) == 0
    message = capsys.readouterr().out
    assert "AGENT IDENTITY INCOMPLETE" in message
    assert "excluded_tools" in message


def test_same_agent_excluded_tools_difference_is_rejected(tmp_path, capsys):
    left = write_fingerprinted_job(tmp_path, "left", candidate_commit="left")
    right = write_fingerprinted_job(tmp_path, "right", candidate_commit="right")
    adapter_path = tmp_path / "right" / "2026-08-19__10-00-00" / "t__a" / "agent" / "stella" / "result.json"
    adapter = json.loads(adapter_path.read_text())
    adapter["excluded_tools"] = ["edit", "read", "write"]
    adapter_path.write_text(json.dumps(adapter))

    assert main([str(left), str(right)]) == 2
    message = capsys.readouterr().err
    assert "CONFIGURATION DIFFERENT:" in message
    assert "excluded_tools" in message
    assert '["edit", "read", "write"]' in message


def test_same_agent_capability_difference_is_rejected(tmp_path, capsys):
    left = write_fingerprinted_job(tmp_path, "left", candidate_commit="left")
    right = write_fingerprinted_job(tmp_path, "right", candidate_commit="right")
    adapter_path = tmp_path / "right" / "2026-08-19__10-00-00" / "t__a" / "agent" / "stella" / "result.json"
    adapter_path.write_text(json.dumps({"capability_profile_digest": "capability-b"}))

    assert main([str(left), str(right)]) == 2
    message = capsys.readouterr().err
    assert "CONFIGURATION DIFFERENT:" in message
    assert "capability_profile_digest" in message


def test_native_code_treatment_requires_explicit_flag_and_is_visible(tmp_path, capsys):
    native = write_fingerprinted_job(tmp_path, "native", candidate_commit="same-commit", tool_strategy="native")
    code = write_fingerprinted_job(tmp_path, "code", candidate_commit="same-commit", tool_strategy="code")

    assert main([str(code), str(native)]) == 2
    assert "tool_strategy" in capsys.readouterr().err

    assert main([str(code), str(native), "--vary-tool-strategy", "--confirm"]) == 0
    output = capsys.readouterr().out
    assert "TRUSTED TREATMENT ACTIVE" in output
    assert "TRUSTED TREATMENT:" in output
    assert "UNTRUSTWORTHY" not in output


def test_tool_strategy_treatment_requires_equal_commit_and_gateway_identity(tmp_path, capsys):
    native = write_fingerprinted_job(tmp_path, "native", candidate_commit="commit-a", tool_strategy="native")
    code = write_fingerprinted_job(tmp_path, "code", candidate_commit="commit-b", tool_strategy="code")
    assert main([str(code), str(native), "--vary-tool-strategy"]) == 2
    assert "candidate_commit" in capsys.readouterr().err

    code_adapter = code / RUN / "t__a" / "agent" / "stella" / "result.json"
    payload = json.loads(code_adapter.read_text())
    payload["gateway_endpoint"] = "https://other-gateway.example.test/v1"
    code_adapter.write_text(json.dumps(payload))
    # Restore the commit so this assertion isolates server-reported gateway
    # evidence rather than caller-visible Harbor configuration.
    write_run_config(code, RUN, candidate_commit="commit-a")
    assert main([str(code), str(native), "--vary-tool-strategy"]) == 2
    assert "gateway_endpoint" in capsys.readouterr().err


def test_tool_strategy_treatment_rejects_partial_or_mixed_strategy_without_type_error(tmp_path, capsys):
    native = write_fingerprinted_job(tmp_path, "native", candidate_commit="same", tool_strategy="native")
    code = write_fingerprinted_job(tmp_path, "code", candidate_commit="same", tool_strategy="code")
    topup = write_fingerprinted_job(tmp_path, "code-topup", candidate_commit="same", tool_strategy="code")
    topup_adapter = topup / RUN / "t__a" / "agent" / "stella" / "result.json"
    payload = json.loads(topup_adapter.read_text())
    payload.pop("tool_strategy")
    topup_adapter.write_text(json.dumps(payload))
    assert main([str(code), str(native), "--candidate-job", str(topup), "--vary-tool-strategy"]) == 2
    assert "code-topup:tool_strategy" in capsys.readouterr().err

    mixed = tmp_path / "mixed"
    write_run_config(mixed, RUN, n_attempts=2, candidate_commit="same")
    (mixed / RUN / "result.json").write_text(json.dumps({"n_total_trials": 2}))
    for suffix, strategy in (("a", "native"), ("b", "code")):
        write(mixed, RUN, "t", 1.0, 0.01, suffix=suffix, adapter={
            "capability_profile_digest": "capability-a", "excluded_tools": [], "tool_strategy": strategy,
            "gateway_endpoint": "https://gateway.example.test/v1", "provider_type": "openai-response", "model_price_digest": "price-a", "execution_capability": ["bash"],
        })
    assert main([str(mixed), str(code), "--vary-tool-strategy"]) == 2
    assert "INTERNALLY INCONSISTENT RUN" in capsys.readouterr().err


def test_tool_strategy_treatment_rejects_missing_unknown_or_cross_agent_evidence(tmp_path, capsys):
    native = write_fingerprinted_job(tmp_path, "native", candidate_commit="native", tool_strategy="native")
    missing = write_fingerprinted_job(tmp_path, "missing", candidate_commit="missing", tool_strategy="code")
    missing_adapter = missing / RUN / "t__a" / "agent" / "stella" / "result.json"
    payload = json.loads(missing_adapter.read_text())
    payload.pop("tool_strategy")
    missing_adapter.write_text(json.dumps(payload))
    assert main([str(native), str(missing), "--vary-tool-strategy"]) == 2
    assert "TOOL-STRATEGY TREATMENT REJECTED" in capsys.readouterr().err

    unknown = write_fingerprinted_job(tmp_path, "unknown", candidate_commit="unknown", tool_strategy="bogus")
    assert main([str(native), str(unknown), "--vary-tool-strategy"]) == 2

    same = write_fingerprinted_job(tmp_path, "same", candidate_commit="same", tool_strategy="native")
    assert main([str(native), str(same), "--vary-tool-strategy"]) == 2
    assert "exactly one complete native result" in capsys.readouterr().err

    pi = write_fingerprinted_job(
        tmp_path, "pi", candidate_commit="pi", tool_strategy="code",
        agents=[{"name": "stella_harbor.pi_gateway:PiGateway", "model_name": "gateway/test"}],
    )
    assert main([str(native), str(pi), "--vary-tool-strategy"]) == 2


def test_tool_strategy_treatment_cannot_be_hidden_inside_allow_mismatch(tmp_path, capsys):
    native = write_fingerprinted_job(tmp_path, "native", candidate_commit="native", tool_strategy="native")
    code = write_fingerprinted_job(tmp_path, "code", candidate_commit="code", tool_strategy="code")
    assert main([str(code), str(native), "--vary-tool-strategy", "--allow-mismatch"]) == 2
    assert "cannot be combined" in capsys.readouterr().err


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
        adapter = {"gateway_endpoint": "https://gateway.example.test/v1", "provider_type": "openai-response", "model_price_digest": "price-a", "excluded_tools": ["view_image", "vllm"], "tool_strategy": "native", "candidate_commit": "commit", "execution_capability": ["bash"]}
        if index < 2:
            adapter["capability_profile_digest"] = "capability-a"
        # One task, five trials: the coverage rule compares task sets, and this
        # fixture is about fingerprint evidence, not task selection.
        write(job, run, "t", 1.0, 0.01, suffix=f"trial-{index}", adapter=adapter)

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


# --- loop comparator (PROTOCOL.md) -----------------------------------------

RUN = "2026-08-19__10-00-00"


def write_side(tmp_path, name, tasks, *, n_attempts=3, **overrides):
    """Build one side of a comparison.

    `tasks` maps a task name to a list of trial specs; a spec is a dict with
    any of reward, cost, valid, timed_out, exception, tools, errors_split,
    calls, turns, ledger.
    """
    job = tmp_path / name
    write_run_config(job, RUN, n_attempts=n_attempts, **overrides)
    (job / RUN / "result.json").write_text("{}")
    for task, trials in tasks.items():
        for index, spec in enumerate(trials):
            trial = job / RUN / f"{task}__{index}"
            trial.mkdir(parents=True)
            harbor = {
                "agent_result": {
                    "cost_usd": spec.get("cost", 0.01),
                    "n_input_tokens": spec.get("input_tokens", 1000),
                    "n_output_tokens": 100,
                },
            }
            reward = spec.get("reward")
            if reward is not None:
                harbor["verifier_result"] = {"rewards": {"reward": reward}}
            if spec.get("exception"):
                harbor["exception_info"] = spec["exception"]
            (trial / "result.json").write_text(json.dumps(harbor))
            tools = spec.get("tools", {"bash": 0})
            metrics = {
                "turns": spec.get("turns", 5),
                "tool_call_total": spec.get("calls", 10),
                "tool_error_total": sum(tools.values()),
                "tools": {n: {"calls": 5, "errors": e} for n, e in tools.items()},
                "timing_ms": {"total": spec.get("wall_ms", 1000)},
            }
            if spec.get("errors_split", True):
                metrics["command_nonzero_total"] = spec.get("command_nonzero_total", 0)
                metrics["orchestration_tool_call_total"] = metrics["tool_call_total"]
                metrics["execution_tool_call_total"] = metrics["tool_call_total"]
                metrics["execution_tool_error_total"] = metrics["tool_error_total"]
                metrics["execution_command_nonzero_total"] = metrics["command_nonzero_total"]
                metrics["execution_tools"] = metrics["tools"]
            adapter = {
                "valid": spec.get("valid", True),
                "timed_out": spec.get("timed_out", False),
                "capability_profile_digest": spec.get("capability", "capability-a"),
                "gateway_endpoint": spec.get("gateway_endpoint", "https://gateway.example.test/v1"),
                "provider_type": spec.get("provider_type", "openai-response"),
                "model_price_digest": spec.get("model_price_digest", "price-a"),
                "excluded_tools": spec.get("excluded_tools", []),
                "tool_strategy": spec.get("tool_strategy", "native"),
                "candidate_commit": spec.get("candidate_commit", "commit-a"),
                "execution_capability": spec.get("execution_capability", ["bash"]),
                "metrics": metrics,
            }
            if spec.get("no_adapter"):
                # Keep current identity evidence while deliberately withholding
                # execution counters for this fixture's missing-metric cases.
                adapter["metrics"] = {"timing_ms": {"total": spec.get("wall_ms", 1000)}}
            if "capability" in spec and spec["capability"] is None:
                adapter.pop("capability_profile_digest")
            if spec.get("ledger"):
                adapter["bridge_ledger"] = spec["ledger"]
            (trial / "agent" / "stella").mkdir(parents=True)
            (trial / "agent" / "stella" / "result.json").write_text(json.dumps(adapter))
    return job


def resolved(n, k=3, **spec):
    """k trials for one task, n of them resolved."""
    return [{"reward": 1.0 if i < n else 0.0, **spec} for i in range(k)]


def test_a_task_moves_and_is_reported_as_a_signal(tmp_path, capsys):
    candidate = write_side(tmp_path, "cand", {"target": resolved(1)})
    reference = write_side(tmp_path, "ref", {"target": resolved(0)})

    assert main([str(candidate), str(reference)]) == 0
    out = capsys.readouterr().out
    assert "SIGNAL target: 1 vs 0 resolved (+1)" in out
    assert "SUSPECTED_REGRESSION" not in out


def test_a_guard_falling_below_k_is_a_suspected_regression(tmp_path, capsys):
    candidate = write_side(tmp_path, "cand", {"guarded": resolved(2)})
    reference = write_side(tmp_path, "ref", {"guarded": resolved(3)})

    assert main([str(candidate), str(reference)]) == 0
    out = capsys.readouterr().out
    assert "SUSPECTED_REGRESSION guarded" in out and "a guard dropped below k/k" in out
    # Loud, but it still does not gate.
    assert "does not gate" in out


def test_a_two_point_drop_is_a_suspected_regression_without_a_guard(tmp_path, capsys):
    # The reference never resolved k of k, so this is not a guard; the drop alone
    # is what triggers.
    candidate = write_side(tmp_path, "cand", {"flaky": resolved(0)}, n_attempts=3)
    reference = write_side(tmp_path, "ref", {"flaky": resolved(2)}, n_attempts=3)

    assert main([str(candidate), str(reference)]) == 0
    out = capsys.readouterr().out
    assert "SUSPECTED_REGRESSION flaky" in out and "down 2 resolved" in out


def test_no_movement_says_so(tmp_path, capsys):
    candidate = write_side(tmp_path, "cand", {"steady": resolved(2)})
    reference = write_side(tmp_path, "ref", {"steady": resolved(2)})

    assert main([str(candidate), str(reference)]) == 0
    assert "No movement" in capsys.readouterr().out


def test_a_short_side_is_insufficient_evidence_and_never_judged(tmp_path, capsys):
    candidate = write_side(tmp_path, "cand", {"t": resolved(0, k=2)})
    reference = write_side(tmp_path, "ref", {"t": resolved(3)})

    assert main([str(candidate), str(reference)]) == 0
    out = capsys.readouterr().out
    assert "INSUFFICIENT_EVIDENCE t: candidate 2 scoreable, reference 3" in out
    # A guard that dropped 3 -> 0 would be the loudest verdict there is; it must
    # not appear, because the task was never judged.
    assert "SUSPECTED_REGRESSION" not in out and "SIGNAL t" not in out


def test_a_valid_trial_without_a_reward_is_not_scoreable(tmp_path, capsys):
    # The verifier's infrastructure failed. That is not an agent failure, so the
    # trial leaves the denominator instead of counting as unresolved.
    candidate = write_side(tmp_path, "cand", {"t": [*resolved(2), {"reward": None}]})
    reference = write_side(tmp_path, "ref", {"t": resolved(2)})

    assert main([str(candidate), str(reference)]) == 0
    out = capsys.readouterr().out
    assert "INSUFFICIENT_EVIDENCE" not in out
    assert "No movement" in out


def test_a_missing_required_task_is_refused_by_name(tmp_path, capsys):
    candidate = write_side(tmp_path, "cand", {"a": resolved(1)})
    reference = write_side(tmp_path, "ref", {"a": resolved(1), "b": resolved(1), "c": resolved(1)})

    assert main([str(candidate), str(reference)]) == 2
    err = capsys.readouterr().err
    assert "REFUSING COMPARISON" in err and "missing task(s)" in err
    assert "b" in err and "c" in err


def test_an_explicit_subset_is_allowed_and_echoed(tmp_path, capsys):
    candidate = write_side(tmp_path, "cand", {"a": resolved(1)})
    reference = write_side(tmp_path, "ref", {"a": resolved(1), "b": resolved(1)})

    assert main([str(candidate), str(reference), "--tasks", "a"]) == 0
    out = capsys.readouterr().out
    assert "EXPLICIT SUBSET (--tasks): 1 task(s) compared — a" in out
    assert "model and dataset are not" in out


def test_a_subset_naming_an_unknown_task_is_refused(tmp_path, capsys):
    candidate = write_side(tmp_path, "cand", {"a": resolved(1)})
    reference = write_side(tmp_path, "ref", {"a": resolved(1)})

    assert main([str(candidate), str(reference), "--tasks", "nope"]) == 2
    assert "the reference never ran" in capsys.readouterr().err


def test_confirmation_confirms_a_regression_and_exits_nonzero(tmp_path, capsys):
    candidate = write_side(tmp_path, "cand", {"t": resolved(1, k=5)}, n_attempts=5)
    reference = write_side(tmp_path, "ref", {"t": resolved(4, k=5)}, n_attempts=5)

    assert main([str(candidate), str(reference), "--confirm"]) == 1
    assert "CONFIRMED_REGRESSION: candidate 1/5 vs reference 4/5" in capsys.readouterr().out


def test_confirmation_confirms_an_improvement_without_gating(tmp_path, capsys):
    candidate = write_side(tmp_path, "cand", {"t": resolved(4, k=5)}, n_attempts=5)
    reference = write_side(tmp_path, "ref", {"t": resolved(1, k=5)}, n_attempts=5)

    assert main([str(candidate), str(reference), "--confirm"]) == 0
    assert "CONFIRMED_IMPROVEMENT: candidate 4/5 vs reference 1/5" in capsys.readouterr().out


def test_a_one_trial_difference_is_dismissed_with_both_counts(tmp_path, capsys):
    candidate = write_side(tmp_path, "cand", {"t": resolved(3, k=5)}, n_attempts=5)
    reference = write_side(tmp_path, "ref", {"t": resolved(2, k=5)}, n_attempts=5)

    assert main([str(candidate), str(reference), "--confirm"]) == 0
    assert "DISMISSED: candidate 3/5 vs reference 2/5" in capsys.readouterr().out


def test_confirmation_refuses_anything_that_is_not_a_single_task_at_k5(tmp_path, capsys):
    candidate = write_side(tmp_path, "cand", {"t": resolved(1)})
    reference = write_side(tmp_path, "ref", {"t": resolved(1)})
    assert main([str(candidate), str(reference), "--confirm"]) == 2
    assert "single-task k=5 run" in capsys.readouterr().err

    two = {"a": resolved(1, k=5), "b": resolved(1, k=5)}
    candidate = write_side(tmp_path, "cand5", two, n_attempts=5)
    reference = write_side(tmp_path, "ref5", two, n_attempts=5)
    assert main([str(candidate), str(reference), "--confirm"]) == 2
    assert "single-task run" in capsys.readouterr().err


def test_efficiency_signal_triggers_on_cost_with_resolution_unchanged(tmp_path, capsys):
    candidate = write_side(tmp_path, "cand", {"t": resolved(2, cost=0.02)})
    reference = write_side(tmp_path, "ref", {"t": resolved(2, cost=0.01)})

    assert main([str(candidate), str(reference)]) == 0
    out = capsys.readouterr().out
    assert "EFFICIENCY_SIGNAL t" in out and "cost +100.0%" in out
    assert "resolved unchanged at 2" in out


def test_efficiency_signal_triggers_on_per_tool_error_counts(tmp_path, capsys):
    candidate = write_side(tmp_path, "cand", {"t": resolved(2, tools={"edit": 1})})
    reference = write_side(tmp_path, "ref", {"t": resolved(2, tools={"edit": 4})})

    assert main([str(candidate), str(reference)]) == 0
    out = capsys.readouterr().out
    assert "EFFICIENCY_SIGNAL t" in out and "errors:edit -75.0%" in out


def test_a_zero_reference_mean_leaves_the_metric_unjudged(tmp_path, capsys):
    # Dividing by a reference mean of zero has no defined delta, so the metric
    # is unjudged rather than infinite.
    candidate = write_side(tmp_path, "cand", {"t": resolved(2, tools={"edit": 3})})
    reference = write_side(tmp_path, "ref", {"t": resolved(2, tools={"edit": 0})})

    assert main([str(candidate), str(reference)]) == 0
    out = capsys.readouterr().out
    assert "EFFICIENCY_SIGNAL" not in out
    assert "No movement" in out


def test_a_missing_metric_leaves_it_unjudged(tmp_path, capsys):
    # The reference agent reports no cost and no adapter metrics at all.
    candidate = write_side(tmp_path, "cand", {"t": resolved(2, cost=0.05)})
    reference = write_side(tmp_path, "ref", {"t": resolved(2, cost=None, no_adapter=True)})

    assert main([str(candidate), str(reference)]) == 0
    out = capsys.readouterr().out
    assert "EFFICIENCY_SIGNAL" not in out


def test_a_change_that_is_only_a_timeout_class_flip_is_untrusted(tmp_path, capsys):
    candidate = write_side(tmp_path, "cand", {"t": [
        {"reward": 0.0, "timed_out": True}, {"reward": 1.0}, {"reward": 1.0},
    ]})
    reference = write_side(tmp_path, "ref", {"t": resolved(3)})

    assert main([str(candidate), str(reference)]) == 0
    out = capsys.readouterr().out
    assert "UNTRUSTED t" in out and "timeout-class flip" in out
    # Untrusted means not judged: the guard drop must not be reported as a verdict.
    assert "SUSPECTED_REGRESSION" not in out and "SIGNAL t" not in out


def test_timeout_classes_are_assigned_by_first_match(tmp_path):
    from stella_harbor.compare import load

    job = write_side(tmp_path, "job", {"t": [
        {"reward": 0.0, "exception": {"exception_type": "AgentTimeoutError",
                                      "exception_message": "agent timed out"},
         "timed_out": True},
        {"reward": 0.0, "timed_out": True, "ledger": [{"op": "exec", "ok": True, "return_code": -1}]},
        {"reward": 0.0, "ledger": [{"op": "exec", "ok": True, "return_code": -1}]},
        {"reward": 1.0},
    ]})

    classes = [row["timeout_class"] for row in load(job)]

    assert classes == ["harness_timeout", "agent_deadline", "command_timeout", "none"]


def test_pre_1077_error_counts_are_displayed_but_never_judged(tmp_path, capsys):
    # Both sides predate the split, so their error counts fold nonzero command
    # exits in. A 4x difference must still not raise an EFFICIENCY_SIGNAL.
    candidate = write_side(tmp_path, "cand", {"t": resolved(2, tools={"bash": 8}, errors_split=False)})
    reference = write_side(tmp_path, "ref", {"t": resolved(2, tools={"bash": 2}, errors_split=False)})

    assert main([str(candidate), str(reference)]) == 0
    out = capsys.readouterr().out
    assert "EFFICIENCY_SIGNAL" not in out
    assert "error counts predate #1077" in out
    # Displayed all the same: 8.0 and 2.0 means, marked untrusted.
    assert "8.0 / 2.0*" in out


def test_mixed_old_and_new_sides_do_not_judge_error_counts(tmp_path, capsys):
    # The candidate split its counters, the reference did not. The pair is only
    # as trustworthy as its weaker side.
    candidate = write_side(tmp_path, "cand", {"t": resolved(2, tools={"bash": 1})})
    reference = write_side(tmp_path, "ref", {"t": resolved(2, tools={"bash": 6}, errors_split=False)})

    assert main([str(candidate), str(reference)]) == 0
    out = capsys.readouterr().out
    assert "EFFICIENCY_SIGNAL" not in out
    assert "error counts predate #1077" in out


def test_process_metrics_show_all_three_tiers(tmp_path, capsys):
    candidate = write_side(tmp_path, "cand", {"t": resolved(2, calls=20, turns=9, wall_ms=4000)})
    reference = write_side(tmp_path, "ref", {"t": resolved(2, calls=10, turns=4, wall_ms=2000)})

    assert main([str(candidate), str(reference)]) == 0
    out = capsys.readouterr().out
    assert "Process metrics" in out
    assert "20.0 / 10.0" in out and "9.0 / 4.0" in out       # behavioral
    assert "1000 / 1000" in out and "0.0100 / 0.0100" in out  # gateway
    assert "4.0 / 2.0" in out                                 # wall, displayed
    assert "Wall time is displayed and never judged" in out


def test_several_job_directories_make_up_one_side(tmp_path, capsys):
    # A side's k trials can be split across top-up jobs; every path is still named.
    first = write_side(tmp_path, "cand-a", {"t": [{"reward": 1.0}, {"reward": 0.0}]})
    second = write_side(tmp_path, "cand-b", {"t": [{"reward": 1.0}]})
    reference = write_side(tmp_path, "ref", {"t": resolved(0)})

    assert main([str(first), str(reference), "--candidate-job", str(second)]) == 0
    out = capsys.readouterr().out
    assert "SIGNAL t: 2 vs 0 resolved (+2)" in out


def test_an_unknown_k_judges_nothing(tmp_path, capsys):
    candidate = write_side(tmp_path, "cand", {"t": resolved(3)}, n_attempts=None)
    reference = write_side(tmp_path, "ref", {"t": resolved(0)}, n_attempts=None)

    assert main([str(candidate), str(reference), "--allow-mismatch"]) == 0
    out = capsys.readouterr().out
    assert "k is unknown" in out
    assert "INSUFFICIENT_EVIDENCE t" in out


# --- review fixes (#1111) ---------------------------------------------------


def test_a_top_up_job_with_a_different_condition_is_refused(tmp_path, capsys):
    # A top-up faces the same identity validation as a positional job; only its
    # attempt budget may differ.
    candidate = write_side(tmp_path, "cand", {"t": [{"reward": 1.0}, {"reward": 0.0}]})
    topup = write_side(tmp_path, "cand-b", {"t": [{"reward": 1.0}]},
                       agents=[{"name": "stella_harbor.agent:StellaAgent", "model_name": "other/model"}])
    reference = write_side(tmp_path, "ref", {"t": resolved(0)})

    assert main([str(candidate), str(reference), "--candidate-job", str(topup)]) == 2
    err = capsys.readouterr().err
    assert "candidate top-up cand-b:model" in err


def test_a_top_up_may_carry_a_different_attempt_budget(tmp_path, capsys):
    # The one permitted difference: a top-up exists to add trials the first job
    # did not run.
    candidate = write_side(tmp_path, "cand", {"t": [{"reward": 1.0}, {"reward": 0.0}]})
    topup = write_side(tmp_path, "cand-b", {"t": [{"reward": 1.0}]}, n_attempts=1)
    reference = write_side(tmp_path, "ref", {"t": resolved(0)})

    assert main([str(candidate), str(reference), "--candidate-job", str(topup)]) == 0
    assert "SIGNAL t: 2 vs 0 resolved (+2)" in capsys.readouterr().out


def test_the_same_job_twice_is_refused_before_it_replicates_evidence(tmp_path, capsys):
    candidate = write_side(tmp_path, "cand", {"t": resolved(1)})
    reference = write_side(tmp_path, "ref", {"t": resolved(1)})

    assert main([str(candidate), str(reference), "--candidate-job", str(candidate)]) == 2
    err = capsys.readouterr().err
    assert "the same run was given more than once" in err

    # A job root and the run directory inside it are the same evidence.
    assert main([str(candidate), str(reference), "--candidate-job", str(candidate / RUN)]) == 2
    assert "the same run was given more than once" in capsys.readouterr().err


def test_the_same_trial_reached_through_two_jobs_is_refused(tmp_path, capsys):
    candidate = write_side(tmp_path, "cand", {"t": resolved(1)})
    reference = write_side(tmp_path, "ref", {"t": resolved(1)})
    mirror = tmp_path / "cand-mirror"
    (mirror / RUN).mkdir(parents=True)
    for name in ("config.json", "result.json"):
        (mirror / RUN / name).write_text((candidate / RUN / name).read_text())
    (mirror / RUN / "t__0").symlink_to(candidate / RUN / "t__0")

    assert main([str(candidate), str(reference), "--candidate-job", str(mirror)]) == 2
    assert "the same trial reached the comparison twice" in capsys.readouterr().err


def test_an_untrusted_row_confirms_nothing(tmp_path, capsys):
    # The candidate lost two, and it also timed out twice more. The flip already
    # explains the movement, so a confirmation cannot be drawn from it.
    candidate = write_side(tmp_path, "cand", {"t": [
        {"reward": 1.0}, {"reward": 1.0}, {"reward": 1.0},
        {"reward": 0.0, "timed_out": True}, {"reward": 0.0, "timed_out": True},
    ]}, n_attempts=5)
    reference = write_side(tmp_path, "ref", {"t": resolved(5, k=5)}, n_attempts=5)

    assert main([str(candidate), str(reference), "--confirm"]) == 0
    out = capsys.readouterr().out
    assert "UNTRUSTED: candidate 3/5 vs reference 5/5" in out
    assert "Re-run both sides." in out
    assert "CONFIRMED_REGRESSION" not in out


def test_allow_mismatch_can_never_back_a_confirmation(tmp_path, capsys):
    candidate = write_side(tmp_path, "cand", {"t": resolved(1, k=5)}, n_attempts=5)
    reference = write_side(tmp_path, "ref", {"t": resolved(4, k=5)}, n_attempts=5)

    assert main([str(candidate), str(reference), "--confirm", "--allow-mismatch"]) == 2
    assert "can never back a confirmation" in capsys.readouterr().err


def test_k_may_not_override_a_recorded_attempt_budget(tmp_path, capsys):
    candidate = write_side(tmp_path, "cand", {"t": resolved(3)})
    reference = write_side(tmp_path, "ref", {"t": resolved(0)})

    assert main([str(candidate), str(reference), "--k", "5"]) == 2
    err = capsys.readouterr().err
    assert "conflicts with the attempt budget the jobs recorded (3)" in err
    # Restating the recorded budget is not a conflict.
    assert main([str(candidate), str(reference), "--k", "3"]) == 0


def test_k_still_fills_in_a_budget_no_artifact_recorded(tmp_path, capsys):
    # An unrecorded budget is a blocking fingerprint issue, and a confirmation
    # may not use --allow-mismatch, so --k has to satisfy it or archived runs
    # cannot be compared at all.
    candidate = write_side(tmp_path, "cand", {"t": resolved(3)}, n_attempts=None)
    reference = write_side(tmp_path, "ref", {"t": resolved(0)}, n_attempts=None)

    assert main([str(candidate), str(reference), "--k", "3"]) == 0
    out = capsys.readouterr().out
    assert "SIGNAL t: 3 vs 0 resolved (+3)" in out
    assert "k = 3 — supplied by --k; no run recorded an attempt budget" in out
    assert "UNTRUSTWORTHY" not in out and "CANNOT VERIFY" not in out


def test_a_k1_pair_is_refused_until_k_is_supplied(tmp_path, capsys):
    # Harbor dumps the run config with exclude_defaults=True, and n_attempts
    # defaults to 1, so a k=1 job on both sides records no budget at all. That
    # is the quick tier, and it must fail closed rather than guess k=1.
    candidate = write_side(tmp_path, "cand", {"t": resolved(1)}, n_attempts=None)
    reference = write_side(tmp_path, "ref", {"t": resolved(0)}, n_attempts=None)

    assert main([str(candidate), str(reference)]) == 2
    assert "expected at run config.json: n_attempts" in capsys.readouterr().err
    # loop.sh --against supplies the k it actually ran, which unblocks it.
    assert main([str(candidate), str(reference), "--k", "1"]) == 0


def test_a_subset_that_selects_nothing_is_refused(tmp_path, capsys):
    candidate = write_side(tmp_path, "cand", {"a": resolved(1)})
    reference = write_side(tmp_path, "ref", {"a": resolved(1)})

    assert main([str(candidate), str(reference), "--tasks", ","]) == 2
    assert "selected no task at all" in capsys.readouterr().err


def test_a_reference_with_no_tasks_is_refused(tmp_path, capsys):
    candidate = write_side(tmp_path, "cand", {"a": resolved(1)})
    reference = write_side(tmp_path, "ref", {})

    assert main([str(candidate), str(reference)]) == 2
    assert "CANNOT VERIFY CONFIGURATION:" in capsys.readouterr().err


def test_confirmation_refuses_a_candidate_carrying_an_extra_task(tmp_path, capsys):
    # Coverage only requires the candidate to hold every reference task, so a
    # one-task reference does not by itself make a single-task confirmation.
    candidate = write_side(tmp_path, "cand", {"t": resolved(1, k=5), "extra": resolved(1, k=5)}, n_attempts=5)
    reference = write_side(tmp_path, "ref", {"t": resolved(4, k=5)}, n_attempts=5)

    assert main([str(candidate), str(reference), "--confirm"]) == 2
    assert "single-task run on both sides" in capsys.readouterr().err


def test_an_improvement_that_came_with_more_timeouts_is_still_judged(tmp_path, capsys):
    # Timing out more often does not explain resolving more, so the flip test
    # must not swallow the improvement.
    candidate = write_side(tmp_path, "cand", {"t": [
        {"reward": 1.0}, {"reward": 1.0}, {"reward": 0.0, "timed_out": True},
    ]})
    reference = write_side(tmp_path, "ref", {"t": resolved(1)})

    assert main([str(candidate), str(reference)]) == 0
    out = capsys.readouterr().out
    assert "SIGNAL t: 2 vs 1 resolved (+1)" in out
    assert "UNTRUSTED" not in out


def test_a_regression_that_came_with_fewer_timeouts_is_still_judged(tmp_path, capsys):
    # Timing out less often does not explain resolving less; absolute counts
    # would have hidden this regression behind an untrusted marker.
    candidate = write_side(tmp_path, "cand", {"t": resolved(0)})
    reference = write_side(tmp_path, "ref", {"t": [
        {"reward": 1.0}, {"reward": 1.0}, {"reward": 0.0, "timed_out": True},
    ]})

    assert main([str(candidate), str(reference)]) == 0
    out = capsys.readouterr().out
    assert "SUSPECTED_REGRESSION t" in out and "down 2 resolved" in out
    assert "UNTRUSTED" not in out


def test_one_trial_without_a_metric_leaves_the_whole_metric_unjudged(tmp_path, capsys):
    # A mean over the trials that happen to carry the field is not the mean the
    # metric names: two of three at 0.02 must not read as a cost signal.
    candidate = write_side(tmp_path, "cand", {"t": [
        {"reward": 1.0, "cost": 0.02}, {"reward": 1.0, "cost": 0.02},
        {"reward": 0.0, "cost": None},
    ]})
    reference = write_side(tmp_path, "ref", {"t": resolved(2, cost=0.01)})

    assert main([str(candidate), str(reference)]) == 0
    out = capsys.readouterr().out
    assert "EFFICIENCY_SIGNAL" not in out


def test_an_empty_tools_dict_measured_zero_errors(tmp_path, capsys):
    # Post-#1077 with tools: {} means the tool never erred, which is a real
    # zero: a reference mean of zero leaves the metric unjudged, it does not
    # make the trial's evidence missing.
    candidate = write_side(tmp_path, "cand", {"t": resolved(2, tools={"edit": 4})})
    reference = write_side(tmp_path, "ref", {"t": [
        {"reward": 1.0, "tools": {}}, {"reward": 1.0, "tools": {}}, {"reward": 0.0, "tools": {}},
    ]})

    assert main([str(candidate), str(reference)]) == 0
    out = capsys.readouterr().out
    assert "error counts predate #1077" not in out
    assert "4.0 / 0.0" in out
    assert "EFFICIENCY_SIGNAL" not in out


def test_a_trial_without_tool_evidence_leaves_error_counts_unjudged(tmp_path, capsys):
    candidate = write_side(tmp_path, "cand", {"t": [
        {"reward": 1.0, "tools": {"edit": 4}}, {"reward": 1.0, "tools": {"edit": 4}},
        {"reward": 0.0, "no_adapter": True},
    ]})
    reference = write_side(tmp_path, "ref", {"t": resolved(2, tools={"edit": 1})})

    assert main([str(candidate), str(reference)]) == 0
    out = capsys.readouterr().out
    assert "EFFICIENCY_SIGNAL" not in out
    assert "error counts predate #1077" not in out


def test_a_pre_split_trial_with_no_tools_still_taints_the_pair(tmp_path, capsys):
    # An empty tools dict does not exempt a pre-#1077 trial: the counter it kept
    # folded command exits in either way.
    candidate = write_side(tmp_path, "cand", {"t": [
        *resolved(2, k=2, tools={"edit": 4}), {"reward": 0.0, "tools": {}, "errors_split": False},
    ]})
    reference = write_side(tmp_path, "ref", {"t": resolved(2, tools={"edit": 1})})

    assert main([str(candidate), str(reference)]) == 0
    out = capsys.readouterr().out
    assert "error counts predate #1077" in out
    assert "EFFICIENCY_SIGNAL" not in out


def test_a_top_up_that_cannot_prove_its_build_is_refused(tmp_path, capsys):
    # State 2, asymmetric evidence: the side recorded a build, the top-up
    # recorded nothing, so it cannot show it belongs here.
    candidate = write_side(tmp_path, "cand", {"t": [{"reward": 1.0}, {"reward": 0.0}]})
    topup = write_side(tmp_path, "cand-b", {"t": [{"reward": 1.0, "candidate_commit": None}]})
    reference = write_side(tmp_path, "ref", {"t": resolved(0)})

    assert main([str(candidate), str(reference), "--candidate-job", str(topup)]) == 2
    captured = capsys.readouterr()
    assert "candidate top-up cand-b:candidate_commit" in captured.err
    # The unverified trials must not have been merged into the side.
    assert "SIGNAL" not in captured.out


def test_a_top_up_from_a_different_commit_is_refused(tmp_path, capsys):
    # State 1, both recorded and they differ.
    candidate = write_side(tmp_path, "cand", {"t": [{"reward": 1.0}, {"reward": 0.0}]},
                           candidate_commit="commit-a")
    topup = write_side(tmp_path, "cand-b", {"t": [{"reward": 1.0}]}, candidate_commit="commit-b")
    reference = write_side(tmp_path, "ref", {"t": resolved(0)})

    assert main([str(candidate), str(reference), "--candidate-job", str(topup)]) == 2
    assert "candidate top-up cand-b:candidate_commit" in capsys.readouterr().err


def test_an_unrecorded_budget_without_k_is_still_refused(tmp_path, capsys):
    candidate = write_side(tmp_path, "cand", {"t": resolved(3)}, n_attempts=None)
    reference = write_side(tmp_path, "ref", {"t": resolved(0)}, n_attempts=None)

    assert main([str(candidate), str(reference)]) == 2
    err = capsys.readouterr().err
    assert "CANNOT VERIFY CONFIGURATION:" in err and "budget" in err


def test_k_rescues_only_the_budget_and_nothing_else(tmp_path, capsys):
    agents = [{"name": "stella_harbor.agent:StellaAgent", "model_name": None}]
    candidate = write_side(tmp_path, "cand", {"t": resolved(3)}, n_attempts=None, agents=agents)
    reference = write_side(tmp_path, "ref", {"t": resolved(0)}, n_attempts=None, agents=agents)

    assert main([str(candidate), str(reference), "--k", "3"]) == 2
    err = capsys.readouterr().err
    assert "CANNOT VERIFY CONFIGURATION:" in err and "model" in err


def test_a_field_neither_job_ever_recorded_is_reported_not_refused(tmp_path, capsys):
    # State 3. No Harbor artifact writes candidate_commit or tool_strategy
    # today, and refusing mutual silence would condemn the protocol's own
    # re-run path: an INSUFFICIENT_EVIDENCE top-up is exactly this case.
    candidate = write_side(tmp_path, "cand", {"t": [{"reward": 1.0}, {"reward": 0.0}]})
    topup = write_side(tmp_path, "cand-b", {"t": [{"reward": 1.0}]})
    reference = write_side(tmp_path, "ref", {"t": resolved(0)})

    assert main([str(candidate), str(reference), "--candidate-job", str(topup)]) == 0
    out = capsys.readouterr().out
    assert "IDENTITY NEVER RECORDED (reported, not blocking):" not in out
    assert "UNTRUSTWORTHY" not in out
    # The top-up's trials are merged, which is the whole point of the path.
    assert "SIGNAL t: 2 vs 0 resolved (+2)" in out


def test_partial_coverage_inside_a_job_is_that_jobs_value(tmp_path, capsys):
    # Real archives carry the digest on most trials, not all. One value, some
    # trials silent: the job's value, with its coverage reported.
    candidate = write_side(tmp_path, "cand", {"t": [
        {"reward": 1.0}, {"reward": 0.0}, {"reward": 0.0, "capability": None},
    ]})
    topup = write_side(tmp_path, "cand-b", {"t": [{"reward": 1.0}]})
    reference = write_side(tmp_path, "ref", {"t": resolved(0)})

    assert main([str(candidate), str(reference), "--candidate-job", str(topup)]) == 0
    out = capsys.readouterr().out
    assert "IDENTITY PARTIALLY COVERED (reported, not blocking):" in out
    assert "candidate top-up cand-b:capability_profile_digest" in out and "[2/3]" in out
    assert "UNTRUSTWORTHY" not in out


def test_two_build_identities_inside_one_top_up_are_refused(tmp_path, capsys):
    candidate = write_side(tmp_path, "cand", {"t": [{"reward": 1.0}, {"reward": 0.0}]})
    topup = write_side(tmp_path, "cand-b", {"t": [
        {"reward": 1.0, "capability": "capability-a"},
        {"reward": 1.0, "capability": "capability-b"},
    ]})
    reference = write_side(tmp_path, "ref", {"t": resolved(0)})

    assert main([str(candidate), str(reference), "--candidate-job", str(topup)]) == 2
    err = capsys.readouterr().err
    assert "INTERNALLY INCONSISTENT RUN:" in err
    assert "candidate top-up cand-b:capability_profile_digest" in err


def test_a_budget_recorded_by_one_side_alone_is_still_a_mismatch(tmp_path, capsys):
    # Reading the recorded budget must not consume it: without --k there is
    # nothing to fill the reference's silence with.
    candidate = write_side(tmp_path, "cand", {"t": resolved(5, k=5)}, n_attempts=5)
    reference = write_side(tmp_path, "ref", {"t": resolved(5, k=5)}, n_attempts=None)

    assert main([str(candidate), str(reference)]) == 2
    err = capsys.readouterr().err
    assert "CANNOT VERIFY CONFIGURATION:" in err and "budget" in err
    assert "supplied by --k" not in capsys.readouterr().out


def test_confirmation_refuses_a_top_up_whose_identity_nothing_records(tmp_path, capsys):
    candidate = write_side(tmp_path, "cand", {"t": resolved(1, k=3)}, n_attempts=5)
    topup = write_side(tmp_path, "cand-b", {"t": resolved(0, k=2)}, n_attempts=5)
    reference = write_side(tmp_path, "ref", {"t": resolved(4, k=5)}, n_attempts=5)

    assert main([str(candidate), str(reference), "--confirm", "--candidate-job", str(topup)]) == 1
    assert "CONFIRMED_REGRESSION" in capsys.readouterr().out


def test_confirmation_without_a_top_up_runs_with_the_same_silent_fields(tmp_path, capsys):
    # Nothing records candidate_commit or tool_strategy here either; the
    # closure is about top-ups, not about the fields themselves.
    candidate = write_side(tmp_path, "cand", {"t": resolved(1, k=5)}, n_attempts=5)
    reference = write_side(tmp_path, "ref", {"t": resolved(4, k=5)}, n_attempts=5)

    assert main([str(candidate), str(reference), "--confirm"]) == 1
    assert "CONFIRMED_REGRESSION: candidate 1/5 vs reference 4/5" in capsys.readouterr().out
