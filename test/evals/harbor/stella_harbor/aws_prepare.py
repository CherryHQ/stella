"""Measure a bounded worker command and prepare Harbor environments without a model."""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import signal
import subprocess
import time
from pathlib import Path

DATASET = "terminal-bench/terminal-bench-2-1"
DATASET_REF = "sha256:7d7bdc1cbedad549fc1140404bd4dc45e5fd0ea7c4186773687d177ad3a0699a"


def utc_now() -> str:
    return dt.datetime.now(dt.timezone.utc).isoformat()


def sample_resources() -> dict[str, float | int]:
    """Read host counters only; never process arguments or task output."""
    if not Path("/proc/meminfo").exists():
        return {}
    memory = dict(
        line.split(":", 1) for line in Path("/proc/meminfo").read_text().splitlines()
    )
    cpu = [
        int(value)
        for value in Path("/proc/stat").read_text().splitlines()[0].split()[1:9]
    ]
    return {
        "available_memory_bytes": int(memory["MemAvailable"].split()[0]) * 1024,
        "cpu_ticks": sum(cpu),
        "idle_ticks": cpu[3] + cpu[4],
        "load_1m": float(Path("/proc/loadavg").read_text().split()[0]),
    }


def measure(
    command: list[str],
    output: Path,
    concurrency: int,
    *,
    env: dict[str, str] | None = None,
) -> int:
    if not command or concurrency < 1:
        raise ValueError("a command and positive concurrency are required")
    output.parent.mkdir(parents=True, exist_ok=True)
    if output.exists():
        raise ValueError("refusing to overwrite a measurement")
    started_at = utc_now()
    started = time.monotonic()
    previous = sample_resources()
    minimum_memory = previous.get("available_memory_bytes")
    maximum_load = previous.get("load_1m")
    maximum_cpu = None
    child = subprocess.Popen(command, env=env, start_new_session=True)

    def interrupt(signum: int, _frame: object) -> None:
        if child.poll() is None:
            os.killpg(child.pid, signum)
        raise KeyboardInterrupt

    handlers = {
        sig: signal.signal(sig, interrupt) for sig in (signal.SIGTERM, signal.SIGINT)
    }
    try:
        while child.poll() is None:
            current = sample_resources()
            if current and previous:
                minimum_memory = min(minimum_memory, current["available_memory_bytes"])
                maximum_load = max(maximum_load, current["load_1m"])
                elapsed = current["cpu_ticks"] - previous["cpu_ticks"]
                if elapsed > 0:
                    busy = 100 * (
                        1 - (current["idle_ticks"] - previous["idle_ticks"]) / elapsed
                    )
                    maximum_cpu = max(maximum_cpu or 0, busy)
            previous = current
            try:
                child.wait(timeout=1)
            except subprocess.TimeoutExpired:
                pass
    finally:
        if child.poll() is None:
            os.killpg(child.pid, signal.SIGTERM)
            try:
                child.wait(timeout=10)
            except subprocess.TimeoutExpired:
                os.killpg(child.pid, signal.SIGKILL)
                child.wait()
        for sig, handler in handlers.items():
            signal.signal(sig, handler)
        # Commands and environment values can contain credentials. Record only
        # the declared concurrency, timestamps, exit status and host counters.
        output.write_text(
            json.dumps(
                {
                    "started_at": started_at,
                    "completed_at": utc_now(),
                    "wall_seconds": round(time.monotonic() - started, 3),
                    "concurrency": concurrency,
                    "exit_code": child.returncode,
                    "minimum_available_memory_bytes": minimum_memory,
                    "maximum_load_1m": maximum_load,
                    "maximum_cpu_busy_percent": maximum_cpu,
                },
                indent=2,
            )
            + "\n"
        )
    return child.returncode


def prepare_command(job: Path, concurrency: int, taskset: Path | None) -> list[str]:
    if not 1 <= concurrency <= 4:
        raise ValueError("environment preparation concurrency must be between 1 and 4")
    source = ["-c", str(taskset)] if taskset else ["-d", f"{DATASET}@{DATASET_REF}"]
    return [
        "harbor",
        "run",
        *source,
        "-a",
        "nop",
        "--install-only",
        "--no-delete",
        "-k",
        "1",
        "-n",
        str(concurrency),
        "-q",
        "-o",
        str(job),
    ]


