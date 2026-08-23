from stella_harbor.agent import StellaAgent, split_trial_budget, verify_evidence


def test_agent_reads_the_loop_exclusion_list(monkeypatch, tmp_path):
    monkeypatch.setenv("STELLA_EVAL_EXCLUDED_TOOLS", "edit,read,write")
    agent = StellaAgent(tmp_path, model="gateway/test", binding_dir=str(tmp_path / "bindings"))
    assert agent.excluded_tools == "edit,read,write"


def result(**changes):
    base = {
        "bridge_nonce": "nonce",
        "turn_terminal_state": "completed",
        "disabled_tools_count": 3,
        "stella_tool_calls": [{"name": "bash", "arguments": {"command": "pwd"}}],
    }
    base.update(changes)
    return base


def test_evidence_matches_core_tool_calls_in_order():
    assert verify_evidence(result(), [{"op": "exec", "command": "pwd"}], "nonce") == []


def test_evidence_fails_closed_for_nonce_and_missing_ledger_call():
    failures = verify_evidence(result(), [], "other")
    assert "bridge nonce does not match" in failures
    assert any("bash tool call" in failure for failure in failures)


def test_evidence_fails_closed_for_empty_turn():
    failures = verify_evidence(result(stella_tool_calls=[], token_count=0), [], "nonce")
    assert "turn shows no model activity" in failures


def test_evidence_accepts_text_only_turn_with_tokens():
    assert verify_evidence(result(stella_tool_calls=[], token_count=12), [], "nonce") == []


def test_evidence_ignores_setup_ledger_traffic_but_not_tool_operations():
    setup = [{"op": "ping"}, {"op": "stat", "path": "/app/.agents"}]
    assert verify_evidence(result(stella_tool_calls=[], token_count=5, timed_out=True), setup, "nonce") == []
    failures = verify_evidence(result(stella_tool_calls=[], token_count=5), setup + [{"op": "exec", "command": "ls"}], "nonce")
    assert any("tool operations" in failure for failure in failures)


def test_bridge_stats_separates_harness_faults_from_agent_mistakes():
    # A missing file is the agent's problem; an "internal" is ours. Reward can
    # hide ours entirely, because a capable agent just routes around a broken
    # tool, so it has to be counted on its own.
    from stella_harbor.agent import bridge_stats

    stats = bridge_stats([
        {"seq": 1, "op": "read_file", "path": "/nope", "ok": False, "code": "not_found", "elapsed_ms": 5},
        {"seq": 2, "op": "read_file", "path": "/etc/nginx/sites-enabled/default", "ok": False,
         "code": "internal", "elapsed_ms": 169},
        {"seq": 3, "op": "exec", "ok": True, "elapsed_ms": 10},
    ])

    assert stats["operations"]["read_file"]["failures"] == 2
    assert [f["seq"] for f in stats["adapter_faults"]] == [2]
    assert stats["adapter_faults"][0]["code"] == "internal"


def _result(calls):
    return {"bridge_nonce": "n", "turn_terminal_state": "completed", "disabled_tools_count": 3,
            "token_count": 100, "stella_tool_calls": calls}


def test_expanded_paths_still_match_their_ledger_entry():
    # Stella expands $TMPDIR and resolves relative paths before the call reaches
    # the bridge, so the transcript and the ledger legitimately disagree.
    from stella_harbor.agent import verify_evidence

    calls = [{"name": "write", "arguments": {"path": "$TMPDIR/nginx.conf"}}]
    ledger = [{"op": "write_file", "path": "/tmp/stella-eval-5d37/nginx.conf", "ok": True}]

    assert verify_evidence(_result(calls), ledger, "n") == []


def test_one_unmatched_call_does_not_consume_the_ledger_for_the_rest():
    from stella_harbor.agent import verify_evidence

    calls = [{"name": "write", "arguments": {"path": "/never/written"}},
             {"name": "write", "arguments": {"path": "/app/real.txt"}}]
    ledger = [{"op": "write_file", "path": "/app/real.txt", "ok": True}]

    assert verify_evidence(_result(calls), ledger, "n") == [
        "write tool call has no matching bridge ledger entry"]


