import asyncio
import contextlib
import http.server
import json
from pathlib import Path
import subprocess
import sys
import threading
import time
import os
import queue
import signal
import socket
import urllib.request
from http.cookiejar import CookieJar

import pytest

from stella_harbor.agent import (
    ADAPTER_PHASES,
    DRIVER_PHASES,
    ParentDeadlines,
    PhaseJournal,
    PreSpawnDeadlineError,
    StellaAgent,
    await_finalization_within,
    derive_parent_deadlines,
    finalize_fixture_cleanup,
    kill_child,
    read_phase_journal,
    verify_evidence,
)


def test_parent_deadlines_split_the_absolute_wall_for_seam_and_production():
    seam = derive_parent_deadlines(
        outer_deadline=15.0, outer_deadline_unix_ms=15_000, agent_timeout_sec=15,
        go_finalization_budget_sec=2, now=0.0,
    )
    assert seam == ParentDeadlines(
        work_deadline=7.0, go_finalize_by=9.0, child_exit_deadline=11.0,
        recovery_deadline=15.0, go_finalize_by_unix_ms=9_000,
    )
    assert seam.work_deadline < seam.go_finalize_by < seam.child_exit_deadline < seam.recovery_deadline

    production = derive_parent_deadlines(
        outer_deadline=900.0, outer_deadline_unix_ms=900_000, agent_timeout_sec=900,
        go_finalization_budget_sec=180, now=0.0,
    )
    assert production.go_finalize_by == 720.0
    assert production.child_exit_deadline == 780.0
    assert production.recovery_deadline == 900.0
    assert production.go_finalize_by_unix_ms == 720_000


def test_parent_deadlines_refuse_a_child_after_setup_consumes_its_work_window():
    with pytest.raises(PreSpawnDeadlineError, match="cannot fit"):
        derive_parent_deadlines(
            outer_deadline=15.0, outer_deadline_unix_ms=15_000, agent_timeout_sec=15,
            go_finalization_budget_sec=2, now=7.0,
        )


def test_adapter_phase_journal_is_mode0600_fixed_and_rejects_symlinks(tmp_path):
    path = tmp_path / "phase.jsonl"
    journal = PhaseJournal(path, ADAPTER_PHASES)
    journal.append("run_start")
    journal.append("child_spawn")
    journal.close()

    assert path.stat().st_mode & 0o777 == 0o600
    entries = read_phase_journal(path, ADAPTER_PHASES)
    assert entries is not None
    assert [entry["phase"] for entry in entries] == ["run_start", "child_spawn"]
    assert all(set(entry) == {"version", "phase", "timestamp"} for entry in entries)

    unsafe = tmp_path / "unsafe.jsonl"
    unsafe.symlink_to(path)
    assert read_phase_journal(unsafe, ADAPTER_PHASES) is None


def test_timeout_finalization_never_starts_a_second_recovery_wall():
    async def hangs():
        await asyncio.Event().wait()

    async def exercise():
        deadline = asyncio.get_running_loop().time() + 0.02
        with pytest.raises(asyncio.TimeoutError):
            await await_finalization_within(deadline, hangs())
        with pytest.raises(TimeoutError):
            await await_finalization_within(asyncio.get_running_loop().time() - 1, hangs())

    asyncio.run(exercise())


def test_parent_watchdog_reaps_before_fallback_recovery_can_start():
    async def exercise():
        proc = await asyncio.create_subprocess_exec(sys.executable, "-c", "import time; time.sleep(10)")
        await kill_child(proc, reap_deadline=asyncio.get_running_loop().time() + 1)
        assert proc.returncode is not None and proc.returncode < 0

    asyncio.run(exercise())


@pytest.mark.parametrize("returncode", [-15, -9], ids=["term", "sigkill"])
def test_signal_terminated_specialized_trial_retries_with_a_live_pat_then_deactivates_and_releases(monkeypatch, tmp_path, returncode):
    calls = []
    pat_active = True

    async def cleanup(_config, _state, *, action="cleanup"):
        assert pat_active or action == "release"
        calls.append(action)

    async def deactivate(_state, _url, _token):
        nonlocal pat_active
        assert pat_active
        calls.append("deactivate")
        pat_active = False

    monkeypatch.setattr("stella_harbor.agent.cleanup_fixture_lease", cleanup)
    monkeypatch.setattr("stella_harbor.agent.deactivate_fixture_user", deactivate)
    result = {"cleanup": [
        {"phase": "mcp_registration", "outcome": "error"},
        {"phase": "agent", "outcome": "completed"},
        {"phase": "provisioned_user", "outcome": "pending"},
    ]}

    recovery = asyncio.run(finalize_fixture_cleanup("fixture.json", tmp_path / "state.json", "http://test", "admin", returncode, result))

    assert recovery == {"outcome": "recovered"}
    assert calls == ["cleanup", "deactivate", "release"]
    assert pat_active is False


