"""Bridge behaviours that only show up against a real container filesystem."""

from __future__ import annotations

import asyncio
import base64
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
