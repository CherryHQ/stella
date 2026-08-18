"""Development harness for the bridge: serve a plain `docker run` container.

Used by the Go contract test (plugins/sandbox/bridge, tag `evalbridge`) and for
manual poking. Real trials use Harbor's own environment object; this shim only
duck-types the four methods bridge.py needs (exec, upload_file, upload_dir,
download_file) on top of the docker CLI.

    python -m stella_harbor.bridge_dev --container <id> --workdir /app \
        --binding-dir /tmp/bindings --user-id <stella-user-id> --socket /tmp/b.sock
"""

from __future__ import annotations

import argparse
import asyncio
import shlex
import signal
from pathlib import Path

from harbor.environments.base import ExecResult

from .bridge import BridgeServer


class DockerCliEnvironment:
    def __init__(self, container: str, user: str | None = None):
        self.container = container
        self.default_user = user

    async def _run(self, *argv: str, stdin: bytes | None = None, timeout: float | None = None) -> tuple[int, bytes, bytes]:
        proc = await asyncio.create_subprocess_exec(
            *argv,
            stdin=asyncio.subprocess.PIPE if stdin is not None else None,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        try:
            out, err = await asyncio.wait_for(proc.communicate(stdin), timeout=timeout)
        except asyncio.TimeoutError:
            proc.kill()
            await proc.wait()
            return 124, b"", b"timeout"
        return proc.returncode or 0, out, err

    async def exec(self, command: str, cwd: str | None = None, env: dict[str, str] | None = None, timeout_sec: int | None = None, user=None) -> ExecResult:
        argv = ["docker", "exec"]
        if cwd:
            argv += ["-w", cwd]
        for k, v in (env or {}).items():
            argv += ["-e", f"{k}={v}"]
        u = user or self.default_user
        if u is not None:
            argv += ["-u", str(u)]
        argv += [self.container, "sh", "-c", command]
        rc, out, err = await self._run(*argv, timeout=timeout_sec)
        return ExecResult(stdout=out.decode(errors="replace"), stderr=err.decode(errors="replace"), return_code=rc)

    async def upload_file(self, source_path, target_path: str) -> None:
        rc, _, err = await self._run("docker", "cp", str(source_path), f"{self.container}:{target_path}")
        if rc != 0:
            raise RuntimeError(f"docker cp failed: {err.decode(errors='replace')}")

    async def upload_dir(self, source_dir, target_dir: str) -> None:
        rc, _, err = await self._run("docker", "cp", f"{source_dir}/.", f"{self.container}:{target_dir}")
        if rc != 0:
            raise RuntimeError(f"docker cp failed: {err.decode(errors='replace')}")

    async def download_file(self, source_path: str, target_path) -> None:
        rc, _, err = await self._run("docker", "cp", f"{self.container}:{source_path}", str(target_path))
        if rc != 0:
            raise RuntimeError(f"docker cp failed: {err.decode(errors='replace')}")


async def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--container", required=True)
    ap.add_argument("--workdir", default="/app")
    ap.add_argument("--binding-dir", required=True)
    ap.add_argument("--user-id", required=True)
    ap.add_argument("--socket", required=True)
    ap.add_argument("--ledger", default=None)
    ap.add_argument("--user", default=None, help="container user for exec")
    ap.add_argument("--path-prepend", default="")
    args = ap.parse_args()

    env = DockerCliEnvironment(args.container, user=args.user)
    server = BridgeServer(
        env=env,
        workdir=args.workdir,
        socket_path=Path(args.socket),
        ledger_path=Path(args.ledger or (args.socket + ".ledger.jsonl")),
        tool_path_prepend=args.path_prepend,
        user=args.user,
    )
    binding = await server.start()
    binding.write(Path(args.binding_dir), args.user_id)
    print(f"READY nonce={binding.nonce} socket={binding.socket}", flush=True)

    stop = asyncio.Event()
    loop = asyncio.get_running_loop()
    for sig in (signal.SIGINT, signal.SIGTERM):
        loop.add_signal_handler(sig, stop.set)
    await stop.wait()
    await server.close()


if __name__ == "__main__":
    asyncio.run(main())
