"""Run a pinned external harness through the same Harbor dataset and scheduler."""
from __future__ import annotations

import argparse
import os
from pathlib import Path
import subprocess
import sys


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--agent", choices=("pi", "hermes"), required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--thinking", default="")
    parser.add_argument("--context-window", type=int, default=0)
    parser.add_argument("--max-tokens", type=int, default=0)
    parser.add_argument("--output", type=Path, required=True)
    args, harbor_args = parser.parse_known_args()
    if harbor_args[:1] == ["--"]:
        harbor_args = harbor_args[1:]
    adapter = "PiGateway" if args.agent == "pi" else "HermesGateway"
    command = ["harbor", "run", *harbor_args,
               "-a", f"stella_harbor.{args.agent}_gateway:{adapter}",
               "-m", "gateway/" + os.environ["OPENAI_MODEL"],
               "--max-retries", "0", "-o", str(args.output),
               "--ak", f"version={args.version}"]
    if args.thinking:
        thinking = "off" if args.agent == "pi" and args.thinking == "none" else args.thinking
        command += ["--ak", f"thinking={thinking}"]
    for name in ("context_window", "max_tokens"):
        value = getattr(args, name)
        if value:
            command += ["--ak", f"{name}={value}"]
    if args.agent == "pi":
        for field in ("input", "output", "cache_read", "cache_write"):
            command += ["--ak", f"cost_{field}={os.environ['EVAL_COST_' + field.upper()]}"]
    result = subprocess.run(command, check=False)
    print(f"  job: {args.output.resolve()}", flush=True)
    return result.returncode


if __name__ == "__main__":
    sys.exit(main())
