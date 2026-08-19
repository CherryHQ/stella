"""Bridge behaviours that only show up against a real container filesystem."""

from __future__ import annotations

import asyncio
import base64
import json
from dataclasses import dataclass
from pathlib import Path

from stella_harbor.bridge import BridgeServer


@dataclass
class _Result:
    stdout: str = ""
    stderr: str = ""
    return_code: int = 0


class _FakeEnv:
    """A container where /link is a symlink to /real/file."""

    def __init__(self) -> None:
        self.downloaded: list[str] = []

    async def exec(self, command: str, **kwargs) -> _Result:
        if "readlink" in command:
            return _Result(stdout="/real/file\n")
        if "-d " in command:  # stat probe
            return _Result(stdout="f 5\n")
        return _Result()

    async def download_file(self, path: str, local: Path) -> None:
        self.downloaded.append(path)
        local.write_bytes(b"hello")


def test_read_file_downloads_the_symlink_target_not_the_link(tmp_path):
    # docker cp copies a symlink as a symlink, so the host copy dangles and the
    # read fails with an opaque error. Resolving first is what keeps read
    # consistent with what bash sees inside the container.
    env = _FakeEnv()
    server = BridgeServer(env, "/app", tmp_path / "s.sock", tmp_path / "l.jsonl")

    out = asyncio.run(server._op_read_file({"path": "/link"}))

    assert env.downloaded == ["/real/file"]
    assert base64.b64decode(out["data"]) == b"hello"


def test_read_file_falls_back_to_the_original_path_when_readlink_is_absent(tmp_path):
    env = _FakeEnv()

    async def exec_without_readlink(command: str, **kwargs) -> _Result:
        if "readlink" in command:
            return _Result(stdout="/link\n")
        return _Result(stdout="f 5\n")

    env.exec = exec_without_readlink  # type: ignore[method-assign]
    server = BridgeServer(env, "/app", tmp_path / "s.sock", tmp_path / "l.jsonl")

    asyncio.run(server._op_read_file({"path": "/link"}))

    assert env.downloaded == ["/link"]


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