def test_early_adapter_exit_recovers_cleanup_before_releasing_the_lease(monkeypatch, tmp_path):
    calls = []

    async def cleanup(_config, _state, *, action="cleanup"):
        calls.append(action)

    async def deactivate(_state, _url, _token):
        calls.append("deactivate")

    monkeypatch.setattr("stella_harbor.agent.cleanup_fixture_lease", cleanup)
    monkeypatch.setattr("stella_harbor.agent.deactivate_fixture_user", deactivate)

    recovery = asyncio.run(finalize_fixture_cleanup(
        "fixture.json", tmp_path / "state.json", "http://test", "admin", 10,
        {"cleanup": [
            {"phase": "mcp_registration", "outcome": "pending"},
            {"phase": "agent", "outcome": "pending"},
            {"phase": "provisioned_user", "outcome": "pending"},
        ]},
    ))

    assert recovery == {"outcome": "recovered"}
    assert calls == ["cleanup", "deactivate", "release"]


def test_normal_exit_cleanup_failure_keeps_the_retryable_lease(monkeypatch, tmp_path):
    calls = []
    deactivated = False

    async def cleanup(_config, _state, *, action="cleanup"):
        calls.append(action)
        if action == "cleanup":
            raise RuntimeError("transient DELETE failure")

    async def deactivate(_state, _url, _token):
        nonlocal deactivated
        deactivated = True

    monkeypatch.setattr("stella_harbor.agent.cleanup_fixture_lease", cleanup)
    monkeypatch.setattr("stella_harbor.agent.deactivate_fixture_user", deactivate)
    result = {"cleanup": [{"phase": "agent", "outcome": "error"}]}

    try:
        asyncio.run(finalize_fixture_cleanup("fixture.json", tmp_path / "state.json", "http://test", "admin", 0, result))
    except RuntimeError as error:
        assert "transient DELETE failure" in str(error)
    else:
        raise AssertionError("cleanup failure was accepted")
    assert calls == ["cleanup"]
    assert deactivated is False


def test_complete_go_cleanup_only_releases_the_lease(monkeypatch, tmp_path):
    calls = []

    async def cleanup(_config, _state, *, action="cleanup"):
        calls.append(action)

    monkeypatch.setattr("stella_harbor.agent.cleanup_fixture_lease", cleanup)
    result = {"cleanup": [
        {"phase": "mcp_registration", "outcome": "completed"},
        {"phase": "agent", "outcome": "completed"},
        {"phase": "provisioned_user", "outcome": "completed"},
    ]}

    recovery = asyncio.run(finalize_fixture_cleanup("fixture.json", tmp_path / "state.json", "http://test", "admin", 0, result))

    assert recovery == {"outcome": "released"}
    assert calls == ["release"]


def test_go_released_fixture_lease_skips_coordinator_cleanup(monkeypatch, tmp_path):
    async def cleanup(*_args, **_kwargs):
        raise AssertionError("coordinator retried an already released lease")

    monkeypatch.setattr("stella_harbor.agent.cleanup_fixture_lease", cleanup)
    recovery = asyncio.run(finalize_fixture_cleanup(
        "fixture.json", tmp_path / "state.json", "http://test", "admin", 12,
        {"fixture_lease_released": True},
    ))

    assert recovery == {"outcome": "released"}


def test_parent_watchdog_uses_the_production_post_go_ceiling(tmp_path):
    agent = StellaAgent(tmp_path, model="gateway/test", binding_dir=str(tmp_path / "bindings"))
    assert agent.STOP_CONFIRM_SEC == 180
    assert agent.PARENT_POST_GO_RESERVE_CEILING_SEC == 180


