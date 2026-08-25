"""Bridge behaviours that only show up against a real container filesystem."""

from __future__ import annotations

import asyncio
import base64
import json
import os
import shutil
import socket
import tempfile
from dataclasses import dataclass
from pathlib import Path

import pytest

from stella_harbor.bridge import BridgeError, BridgeServer


@dataclass
class _Result:
    stdout: str = ""
    stderr: str = ""
    return_code: int = 0


class _FakeEnv:
    """A container with a regular five-byte file."""

    def __init__(self) -> None:
        self.downloaded: list[str] = []

    async def exec(self, command: str, **kwargs) -> _Result:
        if "-d " in command:  # stat probe
            return _Result(stdout="f 5\n")
        return _Result()

    async def download_file(self, path: str, local: Path) -> None:
        self.downloaded.append(path)
        local.write_bytes(b"hello")


class _LocalEnv(_FakeEnv):
    """Run a bounded stat shell probe against host-created file shapes."""

    async def exec(self, command: str, **kwargs) -> _Result:
        proc = await asyncio.create_subprocess_shell(
            command, cwd=kwargs.get("cwd"), stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        stdout, stderr = await asyncio.wait_for(proc.communicate(), kwargs["timeout_sec"])
        return _Result(stdout=stdout.decode(), stderr=stderr.decode(), return_code=proc.returncode)


def _ready_server(env, tmp_path, **kwargs) -> BridgeServer:
    server = BridgeServer(env, "/app", tmp_path / "s.sock", tmp_path / "l.jsonl", **kwargs)
    server._has_timeout_bin = True
    return server


@pytest.mark.parametrize("shape", ["fifo", "symlink", "device", "socket"])
def test_read_file_refuses_non_regular_artifacts_without_downloading_them(tmp_path, shape):
    # `wc -c` opens FIFOs and devices. The target shape is task output, so it
    # must become a scoreable miss before the bridge attempts any byte read.
    target = tmp_path / shape
    if shape == "fifo":
        os.mkfifo(target)
    elif shape == "symlink":
        (tmp_path / "regular").write_text("hello")
        target.symlink_to(tmp_path / "regular")
    elif shape == "device":
        target = Path("/dev/null")
    else:
        socket_dir = Path(tempfile.mkdtemp(prefix="sb-", dir="/tmp"))
        target = socket_dir / "s"
        listener = socket.socket(socket.AF_UNIX)
        listener.bind(str(target))
    env = _LocalEnv()
    server = _ready_server(env, tmp_path)
    server.workdir = str(tmp_path)

    async def local_stat(command, **kwargs):
        # macOS lacks GNU `timeout`; execute the exact stat probe directly while
        # the outer one-second wall proves a FIFO never reaches `wc -c`.
        return await server._exec(command, cwd=server.workdir, timeout_sec=kwargs["timeout_sec"])

    server._exec_bounded = local_stat  # type: ignore[method-assign]

    async def read_non_regular() -> None:
        with pytest.raises(BridgeError, match="regular file") as raised:
            await server._op_read_file({"path": str(target)})
        assert raised.value.code == "non_regular"

    try:
        asyncio.run(asyncio.wait_for(read_non_regular(), timeout=1))
    finally:
        if shape == "socket":
            listener.close()
            shutil.rmtree(socket_dir)
    assert env.downloaded == []


def test_read_file_too_large_response_includes_structured_size_and_limit(tmp_path):
    from stella_harbor.bridge import MAX_PAYLOAD

    env = _FakeEnv()

    async def exec_with_large_stat(command: str, **kwargs) -> _Result:
        if "-d " in command:
            return _Result(stdout=f"f {MAX_PAYLOAD + 7}\n")
        return _Result()

    env.exec = exec_with_large_stat  # type: ignore[method-assign]
    server = _ready_server(env, tmp_path)

    class _Writer:
        def __init__(self) -> None:
            self.data = b""

        def write(self, data: bytes) -> None:
            self.data += data

        async def drain(self) -> None:
            pass

        def close(self) -> None:
            pass

    async def call_server() -> dict:
        reader = asyncio.StreamReader()
        reader.feed_data(json.dumps({"nonce": server.nonce, "op": "read_file", "path": "/app/input.csv"}).encode())
        reader.feed_eof()
        writer = _Writer()
        await server._handle(reader, writer)  # type: ignore[arg-type]
        return json.loads(writer.data)

    response = asyncio.run(call_server())

    assert response["code"] == "too_large"
    assert response["size"] == MAX_PAYLOAD + 7
    assert response["limit"] == MAX_PAYLOAD
    assert "/app/input.csv" in response["error"]


def test_socket_binds_even_when_the_job_path_is_long(tmp_path):
    # sun_path is 104 bytes on macOS; Harbor's job/timestamp/task__id/agent/stella
    # log path plus a repo checkout blows past it, and the failure is a hard
    # OSError at trial start that a longer job name is enough to trigger.
    deep = tmp_path / ("d" * 60) / ("e" * 60) / "agent" / "stella"
    server = BridgeServer(_FakeEnv(), "/app", deep / "bridge.sock", tmp_path / "l.jsonl")

    bind = server._short_socket_path()

    assert len(str(bind).encode()) <= BridgeServer.SUN_PATH_MAX
    assert bind != server.socket_path
    assert server._bind_dir is not None and server._bind_dir.is_dir()
    import shutil

    shutil.rmtree(server._bind_dir)


def test_short_job_paths_keep_the_socket_next_to_the_ledger(tmp_path):
    # pytest's own tmp_path is already near the limit on macOS, so the short
    # case has to be built somewhere genuinely short.
    import shutil
    import tempfile

    short = Path(tempfile.mkdtemp(prefix="t", dir="/tmp"))
    try:
        server = BridgeServer(_FakeEnv(), "/app", short / "b.sock", tmp_path / "l.jsonl")

        assert server._short_socket_path() == server.socket_path
        assert server._bind_dir is None
    finally:
        shutil.rmtree(short)


def test_a_request_split_across_segments_is_read_whole(tmp_path):
    # StreamReader.read(n) returns what has arrived, not n bytes. A large edit
    # spans several segments, so reading once parsed a prefix and failed as
    # malformed; small payloads always fit, which is why it only showed up on a
    # task with big edits.
    payload = json.dumps({"op": "write_file", "data": "x" * 200_000}).encode()
    server = BridgeServer(_FakeEnv(), "/app", tmp_path / "s.sock", tmp_path / "l.jsonl")

    async def read_it() -> bytes:
        reader = asyncio.StreamReader()
        for i in range(0, len(payload), 4096):
            reader.feed_data(payload[i:i + 4096])
        reader.feed_eof()
        return await server._read_request(reader)

    assert asyncio.run(read_it()) == payload


def test_an_oversized_request_is_refused_rather_than_buffered(tmp_path):
    from stella_harbor.bridge import MAX_PAYLOAD, BridgeError

    server = BridgeServer(_FakeEnv(), "/app", tmp_path / "s.sock", tmp_path / "l.jsonl")

    async def read_it() -> bytes:
        reader = asyncio.StreamReader()
        reader.feed_data(b"x" * (MAX_PAYLOAD + (2 << 20)))
        reader.feed_eof()
        return await server._read_request(reader)

    try:
        asyncio.run(read_it())
    except BridgeError as e:
        assert e.code == "too_large"
    else:
        raise AssertionError("oversized request was accepted")


def test_exec_reports_a_timeout_as_exit_code_minus_one(tmp_path):
    # Harbor raises a bare RuntimeError when the command outlives timeout_sec.
    # Passing that through as an adapter fault is wrong twice: the agent gets an
    # opaque "internal" error instead of the bash tool's timeout message, and
    # the ledger counts a fault that voids the whole trial.
    env = _FakeEnv()

    async def exec_that_times_out(command: str, **kwargs) -> _Result:
        raise RuntimeError(f"Command timed out after {kwargs['timeout_sec']} seconds")

    env.exec = exec_that_times_out  # type: ignore[method-assign]
    server = _ready_server(env, tmp_path)

    out = asyncio.run(server._op_exec({"command": "sleep 999", "timeout_sec": 120}))

    assert out["ok"] is True
    assert out["return_code"] == -1
    assert "timed out after 120 seconds" in out["stderr"]


def test_exec_still_fails_loudly_when_the_environment_breaks(tmp_path):
    env = _FakeEnv()

    async def exec_that_breaks(command: str, **kwargs) -> _Result:
        raise RuntimeError("container is not running")

    env.exec = exec_that_breaks  # type: ignore[method-assign]
    server = _ready_server(env, tmp_path)

    try:
        asyncio.run(server._op_exec({"command": "ls", "timeout_sec": 120}))
    except RuntimeError as e:
        assert "not running" in str(e)
    else:
        raise AssertionError("a broken environment must not look like a timeout")


def test_exec_without_a_timeout_is_bounded_by_the_trial(tmp_path):
    # The wedge: a command the model sent without a timeout ran unbounded, spun
    # a core for 3.5 hours, and held the whole Harbor job open.
    env = _FakeEnv()
    seen: list[tuple[str, int | None]] = []

    async def record(command: str, **kwargs) -> _Result:
        seen.append((command, kwargs.get("timeout_sec")))
        return _Result()

    env.exec = record  # type: ignore[method-assign]
    server = _ready_server(env, tmp_path, budget_sec=600)
    server._deadline = __import__("time").monotonic() + 600

    asyncio.run(server._op_exec({"command": "python3 -c 'while True: pass'"}))

    command, client_timeout = seen[0]
    assert command.startswith("timeout -k 5s "), command
    assert "while True" in command
    # The client cannot outlive the absolute bridge cutoff, and SIGKILL is
    # reserved inside that same wall.
    assert client_timeout is not None and client_timeout <= 600


def test_transfer_refuses_an_exhausted_working_deadline(tmp_path):
    server = _ready_server(_FakeEnv(), tmp_path)
    server._deadline = __import__("time").monotonic() + 0.5

    async def hangs():
        await asyncio.Event().wait()

    with pytest.raises(BridgeError, match="working deadline exhausted"):
        asyncio.run(server._transfer_within_work_deadline(hangs()))


def test_exec_refuses_a_window_that_cannot_fit_term_and_sigkill(tmp_path):
    server = _ready_server(_FakeEnv(), tmp_path)
    server._deadline = __import__("time").monotonic() + 1

    with pytest.raises(BridgeError, match="working deadline exhausted"):
        server._bounded("sleep 999", None)


def test_exec_reports_a_container_side_kill_as_a_timeout(tmp_path):
    # `timeout` exits 124 (or 137 after SIGKILL). The agent must see the same
    # answer it gets when the client-side deadline fires, or it learns that a
    # timeout is an unexplained failure.
    env = _FakeEnv()

    async def killed(command: str, **kwargs) -> _Result:
        return _Result(stdout="partial", return_code=124)

    env.exec = killed  # type: ignore[method-assign]
    server = _ready_server(env, tmp_path)

    out = asyncio.run(server._op_exec({"command": "sleep 999", "timeout_sec": 30}))

    assert out["return_code"] == -1
    assert "timed out after 30 seconds" in out["stderr"]
    assert out["stdout"] == "partial"


def test_exec_refuses_to_run_without_the_timeout_binary(tmp_path):
    # Cancelling a host-side docker client does not kill its container child, so
    # weaker images cannot safely participate in an evaluation trial.
    env = _FakeEnv()
    server = BridgeServer(env, "/app", tmp_path / "s.sock", tmp_path / "l.jsonl")
    from stella_harbor.bridge import BridgeError

    try:
        asyncio.run(server._op_exec({"command": "ls", "timeout_sec": 30}))
    except BridgeError as e:
        assert "timeout" in str(e)
    else:
        raise AssertionError("bridge accepted an unbounded container command")


def test_close_cancels_an_inflight_handler(tmp_path):
    server = _ready_server(_FakeEnv(), tmp_path)
    started = asyncio.Event()

    async def block(_req):
        started.set()
        await asyncio.Future()

    server._dispatch = block  # type: ignore[method-assign]

    class _Writer:
        def write(self, _data):
            pass

        async def drain(self):
            pass

        def close(self):
            pass

    async def close_it():
        reader = asyncio.StreamReader()
        reader.feed_data(json.dumps({"nonce": server.nonce, "op": "exec"}).encode())
        reader.feed_eof()
        task = asyncio.create_task(server._serve_connection(reader, _Writer()))
        await started.wait()
        await server.close()
        assert task.cancelled()

    asyncio.run(close_it())
