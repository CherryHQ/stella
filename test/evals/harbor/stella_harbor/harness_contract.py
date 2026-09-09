"""Verify native request controls, optionally with a live non-benchmark tool round trip."""
from __future__ import annotations

import argparse
import asyncio
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import socket
import secrets
import sys
import threading
import time
import uuid
import urllib.error
import urllib.request

from harbor.environments.docker.docker import DockerEnvironment
from harbor.models.agent.context import AgentContext
from harbor.models.task.config import EnvironmentConfig
from harbor.models.trial.paths import TrialPaths

from stella_harbor.hermes_gateway import HermesGateway
from stella_harbor.pi_gateway import PiGateway
from stella_harbor.archive import _replace_known
from stella_harbor.install import SETUP_TIMEOUT_SEC


def response_events(model: str) -> list[dict]:
    item = {"id": "msg_contract", "type": "message", "role": "assistant", "status": "completed",
            "content": [{"type": "output_text", "text": "OK", "annotations": []}]}
    response = {"id": "resp_contract", "object": "response", "created_at": int(time.time()),
                "status": "completed", "model": model, "output": [item],
                "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2,
                          "input_tokens_details": {"cached_tokens": 0}, "output_tokens_details": {"reasoning_tokens": 0}}}
    events = [
        {"type": "response.created", "response": response | {"status": "in_progress", "output": []}},
        {"type": "response.output_item.added", "output_index": 0, "item": item | {"status": "in_progress", "content": []}},
        {"type": "response.content_part.added", "item_id": item["id"], "output_index": 0, "content_index": 0, "part": {"type": "output_text", "text": "", "annotations": []}},
        {"type": "response.output_text.delta", "item_id": item["id"], "output_index": 0, "content_index": 0, "delta": "OK"},
        {"type": "response.output_text.done", "item_id": item["id"], "output_index": 0, "content_index": 0, "text": "OK"},
        {"type": "response.output_item.done", "output_index": 0, "item": item},
        {"type": "response.completed", "response": response},
    ]
    return [event | {"sequence_number": n} for n, event in enumerate(events)]