def test_a_failed_tool_call_needs_no_ledger_entry():
    # A call that failed may never have reached the sandbox. Requiring evidence
    # for it voided trials whose agent simply made some bad edits.
    from stella_harbor.agent import verify_evidence

    calls = [{"name": "edit", "arguments": {"path": "/app/x"}, "is_error": True},
             {"name": "edit", "arguments": {"path": "/app/y"}}]
    ledger = [{"op": "write_file", "path": "/app/y", "ok": True}]

    assert verify_evidence(_result(calls), ledger, "n") == []


def test_trial_budget_holds_the_stop_confirmation_inside_harbor_s_limit():
    # The regression: 885s of work plus a 3 minute confirmation against a 900s
    # limit killed 35 of 445 trials at ~1005s, each one unscoreable.
    limit, margin = 900, 15
    deadline, confirm = split_trial_budget(limit, margin, 60)

    assert deadline + confirm <= limit - margin
    assert confirm == 60
    assert deadline == 825


def test_trial_budget_shrinks_the_confirmation_rather_than_the_work():
    deadline, confirm = split_trial_budget(60, 15, 600)

    assert deadline + confirm <= 45
    assert deadline > confirm
    assert confirm >= 1


def test_bridge_stats_counts_a_nonzero_exit_as_the_container_answering():
    # A nonzero exit is the agent learning what the image has, not a tool that
    # failed. Counting the two together made a clean run report 81 tool errors
    # and fed the execution class of the failure taxonomy.
    from stella_harbor.agent import bridge_stats

    stats = bridge_stats([
        {"op": "exec", "ok": True, "return_code": 0, "elapsed_ms": 5},
        {"op": "exec", "ok": True, "return_code": 1, "elapsed_ms": 5},
        {"op": "exec", "ok": True, "return_code": 127, "elapsed_ms": 5},
        {"op": "exec", "ok": True, "return_code": -1, "elapsed_ms": 5},
        {"op": "read", "ok": False, "code": "not_found", "elapsed_ms": 1},
    ])

    assert stats["command_nonzero"] == 2
    assert stats["command_timeout"] == 1


def test_code_execution_metrics_use_only_outer_code_and_mapped_ledger_ops():
    from stella_harbor.agent import execution_metrics

    evidence = {
        "tool_strategy": "code",
        "stella_tool_calls": [{"name": "code", "arguments": {"source": "..."}}],
        "metrics": {"tool_call_total": 1},
    }
    failures = execution_metrics(evidence, [
        {"op": "ping", "ok": True},  # setup, never execution
        {"op": "exec", "ok": True, "return_code": 1},
        {"op": "read_file", "ok": False},
    ])

    assert failures == []
    assert evidence["metrics"]["orchestration_tool_call_total"] == 1
    assert evidence["metrics"]["execution_tool_call_total"] == 2
    assert evidence["metrics"]["execution_tools"] == {
        "bash": {"calls": 1, "errors": 0, "command_nonzero": 1},
        "view_image": {"calls": 1, "errors": 1, "command_nonzero": 0},
    }


def test_code_execution_metrics_fail_closed_without_code_or_for_unknown_bridge_op():
    from stella_harbor.agent import execution_metrics

    no_outer = {"tool_strategy": "code", "stella_tool_calls": [], "metrics": {"tool_call_total": 0}}
    assert execution_metrics(no_outer, [{"op": "exec", "ok": True}]) == [
        "code bridge operations have no outer code activity"]

    unmapped = {"tool_strategy": "code", "stella_tool_calls": [{"name": "code"}], "metrics": {"tool_call_total": 1}}
    assert execution_metrics(unmapped, [{"op": "write_file", "ok": True}]) == [
        "code bridge operation 'write_file' has no eval capability mapping"]