def preparation_inventory(job: Path, expected_tasks: int) -> dict[str, int]:
    trials = []
    summaries = []
    for result_path in job.rglob("result.json"):
        config_path = result_path.with_name("config.json")
        if not config_path.is_file():
            continue
        config = json.loads(config_path.read_text())
        if config.get("trial_name"):
            trials.append(json.loads(result_path.read_text()))
        else:
            summaries.append(json.loads(result_path.read_text()))
    failed = sum(bool(trial.get("exception_info")) for trial in trials)
    if len(trials) != expected_tasks or failed:
        raise ValueError(
            f"environment preparation incomplete: trials={len(trials)} expected={expected_tasks} failed={failed}"
        )
    if len(summaries) != 1:
        raise ValueError("environment preparation must produce exactly one job summary")
    summary = summaries[0]
    stats = summary.get("stats") or {}
    if (
        not summary.get("finished_at")
        or summary.get("n_total_trials") != expected_tasks
        or stats.get("n_completed_trials") != expected_tasks
        or any(
            stats.get(field, 0) != 0
            for field in (
                "n_errored_trials",
                "n_cancelled_trials",
                "n_pending_trials",
                "n_running_trials",
            )
        )
    ):
        raise ValueError(
            "environment job did not finish all planned trials successfully"
        )
    names = set()
    for trial in trials:
        names.add(trial.get("task_name"))
        if (
            not trial.get("started_at")
            or not trial.get("finished_at")
            or (trial.get("agent_info") or {}).get("name") != "nop"
            or trial.get("verifier_result") is not None
            or any(
                not (trial.get(phase) or {}).get(endpoint)
                for phase in ("environment_setup", "agent_setup")
                for endpoint in ("started_at", "finished_at")
            )
        ):
            raise ValueError("environment trial lacks completed install-only evidence")
    if None in names or len(names) != expected_tasks:
        raise ValueError("environment preparation repeated or omitted task identities")
    return {"environments": len(trials), "failed": failed, "model_calls": 0}


def add_phase_metrics(job: Path, output: Path) -> None:
    """Keep environment startup separate from the model's variable runtime."""
    phases: dict[str, list[float]] = {
        phase: [] for phase in ("environment_setup", "agent_setup", "agent_execution")
    }
    for result_path in job.rglob("result.json"):
        config_path = result_path.with_name("config.json")
        if not config_path.is_file() or not json.loads(config_path.read_text()).get(
            "trial_name"
        ):
            continue
        trial = json.loads(result_path.read_text())
        for name, durations in phases.items():
            phase = trial.get(name) or {}
            if phase.get("started_at") and phase.get("finished_at"):
                durations.append(
                    (
                        dt.datetime.fromisoformat(phase["finished_at"])
                        - dt.datetime.fromisoformat(phase["started_at"])
                    ).total_seconds()
                )
    summary = json.loads(output.read_text())
    summary["trial_phases"] = {
        name: {
            "measured_trials": len(durations),
            "total_seconds": round(sum(durations), 3),
        }
        for name, durations in phases.items()
    }
    output.write_text(json.dumps(summary, indent=2) + "\n")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="action", required=True)
    phases = commands.add_parser("phases")
    phases.add_argument("--job", type=Path, required=True)
    phases.add_argument("--output", type=Path, required=True)
    for name in ("measure", "prepare"):
        command = commands.add_parser(name)
        command.add_argument("--output", type=Path, required=True)
        command.add_argument("--concurrency", type=int, required=True)
        if name == "measure":
            command.add_argument("command", nargs=argparse.REMAINDER)
        else:
            command.add_argument("--taskset", type=Path)
            command.add_argument("--expected-tasks", type=int, required=True)
    args = parser.parse_args(argv)
    if args.action == "phases":
        add_phase_metrics(args.job, args.output)
        return 0
    if args.action == "measure":
        command = args.command[1:] if args.command[:1] == ["--"] else args.command
        return measure(command, args.output, args.concurrency)
    job = args.output.parent.parent / "environment-jobs" / "warmup"
    job.parent.mkdir(parents=True, exist_ok=True)
    if job.exists():
        parser.error("environment job already exists")
    # Nop never uses a provider. Remove the deployment credentials as an
    # additional boundary, even though Harbor does not inject host env by default.
    env = {
        key: value
        for key, value in os.environ.items()
        if not key.startswith(("OPENAI_", "EVAL_COST_", "STELLA_EVAL_"))
    }
    status = measure(
        prepare_command(job, args.concurrency, args.taskset),
        args.output,
        args.concurrency,
        env=env,
    )
    if status:
        return status
    state = preparation_inventory(job, args.expected_tasks)
    summary = json.loads(args.output.read_text()) | state
    args.output.write_text(json.dumps(summary, indent=2) + "\n")
    add_phase_metrics(job, args.output)
    print(json.dumps(state, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
