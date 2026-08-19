"""Per-trial bridge: Stella's `bridge` sandbox backend dials this Unix socket.

Wire protocol (mirrors plugins/sandbox/bridge/client.go): one JSON request per
connection, one JSON response, then close. Every request carries the trial
nonce; a mismatch is refused with code "bad_nonce" so a session bound to
another trial can never execute here.

Ops: ping, exec, stat, read_dir, read_file, write_file, project.
Every op is appended to a JSONL ledger so the result can prove which calls
reached the container (ADR §9 pass predicate).
"""

from __future__ import annotations

import asyncio
import base64
import json
import os
import secrets
import shlex
import shutil
import tempfile
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from harbor.environments.base import BaseEnvironment

# JSON payload cap; must match maxPayloadBytes in client.go.
MAX_PAYLOAD = 32 << 20


class BridgeError(Exception):
    def __init__(self, code: str, message: str):
        super().__init__(message)
        self.code = code


@dataclass
class Binding:
    socket: str
    nonce: str
    workdir: str
    home: str = ""
    temp_dir: str = ""
    path: str = ""

    def write(self, binding_dir: Path, user_id: str) -> Path:
        binding_dir.mkdir(parents=True, exist_ok=True)
        target = binding_dir / f"{user_id}.json"
        tmp = target.with_suffix(".json.tmp")
        tmp.write_text(json.dumps(self.__dict__))
        os.chmod(tmp, 0o600)
        tmp.replace(target)
        return target