def test_absolute_outer_wall_covers_setup_spawn_and_stubborn_child(monkeypatch, tmp_path):
    """A pre-spawn delay cannot buy a child a fresh Harbor wall."""
    commands = []
    spawned_at = []
    outer_deadlines = []
    outer_deadlines_unix_ms = []

    class FakeEnvironment:
        environment_name = "ordinary"

        async def exec(self, _command):
            await asyncio.sleep(0.2)
            return type("Exec", (), {"return_code": 0, "stdout": "/workspace\n", "stderr": ""})()

    class DelayedBridge:
        def __init__(self, _environment, _workdir, _socket, _ledger, **_kwargs):
            pass

        async def start(self):
            await asyncio.sleep(1.1)
            return type("Binding", (), {"socket": "/tmp/bridge.sock", "nonce": "nonce", "workdir": "/workspace"})()

        def set_deadline(self, _deadline):
            pass

        async def close(self):
            return None

    class StubbornChild:
        returncode = None

        async def communicate(self):
            await asyncio.Event().wait()

        def terminate(self):
            pass  # Deliberately ignores TERM.

        def kill(self):
            self.returncode = -9  # A truly stubborn child cannot fabricate evidence.

        async def wait(self):
            while self.returncode is None:
                await asyncio.sleep(0)
            return self.returncode

    async def spawn(*command, **_kwargs):
        commands.append(command)
        spawned_at.append(asyncio.get_running_loop().time())
        return StubbornChild()

    monkeypatch.setenv("HARBOR_AGENT_TIMEOUT_SEC", "10")
    monkeypatch.setattr("stella_harbor.agent.BridgeServer", DelayedBridge)
    monkeypatch.setattr("stella_harbor.agent.asyncio.create_subprocess_exec", spawn)
    agent = StellaAgent(tmp_path, stella_url="http://stella", model="gateway/test", binding_dir=str(tmp_path / "bindings"))
    agent.stop_confirm_sec = 1

    async def exercise():
        outer_deadlines.append(asyncio.get_running_loop().time() + 10.0)
        outer_deadlines_unix_ms.append(time.time_ns() // 1_000_000 + 10_000)
        with pytest.raises(RuntimeError, match="absolute parent watchdog"):
            await asyncio.wait_for(agent.run("work", FakeEnvironment(), object()), timeout=11.0)

    asyncio.run(exercise())
    output = tmp_path / "stella" / "result.json"
    assert not output.exists()
    assert commands
    finalize_by = int(commands[0][commands[0].index("--finalize-by-unix-ms") + 1])
    assert abs(finalize_by - (outer_deadlines_unix_ms[0] - 4_000)) <= 50
    assert spawned_at and spawned_at[0] < outer_deadlines[0]
    phases = read_phase_journal(tmp_path / "stella" / "adapter-phase-journal.jsonl", ADAPTER_PHASES)
    assert phases is not None
    assert [entry["phase"] for entry in phases][-2:] == ["kill", "reap"]


def test_absolute_outer_wall_allows_cooperative_typed_finalization(monkeypatch, tmp_path):
    commands = []
    spawned_at = []
    outer_deadlines = []
    outer_deadlines_unix_ms = []

    class Environment:
        environment_name = "ordinary"

        async def exec(self, _command):
            await asyncio.sleep(0.2)
            return type("Exec", (), {"return_code": 0, "stdout": "/workspace\n", "stderr": ""})()

    class Bridge:
        def __init__(self, *_args, **_kwargs):
            pass

        async def start(self):
            await asyncio.sleep(1.1)
            return type("Binding", (), {"socket": "/tmp/bridge.sock", "nonce": "nonce", "workdir": "/workspace"})()

        def set_deadline(self, _deadline):
            pass

        async def close(self):
            return None

    class CooperativeChild:
        returncode = 10
        terminated = False
        killed = False

        async def communicate(self):
            output = commands[0][commands[0].index("--output") + 1]
            with open(output, "w") as result:
                result.write('{"bridge_nonce":"nonce","turn_terminal_state":"stopped","disabled_tools_count":1,"timed_out":true,"failure_class":"adapter"}')
            return b"", b""

        def terminate(self):
            self.terminated = True

        def kill(self):
            self.killed = True

        async def wait(self):
            return self.returncode

    child = CooperativeChild()

    async def spawn(*command, **_kwargs):
        commands.append(command)
        spawned_at.append(asyncio.get_running_loop().time())
        return child

    monkeypatch.setenv("HARBOR_AGENT_TIMEOUT_SEC", "10")
    monkeypatch.setenv("OPENAI_BASE_URL", "http://gateway.test")
    monkeypatch.setattr("stella_harbor.agent.BridgeServer", Bridge)
    monkeypatch.setattr("stella_harbor.agent.asyncio.create_subprocess_exec", spawn)
    agent = StellaAgent(tmp_path, stella_url="http://stella", model="gateway/test", binding_dir=str(tmp_path / "bindings"))
    agent.stop_confirm_sec = 1

    async def exercise():
        outer_deadlines.append(asyncio.get_running_loop().time() + 10.0)
        outer_deadlines_unix_ms.append(time.time_ns() // 1_000_000 + 10_000)
        with pytest.raises(RuntimeError, match="Stella adapter evidence failure"):
            await asyncio.wait_for(agent.run("work", Environment(), type("Context", (), {})()), timeout=10.0)

    asyncio.run(exercise())
    assert not child.terminated and not child.killed
    finalize_by = int(commands[0][commands[0].index("--finalize-by-unix-ms") + 1])
    assert abs(finalize_by - (outer_deadlines_unix_ms[0] - 4_000)) <= 50
    assert spawned_at and spawned_at[0] < outer_deadlines[0]


def test_agent_reads_the_loop_exclusion_list(monkeypatch, tmp_path):
    monkeypatch.setenv("STELLA_EVAL_EXCLUDED_TOOLS", "edit,read,write")
    agent = StellaAgent(tmp_path, model="gateway/test", binding_dir=str(tmp_path / "bindings"))
    assert agent.excluded_tools == "edit,read,write"


def test_real_go_child_handles_clean_done_for_a_detached_turn_at_the_work_cutoff(monkeypatch, tmp_path):
    """A clean observer stream must not lend its finalization budget to a working turn."""
    repo = Path(__file__).resolve().parents[4]
    binary = tmp_path / "stella-eval-agent"
    subprocess.run(["go", "build", "-o", str(binary), "./cmd/stella-eval-agent"], cwd=repo, check=True)
    agent_timeout_sec = 12
    stop_confirm_sec = 2
    monkeypatch.setenv("HARBOR_AGENT_TIMEOUT_SEC", str(agent_timeout_sec))
    monkeypatch.setenv("OPENAI_BASE_URL", "http://gateway.test")
    monkeypatch.setenv("STELLA_EVAL_ADMIN_TOKEN", "test-token")

    kill_calls = []
    original_kill_child = kill_child

    async def record_parent_kill(proc, journal=None, *, reap_deadline=None):
        kill_calls.append(proc)
        return await original_kill_child(proc, journal, reap_deadline=reap_deadline)

    monkeypatch.setattr("stella_harbor.agent.kill_child", record_parent_kill)

    class Environment:
        environment_name = "ordinary"

        async def exec(self, command, **_kwargs):
            if command == "pwd":
                return type("Exec", (), {"return_code": 0, "stdout": "/workspace\n", "stderr": ""})()
            return type("Exec", (), {"return_code": 0, "stdout": "/home/agent\n/usr/bin:/bin\n/usr/bin/timeout\n", "stderr": ""})()

    async def run_case(hang_evidence):
        stop_times = []
        working_polls = []

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, _format, *_args):
                pass

            def json(self, payload, status=200):
                body = json.dumps(payload).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def do_POST(self):
                if self.path == "/api/provisioned-users":
                    return self.json({"provisioned_user": {"id": "provisioned"}, "token": "trial"}, 201)
                if self.path == "/api/agents":
                    return self.json({"id": "agent"})
                if self.path == "/api/agents/agent/sessions":
                    return self.json({"id": "session"})
                if self.path == "/api/agents/agent/sessions/session/stop":
                    stop_times.append(time.monotonic())
                    self.send_response(204)
                    return self.end_headers()
                if self.path == "/api/agents/agent/sessions/session/messages":
                    # The observer sees a clean terminal-looking SSE sequence
                    # before its work cutoff, but the server-owned turn remains
                    # working through that cutoff.
                    time.sleep(3)
                    self.send_response(200)
                    self.send_header("Content-Type", "text/event-stream")
                    self.end_headers()
                    self.wfile.write(b'data: {"type":"finish"}\n\ndata: [DONE]\n\n')
                    self.wfile.flush()
                    return
                if self.path == "/api/provisioned-users/provisioned/deactivate":
                    self.send_response(204)
                    return self.end_headers()
                self.send_response(204)
                self.end_headers()

            def do_GET(self):
                path = self.path.split("?", 1)[0]
                if path == "/api/status":
                    return self.json({"sandbox_backend": "bridge"})
                if path == "/api/auth/me":
                    return self.json({"id": "account"})
                if path == "/api/agents/agent/tools":
                    return self.json({"tools": [{"name": "bash", "source": "core", "enabled": True}, {"name": "web_search", "source": "plugin", "enabled": True}]})
                if path == "/api/agents/agent/sessions/session":
                    if not stop_times:
                        working_polls.append(time.monotonic())
                        return self.json({"activity_status": "working"})
                    return self.json({"activity_status": "stopped"})
                if path == "/api/agents/agent/sessions/session/messages":
                    if hang_evidence:
                        time.sleep(10)
                        return
                    return self.json({"messages": [{"role": "assistant", "token_count": 1}]})
                if path == "/api/agents/agent/sessions/session/usage":
                    return self.json({"pending_call_count": 0})
                self.send_response(404)
                self.end_headers()

            def do_PATCH(self):
                self.send_response(204)
                self.end_headers()

            def do_DELETE(self):
                self.send_response(204)
                self.end_headers()

        class Server(http.server.ThreadingHTTPServer):
            daemon_threads = True

        server = Server(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            logs = tmp_path / ("hang" if hang_evidence else "complete")
            context = type("Context", (), {})()
            agent = StellaAgent(logs, stella_url=f"http://127.0.0.1:{server.server_port}", model="gateway/test", binding_dir=str(logs / "bindings"), eval_agent_bin=str(binary))
            agent.stop_confirm_sec = stop_confirm_sec
            started = time.monotonic()
            if hang_evidence:
                with pytest.raises(RuntimeError, match="Stella adapter evidence failure"):
                    await agent.run("work", Environment(), context)
            else:
                await agent.run("work", Environment(), context)
            elapsed = time.monotonic() - started
            result = context.metadata["stella_result"]
            work_cutoff = agent_timeout_sec - min(
                agent_timeout_sec * agent.PARENT_POST_GO_RESERVE_RATIO,
                agent.PARENT_POST_GO_RESERVE_CEILING_SEC,
            ) - stop_confirm_sec
            assert working_polls
            assert len(stop_times) == 1
            # The old finalization-context wait delayed this POST until roughly
            # work_cutoff + stop_confirm_sec. That violates this bound before a
            # parent watchdog is needed to demonstrate the regression.
            assert work_cutoff - 0.75 <= stop_times[0] - started <= work_cutoff + 0.75
            assert elapsed < agent_timeout_sec
            assert result["timed_out"] is True
            if hang_evidence:
                assert result["failure_class"] == "adapter"
                assert result["valid"] is False
                assert context.metadata["stella_exit_code"] == 10
            else:
                assert result["valid"] is True
                assert context.metadata["stella_exit_code"] == 12
        finally:
            server.shutdown()
            server.server_close()

    async def exercise():
        await asyncio.gather(run_case(False), run_case(True))

    asyncio.run(exercise())
    assert not kill_calls


@pytest.mark.skipif(os.environ.get("STELLA_RUN_REAL_EVAL_SEAM") != "1",
                    reason="real subprocess seam is opt-in; it owns an embedded PostgreSQL testbed")
def test_real_no_model_specialized_watchdog_seam(monkeypatch, tmp_path):
    """Exercise kill/reap recovery then a natural scripted turn on one testbed.

    This owns a fresh real testbed, compiled child, bridge socket, HMAC MCP
    fixture, Memory/Library lease, and loopback Responses SSE provider. It
    records phases only, never fixture material or credential values.
    """
    repo = Path(__file__).resolve().parents[4]
    subprocess.run(["bash", "test/evals/harbor/eval_build.sh"], cwd=repo, check=True)
    workspace = tmp_path / "workspace"
    workspace.mkdir()
    bridge_dir = tmp_path / "bindings"
    bridge_dir.mkdir(mode=0o700)
    port_socket = socket.socket()
    port_socket.bind(("127.0.0.1", 0))
    port = port_socket.getsockname()[1]
    port_socket.close()

    provider_started = threading.Event()
    provider_release = threading.Event()
    verdict_uploads = []
    provider_mode = "stalled"
    provider_mode_lock = threading.Lock()

    class FakeResponses(http.server.BaseHTTPRequestHandler):
        def log_message(self, _format, *_args):
            pass

        def do_POST(self):
            if self.path != "/v1/responses":
                self.send_error(404)
                return
            # Drain but never parse a model request: it can contain fixture
            # material, IDs, or credentials outside this seam's phase contract.
            self.rfile.read(int(self.headers.get("Content-Length", "0")))
            with provider_mode_lock:
                mode = provider_mode
            provider_started.set()
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            self.wfile.write(b'event: response.created\ndata: {"type":"response.created"}\n\n')
            self.wfile.flush()
            if mode == "stalled":
                # Deliberately outlives working time, like an upstream SSE that
                # accepted the request but has not finished the turn.
                provider_release.wait(30)
                return
            completed = {
                "type": "response.completed", "sequence_number": 1,
                "response": {"id": "seam", "object": "response", "created_at": 0,
                             "status": "completed", "model": "seam", "output": [],
                             "usage": {"input_tokens": 1, "input_tokens_details": {"cached_tokens": 0},
                                       "output_tokens": 1, "output_tokens_details": {"reasoning_tokens": 0},
                                       "total_tokens": 2}},
            }
            self.wfile.write(b"event: response.completed\ndata: " + json.dumps(completed).encode() + b"\n\n")
            self.wfile.flush()

    fake = http.server.ThreadingHTTPServer(("127.0.0.1", 0), FakeResponses)
    fake_thread = threading.Thread(target=fake.serve_forever, daemon=True)
    fake_thread.start()

    env = os.environ.copy()
    env.update({
        "STELLA_TESTBED_PORT": str(port),
        "STELLA_SANDBOX_BACKEND": "bridge",
        "STELLA_EVAL_BRIDGE_DIR": str(bridge_dir),
        "STELLA_EVAL_FIXTURE_PLAN_SEED": "seam-only-fixture-seed",
    })
    setup_started = time.monotonic()
    testbed = subprocess.Popen([str(repo / "dist/bin/testbed"), "start"], cwd=repo, env=env,
                               stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    output_lines: queue.Queue[str] = queue.Queue()

    def pump_testbed_output():
        assert testbed.stdout is not None
        for line in testbed.stdout:
            output_lines.put(line)

    threading.Thread(target=pump_testbed_output, daemon=True).start()
    credentials_path = None
    try:
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline and credentials_path is None:
            if testbed.poll() is not None:
                pytest.fail("real testbed exited before publishing credentials")
            try:
                line = output_lines.get(timeout=0.2)
            except queue.Empty:
                continue
            if "Credentials: " in line:
                credentials_path = Path(line.split("Credentials: ", 1)[1].strip())
        assert credentials_path is not None and credentials_path.is_file()
        assert time.monotonic() - setup_started <= 120, "testbed setup exceeded independent 120s ceiling"
        credentials = json.loads(credentials_path.read_text())
        base_url = credentials["base_url"]

        def post(path, payload, token="", opener=None):
            request = urllib.request.Request(base_url + path, data=json.dumps(payload).encode(), method="POST")
            request.add_header("Content-Type", "application/json")
            if token:
                request.add_header("Authorization", "Bearer " + token)
            transport = opener.open if opener is not None else urllib.request.urlopen
            with transport(request, timeout=10) as response:
                return json.loads(response.read() or b"{}")

        provider_id = "seam-provider"
        post("/api/providers", {
            "id": provider_id, "type": "openai-response", "name": "local watchdog seam", "enabled": True,
            "api_key": "local-test-key", "base_url": f"http://127.0.0.1:{fake.server_port}/v1",
            "models": {"seam": {"enabled": True}},
        }, credentials["admin"]["token"])
        jar = CookieJar()
        opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
        post("/api/auth/local/login", {"email": credentials["admin"]["email"], "password": credentials["admin"]["password"]}, opener=opener)
        provisioning = post("/api/admin/provisioning-tokens", {
            "name": "watchdog-seam", "expires_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(time.time() + 3600)),
        }, opener=opener)

        class LocalBridgeEnvironment:
            environment_name = "memory-library-evidence"
            timeout_binary_probe = "/home/agent\n/usr/bin:/bin\n/usr/bin/timeout\n"

            async def exec(self, command, **_kwargs):
                if command == "pwd":
                    return type("Exec", (), {"return_code": 0, "stdout": str(workspace) + "\\n", "stderr": ""})()
                # The seam does not execute a model tool, but BridgeServer must
                # still negotiate its real socket and container-like discovery.
                if "command -v timeout" in command:
                    return type("Exec", (), {"return_code": 0, "stdout": self.timeout_binary_probe, "stderr": ""})()
                # The no-model natural turn cannot produce the required
                # artifact. Its verifier must receive a scoreable miss through
                # the real bridge, not an adapter failure from this seam shim.
                if "if [ -L " in command:
                    return type("Exec", (), {"return_code": 3, "stdout": "", "stderr": ""})()
                if command.startswith("rm -rf /root/.stella-harbor-verdict") or command.startswith("chown root:root /root/.stella-harbor-verdict"):
                    return type("Exec", (), {"return_code": 0, "stdout": "", "stderr": ""})()
                raise AssertionError(f"unexpected bridge environment command: {command}")

            async def upload_file(self, _source, destination):
                assert destination.startswith("/root/.stella-harbor-verdict/")
                verdict_uploads.append(destination)

        assert LocalBridgeEnvironment.timeout_binary_probe.splitlines() == [
            "/home/agent", "/usr/bin:/bin", "/usr/bin/timeout",
        ]

        # Capture the genuine child only to freeze it after its durable stream
        # phase. No Stella route is mocked or replaced.
        original_spawn = asyncio.create_subprocess_exec
        child_holder = []

        async def capture_spawn(*args, **kwargs):
            child = await original_spawn(*args, **kwargs)
            child_holder.append(child)
            return child

        recoveries = []
        recovery_completed_at = []
        from stella_harbor import agent as adapter_module
        original_recovery = adapter_module.finalize_fixture_cleanup

        async def capture_recovery(*args, **kwargs):
            outcome = await original_recovery(*args, **kwargs)
            recoveries.append(outcome)
            recovery_completed_at.append(time.monotonic())
            return outcome

        monkeypatch.setattr("stella_harbor.agent.asyncio.create_subprocess_exec", capture_spawn)
        monkeypatch.setattr("stella_harbor.agent.finalize_fixture_cleanup", capture_recovery)
        monkeypatch.setenv("HARBOR_AGENT_TIMEOUT_SEC", "15")
        monkeypatch.setenv("STELLA_EVAL_ADMIN_TOKEN", provisioning["token"])
        monkeypatch.setenv("OPENAI_BASE_URL", f"http://127.0.0.1:{fake.server_port}/v1")
        def new_agent(name):
            agent = StellaAgent(tmp_path / name, stella_url=base_url, model=provider_id + "/seam",
                                binding_dir=str(bridge_dir), eval_agent_bin=str(repo / "dist/bin-eval/stella-eval-agent"),
                                excluded_tools="view_image,vllm")
            agent.stop_confirm_sec = 2
            agent.fixture_config = str(credentials_path.parent / "testbed-mcp-fixture.json")
            # Each real child must own a separate provisioned-user and lease.
            agent.context_id = "seam-" + name
            return agent

        sigstop_agent = new_agent("sigstop")

        async def freeze_after_stream_phase():
            deadline = time.monotonic() + 10
            while time.monotonic() < deadline:
                path = tmp_path / "sigstop" / "stella" / "driver-phase-journal.jsonl"
                phases = read_phase_journal(path, DRIVER_PHASES)
                if child_holder and provider_started.is_set() and phases and phases[-1]["phase"] == "stream_start":
                    os.kill(child_holder[-1].pid, signal.SIGSTOP)
                    return
                await asyncio.sleep(0.02)
            raise AssertionError("compiled child did not reach stream_start")

        async def sigstop_case():
            freezer = asyncio.create_task(freeze_after_stream_phase())
            started = time.monotonic()
            with pytest.raises(RuntimeError, match="absolute parent watchdog"):
                await sigstop_agent.run("use the seeded facts and keep working", LocalBridgeEnvironment(), type("Context", (), {})())
            await freezer
            return started, time.monotonic() - started

        sigstop_started, sigstop_elapsed = asyncio.run(sigstop_case())
        assert provider_started.is_set()
        assert sigstop_elapsed <= 15
        assert recoveries == [{"outcome": "recovered"}]
        assert recovery_completed_at[-1] <= sigstop_started + 15
        sigstop_adapter_phases = read_phase_journal(tmp_path / "sigstop" / "stella" / "adapter-phase-journal.jsonl", ADAPTER_PHASES)
        sigstop_driver_phases = read_phase_journal(tmp_path / "sigstop" / "stella" / "driver-phase-journal.jsonl", DRIVER_PHASES)
        assert sigstop_adapter_phases is not None and [entry["phase"] for entry in sigstop_adapter_phases][-3:] == ["watchdog_fire", "kill", "reap"]
        assert sigstop_driver_phases is not None and sigstop_driver_phases[-1]["phase"] == "stream_start"

        # The first request remains in a daemon provider thread after its child
        # is killed. Release it before reusing this owned testbed for the
        # natural scripted subcase.
        provider_release.set()
        with provider_mode_lock:
            provider_mode = "completed"
        provider_started.clear()
        natural_agent = new_agent("natural")

        async def natural_case():
            context = type("Context", (), {})()
            # The scripted provider makes no model tool call. It must still
            # self-exit with a typed verifier result, not reach the watchdog.
            await natural_agent.run("use the seeded facts and keep working", LocalBridgeEnvironment(), context)
            return context

        natural_context = asyncio.run(natural_case())
        assert provider_started.is_set()
        natural_result = natural_context.metadata["stella_result"]
        assert isinstance(natural_result, dict)
        assert natural_result["valid"] is True
        assert natural_result["host_verdict"] == {
            "version": 1, "task_id": "memory-library-evidence", "valid": True,
            "reward": 0, "reasons": ["required evidence artifact is missing"], "nonce": natural_result["bridge_nonce"],
        }
        assert natural_context.metadata["stella_exit_code"] == 0
        assert sorted(verdict_uploads) == [
            "/root/.stella-harbor-verdict/payload.json",
            "/root/.stella-harbor-verdict/public.pem",
            "/root/.stella-harbor-verdict/signature.bin",
        ]
        natural_adapter_phases = read_phase_journal(tmp_path / "natural" / "stella" / "adapter-phase-journal.jsonl", ADAPTER_PHASES)
        natural_driver_phases = read_phase_journal(tmp_path / "natural" / "stella" / "driver-phase-journal.jsonl", DRIVER_PHASES)
        assert natural_adapter_phases is not None
        assert natural_driver_phases is not None
        natural_adapter_phase_names = [entry["phase"] for entry in natural_adapter_phases]
        assert "watchdog_fire" not in natural_adapter_phase_names
        assert "kill" not in natural_adapter_phase_names
        assert natural_adapter_phase_names[-1] == "result_seen"
        natural_driver_phase_names = [entry["phase"] for entry in natural_driver_phases]
        verification = natural_driver_phase_names.index("verification_start")
        assert natural_driver_phase_names[verification:verification + 3] == [
            "verification_start", "verification_return", "evidence_start",
        ]
        print("real no-model seam phases:", {
            "sigstop_adapter": [entry["phase"] for entry in sigstop_adapter_phases],
            "sigstop_driver_last": sigstop_driver_phases[-1]["phase"],
            "natural_adapter": natural_adapter_phase_names,
            "natural_driver": natural_driver_phase_names,
        })
    finally:
        teardown_started = time.monotonic()
        provider_release.set()
        fake.shutdown()
        fake.server_close()
        try:
            subprocess.run([str(repo / "dist/bin/testbed"), "stop"], cwd=repo, env=env, timeout=90, check=False,
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        except subprocess.TimeoutExpired:
            pass
        with contextlib.suppress(subprocess.TimeoutExpired):
            testbed.wait(timeout=5)
        if testbed.poll() is None:
            testbed.kill()
            testbed.wait(timeout=5)
        assert time.monotonic() - teardown_started <= 90, "testbed teardown exceeded independent 90s ceiling"


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