async def verify(args: argparse.Namespace) -> None:
    expected = {"model": args.model, "max_output_tokens": args.max_tokens, "reasoning": {"effort": args.thinking}}
    requests: list[dict] = []
    returned_models: set[str] = set()
    gateway_errors: list[str] = []
    upstream_url = os.environ.get("OPENAI_BASE_URL", "").rstrip("/")
    upstream_key = os.environ.get("OPENAI_API_KEY", "")
    if args.live and (not upstream_url or not upstream_key):
        raise ValueError("live contract requires gateway credentials")
    contract_key = secrets.token_urlsafe(32)
    archive = os.environ.get("HERMES_RELEASE_ARCHIVE") if args.agent == "hermes" else None
    archive_sha256 = hashlib.sha256(Path(archive).read_bytes()).hexdigest() if archive else None

    def matches(request: dict) -> bool:
        return (request.get("model") == args.model
                and request.get("max_output_tokens") == args.max_tokens
                and (request.get("reasoning") or {}).get("effort") == args.thinking
                and request.get("path") == "/v1/responses")

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_args):
            pass

        def do_POST(self):
            if self.headers.get("Authorization") != "Bearer " + contract_key:
                self.send_error(401)
                return
            if len(requests) >= 12:
                self.send_error(429, "contract request budget exhausted")
                return
            body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))))
            observed = {key: body.get(key) for key in expected} | {"path": self.path}
            observed["shell_roundtrip"] = any(
                isinstance(item, dict) and item.get("type") == "function_call_output"
                and "contract-ok" in str(item.get("output", ""))
                for item in body.get("input", []) if isinstance(body.get("input"), list)
            )
            requests.append(observed)
            if not matches(observed):
                self.send_response(400)
                self.end_headers()
                self.wfile.write(b'{"error":{"message":"harness request differs from declared model controls"}}')
                return
            if args.live:
                request = urllib.request.Request(upstream_url + "/responses", data=json.dumps(body).encode(),
                    headers={"Authorization": "Bearer " + upstream_key, "Content-Type": "application/json"})
                try:
                    with urllib.request.urlopen(request, timeout=90) as response:
                        self.send_response(response.status)
                        self.send_header("Content-Type", response.headers.get("Content-Type", "text/event-stream"))
                        self.end_headers()
                        for line in response:
                            if line.startswith(b"data: "):
                                try:
                                    event = json.loads(line[6:])
                                    if model := (event.get("response") or {}).get("model"):
                                        returned_models.add(model)
                                    if event.get("type") in {"response.failed", "response.incomplete", "error"}:
                                        gateway_errors.append(event["type"])
                                except (ValueError, AttributeError):
                                    pass
                            self.wfile.write(line)
                            self.wfile.flush()
                except urllib.error.HTTPError as exc:
                    gateway_errors.append(f"HTTP {exc.code}")
                    self.send_response(exc.code)
                    self.end_headers()
                    self.wfile.write(exc.read())
                return
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            for event in response_events(args.model):
                self.wfile.write(f"event: {event['type']}\ndata: {json.dumps(event)}\n\n".encode())
            self.wfile.flush()

    server = ThreadingHTTPServer(("0.0.0.0", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    if sys.platform == "darwin":
        host = "host.docker.internal"
    else:
        with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as connection:
            connection.connect(("8.8.8.8", 80))
            host = connection.getsockname()[0]
    # This is a separate process. Real credentials never enter this container.
    os.environ["OPENAI_BASE_URL"] = f"http://{host}:{server.server_port}/v1"
    os.environ["OPENAI_API_KEY"] = contract_key
    for field in ("INPUT", "OUTPUT", "CACHE_READ", "CACHE_WRITE"):
        os.environ["EVAL_COST_" + field] = "0"
    paths = TrialPaths(trial_dir=args.output / "native-contract")
    paths.mkdir()
    environment_dir = args.output / "environment"
    environment_dir.mkdir(parents=True, exist_ok=True)
    env = DockerEnvironment(environment_dir=environment_dir, environment_name="harness-contract",
                            session_id="contract-" + uuid.uuid4().hex[:12], trial_paths=paths,
                            task_env_config=EnvironmentConfig(docker_image=args.image, cpus=2, memory_mb=4096))
    adapter = PiGateway if args.agent == "pi" else HermesGateway
    agent = adapter(logs_dir=paths.agent_dir, model_name="gateway/" + args.model, version=args.version,
                    thinking=args.thinking, max_tokens=args.max_tokens, context_window=args.context_window)
    error: str | None = None
    setup_elapsed: float | None = None
    try:
        await asyncio.wait_for(env.start(force_build=False), timeout=180)
        await env.exec("mkdir -p /logs/agent /logs/verifier")
        started = time.monotonic()
        try:
            await asyncio.wait_for(agent.setup(env), timeout=SETUP_TIMEOUT_SEC)
        finally:
            setup_elapsed = round(time.monotonic() - started, 3)
        instruction = "Use a shell tool to print contract-ok, then reply with only OK." if args.live else "Reply with only OK. Do not call tools."
        if args.agent == "pi":
            instruction = "- " + instruction
        await asyncio.wait_for(agent.run(instruction, env, AgentContext()), timeout=120)
        if not requests or not all(matches(request) for request in requests):
            raise ValueError("native harness did not send the declared model controls")
        if args.live and (gateway_errors or not returned_models or not any(request["shell_roundtrip"] for request in requests)):
            raise ValueError("live gateway did not complete a verified shell tool round trip")
    except BaseException as exc:
        error = f"{type(exc).__name__}: {exc}"
        error = _replace_known(error, upstream_key)[0].replace(contract_key, "[redacted_secret]")
        raise
    finally:
        server.shutdown()
        server.server_close()
        await env.stop(delete=True)
        (args.output / "contract.json").write_text(json.dumps({"agent": args.agent, "version": args.version,
            "release_archive_sha256": archive_sha256,
            "image": args.image, "setup_timeout_sec": SETUP_TIMEOUT_SEC,
            "setup_elapsed_sec": setup_elapsed,
            "install_stages": json.loads((paths.agent_dir / "install-stages.json").read_text())
                if (paths.agent_dir / "install-stages.json").exists() else [],
            "context_window": args.context_window, "expected": expected, "requests": requests,
            "live_gateway": args.live, "returned_models": sorted(returned_models), "gateway_errors": gateway_errors,
            "passed": error is None, "error": error}, indent=2) + "\n")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--agent", choices=("pi", "hermes"), required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--model", required=True)
    parser.add_argument("--thinking", required=True)
    parser.add_argument("--context-window", type=int, required=True)
    parser.add_argument("--max-tokens", type=int, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--live", action="store_true", help="validate a short non-benchmark tool round trip through the real gateway")
    parser.add_argument("--image", default="debian:13", help="non-benchmark installation image")
    parser.add_argument("--concurrency", type=int, default=1, help="independent simultaneous native installations")
    args = parser.parse_args()
    if args.concurrency < 1:
        parser.error("concurrency must be positive")
    args.output = args.output.resolve()
    if args.concurrency == 1:
        asyncio.run(verify(args))
    else:
        asyncio.run(verify_concurrent(args))
    print("native harness request contract: PASS")


async def verify_concurrent(args: argparse.Namespace) -> None:
    # Each child owns its gateway proxy and environment; credentials must not
    # race through os.environ between concurrent verifications.
    async def child(index: int) -> dict:
        output = args.output / f"worker-{index}"
        output.mkdir(parents=True, exist_ok=True)
        command = [sys.executable, "-m", "stella_harbor.harness_contract"]
        for flag in ("agent", "version", "model", "thinking", "context_window", "max_tokens", "image"):
            command += ["--" + flag.replace("_", "-"), str(getattr(args, flag))]
        command += ["--output", str(output)]
        if args.live:
            command.append("--live")
        with (output / "contract.log").open("wb") as log:
            process = await asyncio.create_subprocess_exec(*command, stdout=log, stderr=log)
            status = await process.wait()
        contract = output / "contract.json"
        evidence = json.loads(contract.read_text()) if contract.exists() else None
        return {"worker": index, "passed": status == 0 and bool(evidence and evidence.get("passed")), "exit_code": status,
                "contract": evidence}

    results = await asyncio.gather(*(child(index) for index in range(args.concurrency)))
    passed = all(result["passed"] for result in results)
    (args.output / "contract.json").write_text(json.dumps({
        "agent": args.agent, "concurrency": args.concurrency, "image": args.image,
        "setup_timeout_sec": SETUP_TIMEOUT_SEC, "passed": passed, "workers": results,
    }, indent=2) + "\n")
    if not passed:
        raise RuntimeError("concurrent native installation contract failed; see worker logs")


if __name__ == "__main__":
    main()