@dataclass
class BridgeServer:
    env: BaseEnvironment
    workdir: str
    socket_path: Path
    ledger_path: Path
    nonce: str = field(default_factory=lambda: secrets.token_hex(16))
    tool_path_prepend: str = ""
    user: str | int | None = None
    _server: asyncio.AbstractServer | None = None
    _ledger: Any = None
    _calls: int = 0

    # ---- lifecycle -------------------------------------------------------

    async def start(self) -> Binding:
        self.socket_path.parent.mkdir(parents=True, exist_ok=True)
        if self.socket_path.exists():
            self.socket_path.unlink()
        self.ledger_path.parent.mkdir(parents=True, exist_ok=True)
        self._ledger = self.ledger_path.open("a")
        self._server = await asyncio.start_unix_server(self._handle, path=str(self.socket_path))
        os.chmod(self.socket_path, 0o600)
        home, path, temp_dir = await self._discover()
        return Binding(
            socket=str(self.socket_path),
            nonce=self.nonce,
            workdir=self.workdir,
            home=home,
            temp_dir=temp_dir,
            path=path,
        )

    async def close(self) -> None:
        if self._server is not None:
            self._server.close()
            await self._server.wait_closed()
            self._server = None
        if self.socket_path.exists():
            self.socket_path.unlink()
        if self._ledger is not None:
            self._ledger.close()
            self._ledger = None

    @property
    def calls(self) -> int:
        return self._calls

    async def _discover(self) -> tuple[str, str, str]:
        temp_dir = f"/tmp/stella-eval-{self.nonce[:8]}"
        r = await self._exec(
            f'mkdir -p {shlex.quote(temp_dir)} && printf "%s\\n%s\\n" "$HOME" "$PATH"',
            timeout_sec=30,
        )
        if r.return_code != 0:
            raise RuntimeError(f"bridge discover failed: {r.stderr}")
        lines = (r.stdout or "").splitlines()
        home = lines[0] if lines else ""
        path = lines[1] if len(lines) > 1 else ""
        if self.tool_path_prepend:
            path = f"{self.tool_path_prepend}:{path}" if path else self.tool_path_prepend
        return home, path, temp_dir

    # ---- connection handling --------------------------------------------

    async def _handle(self, reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
        started = time.monotonic()
        req: dict[str, Any] = {}
        try:
            raw = await reader.read(MAX_PAYLOAD + (1 << 20))
            req = json.loads(raw)
            if req.get("nonce") != self.nonce:
                resp = {"ok": False, "code": "bad_nonce", "error": "nonce mismatch"}
            else:
                resp = await self._dispatch(req)
        except BridgeError as e:
            resp = {"ok": False, "code": e.code, "error": str(e)}
        except Exception as e:  # noqa: BLE001 - surface any failure to the caller
            resp = {"ok": False, "code": "internal", "error": f"{type(e).__name__}: {e}"}
        try:
            writer.write(json.dumps(resp).encode())
            await writer.drain()
        finally:
            writer.close()
        self._record(req, resp, time.monotonic() - started)

    def _record(self, req: dict[str, Any], resp: dict[str, Any], elapsed: float) -> None:
        self._calls += 1
        if self._ledger is None:
            return
        entry = {
            "seq": self._calls,
            "ts": time.time(),
            "op": req.get("op"),
            "path": req.get("path"),
            # Command text is trimmed; secrets are the caller's problem to redact
            # (Stella already redacts its own transcript), but keep this short.
            "command": (req.get("command") or "")[:200],
            "ok": resp.get("ok"),
            "code": resp.get("code"),
            "return_code": resp.get("return_code"),
            "elapsed_ms": int(elapsed * 1000),
        }
        self._ledger.write(json.dumps(entry) + "\n")
        self._ledger.flush()

    async def _dispatch(self, req: dict[str, Any]) -> dict[str, Any]:
        op = req.get("op")
        handler = {
            "ping": self._op_ping,
            "exec": self._op_exec,
            "stat": self._op_stat,
            "read_dir": self._op_read_dir,
            "read_file": self._op_read_file,
            "write_file": self._op_write_file,
            "project": self._op_project,
        }.get(op)
        if handler is None:
            raise BridgeError("bad_op", f"unknown op {op!r}")
        return await handler(req)

    # ---- ops -------------------------------------------------------------

    async def _exec(self, command: str, *, cwd: str | None = None, env: dict[str, str] | None = None, timeout_sec: int | None = None):
        return await self.env.exec(command, cwd=cwd, env=env, timeout_sec=timeout_sec, user=self.user)

    async def _op_ping(self, req: dict[str, Any]) -> dict[str, Any]:
        return {"ok": True}

    async def _op_exec(self, req: dict[str, Any]) -> dict[str, Any]:
        timeout = req.get("timeout_sec") or None
        r = await self._exec(req["command"], cwd=req.get("cwd") or self.workdir, env=req.get("env") or None, timeout_sec=timeout)
        return {"ok": True, "stdout": r.stdout or "", "stderr": r.stderr or "", "return_code": r.return_code}

    async def _op_stat(self, req: dict[str, Any]) -> dict[str, Any]:
        p = shlex.quote(req["path"])
        r = await self._exec(
            f'if [ -d {p} ]; then echo d 0; elif [ -e {p} ]; then echo f "$(wc -c < {p})"; else exit 3; fi',
            timeout_sec=30,
        )
        if r.return_code == 3:
            raise BridgeError("not_found", f"{req['path']}: no such file or directory")
        if r.return_code != 0:
            raise BridgeError("internal", f"stat failed: {r.stderr}")
        kind, size = (r.stdout or "").split()
        return {"ok": True, "is_dir": kind == "d", "size": int(size)}

    async def _op_read_dir(self, req: dict[str, Any]) -> dict[str, Any]:
        p = shlex.quote(req["path"])
        # POSIX sh only: task images may lack GNU find/python. Names with tabs or
        # newlines break this; ceiling accepted for the spike.
        script = (
            f'cd {p} || exit 3; for f in .[!.]* ..?* *; do '
            '[ -e "$f" ] || [ -L "$f" ] || continue; '
            'if [ -d "$f" ]; then printf "d\\t0\\t%s\\n" "$f"; '
            'else printf "f\\t%s\\t%s\\n" "$(wc -c < "$f" 2>/dev/null || echo 0)" "$f"; fi; done'
        )
        r = await self._exec(script, timeout_sec=60)
        if r.return_code == 3:
            raise BridgeError("not_found", f"{req['path']}: no such directory")
        if r.return_code != 0:
            raise BridgeError("internal", f"read_dir failed: {r.stderr}")
        entries = []
        for line in (r.stdout or "").splitlines():
            parts = line.split("\t", 2)
            if len(parts) != 3:
                continue
            kind, size, name = parts
            entries.append({"name": name, "is_dir": kind == "d", "size": int(size or 0)})
        return {"ok": True, "entries": entries}

    async def _op_read_file(self, req: dict[str, Any]) -> dict[str, Any]:
        st = await self._op_stat(req)
        if st["is_dir"]:
            raise BridgeError("is_dir", f"{req['path']}: is a directory")
        if st["size"] > MAX_PAYLOAD:
            raise BridgeError("too_large", f"{req['path']}: {st['size']} bytes exceeds cap {MAX_PAYLOAD}")
        source = await self._resolve_symlink(req["path"])
        with tempfile.TemporaryDirectory(prefix="stella-bridge-") as td:
            local = Path(td) / "f"
            await self.env.download_file(source, local)
            data = local.read_bytes()
        return {"ok": True, "data": base64.b64encode(data).decode()}

    async def _resolve_symlink(self, path: str) -> str:
        """Return the real path a file lives at.

        `docker cp` copies a symlink as a symlink, so the copy dangles on the
        host and reading it fails. Linux task images are full of symlinked
        config (/etc/nginx/sites-enabled, /etc/alternatives), and the failure is
        silent: the agent just sees a broken read tool and works around it.
        Resolving first keeps read consistent with what bash sees.
        """
        p = shlex.quote(path)
        r = await self._exec(
            f'if command -v readlink >/dev/null 2>&1; then readlink -f -- {p} 2>/dev/null '
            f'|| printf "%s" {p}; else printf "%s" {p}; fi',
            timeout_sec=30,
        )
        resolved = (r.stdout or "").strip()
        return resolved if r.return_code == 0 and resolved.startswith("/") else path

    async def _op_write_file(self, req: dict[str, Any]) -> dict[str, Any]:
        path = req["path"]
        data = base64.b64decode(req.get("data") or "")
        mode = int(req.get("mode") or 0o644)
        parent = shlex.quote(os.path.dirname(path) or "/")
        tmp_remote = f"{path}.stella-tmp-{secrets.token_hex(4)}"
        with tempfile.TemporaryDirectory(prefix="stella-bridge-") as td:
            local = Path(td) / "f"
            local.write_bytes(data)
            r = await self._exec(f"mkdir -p {parent}", timeout_sec=30)
            if r.return_code != 0:
                raise BridgeError("internal", f"mkdir failed: {r.stderr}")
            await self.env.upload_file(local, tmp_remote)
        r = await self._exec(
            f"chmod {mode:o} {shlex.quote(tmp_remote)} && mv -f {shlex.quote(tmp_remote)} {shlex.quote(path)}",
            timeout_sec=30,
        )
        if r.return_code != 0:
            await self._exec(f"rm -f {shlex.quote(tmp_remote)}", timeout_sec=10)
            raise BridgeError("internal", f"write failed: {r.stderr}")
        return {"ok": True}

    async def _op_project(self, req: dict[str, Any]) -> dict[str, Any]:
        """Exact, no-replace projection: publish a tree at `path`.

        If the target already exists it must be byte-identical (diff -r), else
        code "conflict". Publication is stage-then-rename so a partially
        uploaded tree is never visible.
        """
        target = req["path"]
        files = req.get("files") or []
        stage = f"{target}.stella-stage-{secrets.token_hex(4)}"
        with tempfile.TemporaryDirectory(prefix="stella-bridge-") as td:
            root = Path(td)
            for f in files:
                rel = f["path"]
                if rel.startswith("/") or ".." in Path(rel).parts:
                    raise BridgeError("bad_path", f"invalid projection path {rel!r}")
                dest = root / rel
                dest.parent.mkdir(parents=True, exist_ok=True)
                dest.write_bytes(base64.b64decode(f.get("data") or ""))
                os.chmod(dest, int(f.get("mode") or 0o644))
            r = await self._exec(f"mkdir -p {shlex.quote(stage)}", timeout_sec=30)
            if r.return_code != 0:
                raise BridgeError("internal", f"stage mkdir failed: {r.stderr}")
            await self.env.upload_dir(root, stage)
        q_stage, q_target = shlex.quote(stage), shlex.quote(target)
        script = (
            f"if [ -e {q_target} ]; then "
            f"  if diff -r {q_stage} {q_target} >/dev/null 2>&1; then rm -rf {q_stage}; exit 0; "
            f"  else rm -rf {q_stage}; exit 75; fi; "
            f"else mkdir -p \"$(dirname {q_target})\" && mv {q_stage} {q_target}; fi"
        )
        r = await self._exec(script, timeout_sec=60)
        if r.return_code == 75:
            raise BridgeError("conflict", f"{target}: existing tree differs")
        if r.return_code != 0:
            raise BridgeError("internal", f"project failed: {r.stderr}")
        return {"ok": True}
