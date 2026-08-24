import asyncio
import sys

from stella_harbor.agent import (
    StellaAgent,
    finalize_fixture_cleanup,
    split_trial_budget,
    terminate_child,
    verify_evidence,
)


def test_terminate_child_uses_term_before_sigkill():
    async def cooperative():
        proc = await asyncio.create_subprocess_exec(sys.executable, "-c", "import time; time.sleep(10)")
        killed = await terminate_child(proc, 1)
        assert killed is False
        assert proc.returncode is not None and proc.returncode < 0

    async def stubborn():
        code = "import signal,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); time.sleep(10)"
        proc = await asyncio.create_subprocess_exec(sys.executable, "-c", code)
        await asyncio.sleep(0.05)  # let the child install its SIGTERM handler
        killed = await terminate_child(proc, 0.05)
        assert killed is True
        assert proc.returncode is not None and proc.returncode < 0

    asyncio.run(cooperative())
    asyncio.run(stubborn())


def test_normal_exit_retries_an_incomplete_cleanup_before_releasing_its_lease(monkeypatch, tmp_path):
    calls = []

    async def cleanup(_config, _state, _url, _token, *, action="cleanup"):
        calls.append(action)

    monkeypatch.setattr("stella_harbor.agent.cleanup_fixture_lease", cleanup)
    result = {"cleanup": [
        {"phase": "mcp_registration", "outcome": "error"},
        {"phase": "agent", "outcome": "completed"},
        {"phase": "provisioned_user", "outcome": "completed"},
    ]}

    recovery = asyncio.run(finalize_fixture_cleanup("fixture.json", tmp_path / "state.json", "http://test", "admin", 0, result))

    assert recovery == {"outcome": "recovered"}
    assert calls == ["cleanup", "release"]


def test_normal_exit_cleanup_failure_keeps_the_retryable_lease(monkeypatch, tmp_path):
    calls = []

    async def cleanup(_config, _state, _url, _token, *, action="cleanup"):
        calls.append(action)
        if action == "cleanup":
            raise RuntimeError("transient DELETE failure")

    monkeypatch.setattr("stella_harbor.agent.cleanup_fixture_lease", cleanup)
    result = {"cleanup": [{"phase": "agent", "outcome": "error"}]}

    try:
        asyncio.run(finalize_fixture_cleanup("fixture.json", tmp_path / "state.json", "http://test", "admin", 0, result))
    except RuntimeError as error:
        assert "transient DELETE failure" in str(error)
    else:
        raise AssertionError("cleanup failure was accepted")
    assert calls == ["cleanup"]


def test_watchdog_grace_covers_the_driver_cleanup_budget(tmp_path):
    agent = StellaAgent(tmp_path, model="gateway/test", binding_dir=str(tmp_path / "bindings"))
    assert agent.CHILD_REAP_SEC >= 30


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


def test_evidence_ignores_setup_and_host_verifier_bridge_traffic():
    setup = [{"op": "ping"}, {"op": "stat", "path": "/app/.agents"}]
    verifier_read = {"op": "read_file", "path": "/workspace/evidence.txt", "verifier": True}
    assert verify_evidence(result(stella_tool_calls=[], token_count=5, timed_out=True), setup + [verifier_read], "nonce") == []
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
