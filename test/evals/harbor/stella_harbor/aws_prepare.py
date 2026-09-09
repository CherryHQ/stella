"""Measure a bounded worker command and prepare Harbor environments without a model."""

from __future__ import annotations

import argparse
import asyncio
import datetime as dt
import hashlib
import json
import math
import os
import re
import signal
import subprocess
import time
from collections import Counter
from pathlib import Path

DATASET = "terminal-bench/terminal-bench-2-1"
DATASET_REF = "sha256:7d7bdc1cbedad549fc1140404bd4dc45e5fd0ea7c4186773687d177ad3a0699a"
QUEUE_TASKS = 89
QUEUE_ATTEMPTS = 5


def utc_now() -> str:
    return dt.datetime.now(dt.UTC).isoformat()


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
        "oom_kills": int(
            dict(
                line.split() for line in Path("/proc/vmstat").read_text().splitlines()
            ).get("oom_kill", 0)
        ),
    }


def measure(
    command: list[str],
    output: Path,
    concurrency: int,
    *,
    env: dict[str, str] | None = None,
    minimum_memory_bytes: int = 0,
    stop_on_oom: bool = False,
    max_seconds: float = 0,
    trial_root: Path | None = None,
) -> int:
    if not command or concurrency < 1 or max_seconds < 0:
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
    initial_oom = previous.get("oom_kills", 0)
    oom_kills = 0
    stop_reason = None
    sample_seconds = None
    sample_inventory = None
    job_progress = None
    maximum_running_trials = 0
    last_progress_sample = -5.0
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
            elapsed_seconds = time.monotonic() - started
            if trial_root is not None and elapsed_seconds - last_progress_sample >= 5:
                job_progress = trial_progress(trial_root) or job_progress
                maximum_running_trials = max(
                    maximum_running_trials,
                    (job_progress or {}).get("n_running_trials", 0),
                )
                last_progress_sample = elapsed_seconds
            if max_seconds and elapsed_seconds >= max_seconds:
                stop_reason = "sample_time_limit"
                sample_seconds = round(elapsed_seconds, 3)
                if trial_root is not None:
                    # Snapshot before cancellation: cleanup must not turn the
                    # interrupted trials into measured infrastructure failures.
                    try:
                        sample_inventory = trial_inventory(trial_root)
                    except (FileNotFoundError, json.JSONDecodeError):
                        # Keep the hard time limit if a result is mid-write.
                        # An unavailable snapshot cannot back a throughput rate.
                        sample_inventory = None
                    job_progress = trial_progress(trial_root) or job_progress
                break
            current = sample_resources()
            if current and previous:
                oom_kills = current.get("oom_kills", 0) - initial_oom
                if (stop_on_oom and oom_kills > 0) or current[
                    "available_memory_bytes"
                ] < minimum_memory_bytes:
                    stop_reason = (
                        "oom_kill"
                        if stop_on_oom and oom_kills > 0
                        else "available_memory_below_floor"
                    )
                    minimum_memory = min(
                        minimum_memory, current["available_memory_bytes"]
                    )
                    break
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
        if not stop_reason:
            if trial_root is not None:
                job_progress = trial_progress(trial_root) or job_progress
            final_sample = sample_resources()
            if final_sample:
                oom_kills = final_sample.get("oom_kills", 0) - initial_oom
                minimum_memory = min(
                    minimum_memory, final_sample["available_memory_bytes"]
                )
                if stop_on_oom and oom_kills > 0:
                    stop_reason = "oom_kill"
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
                    "oom_kills": oom_kills,
                    "stop_on_oom": stop_on_oom,
                    "stop_reason": stop_reason,
                    "sample_seconds": sample_seconds,
                    "sample_inventory": sample_inventory,
                    "job_progress": job_progress,
                    "maximum_running_trials": maximum_running_trials
                    if trial_root is not None
                    else None,
                },
                indent=2,
            )
            + "\n"
        )
    if stop_reason == "sample_time_limit":
        return 124
    return 125 if stop_reason else child.returncode


def trial_progress(root: Path) -> dict[str, int] | None:
    for path in sorted(root.glob("*/*/result.json"), reverse=True):
        try:
            stats = json.loads(path.read_text()).get("stats")
        except (FileNotFoundError, json.JSONDecodeError):
            continue  # Harbor may be replacing a live summary during a sample.
        if stats:
            return {
                key: stats.get(key, 0)
                for key in (
                    "n_completed_trials",
                    "n_errored_trials",
                    "n_running_trials",
                    "n_pending_trials",
                    "n_cancelled_trials",
                )
            }
    return None


def trial_inventory(root: Path) -> dict:
    if __package__:
        from .aws_merge import inventory
    else:
        from aws_merge import inventory
    state = inventory(root, 1)
    state.pop("selected")
    return state


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


def queue_config(
    task_names: list[str], trial_limit: int, concurrency: int
) -> tuple[str, dict]:
    """Declare attempt counts before execution, never using rewards or durations."""
    if (
        len(task_names) != QUEUE_TASKS
        or len(set(task_names)) != QUEUE_TASKS
        or any(not re.fullmatch(r"[a-z0-9][a-z0-9.-]*", task) for task in task_names)
    ):
        raise ValueError(
            "queued evaluation requires exactly 89 distinct task identifiers"
        )
    if (
        not QUEUE_TASKS <= trial_limit <= QUEUE_TASKS * QUEUE_ATTEMPTS
        or concurrency < 1
    ):
        raise ValueError(
            "queued evaluation requires 89-445 trials and positive concurrency"
        )
    names = sorted(task_names)
    rounds, extra = divmod(trial_limit, QUEUE_TASKS)
    # Hash ordering avoids taking an alphabetical prefix and is independent of
    # runtime/reward. Every task is represented before extra attempts are added.
    extra_names = sorted(
        names, key=lambda name: hashlib.sha256(name.encode()).digest()
    )[:extra]
    counts = {name: rounds + (name in extra_names) for name in names}
    groups = [names] if not extra else [names] * rounds + [sorted(extra_names)]
    attempts = rounds if not extra else 1
    lines = [
        f"n_attempts: {attempts}",
        f"n_concurrent_trials: {concurrency}",
        "retry:",
        "  max_retries: 0",
        "datasets:",
    ]
    for group in groups:
        lines.extend(
            [
                f"  - name: {DATASET}",
                f"    ref: {DATASET_REF}",
                "    task_names:",
                *[f"      - terminal-bench/{name}" for name in group],
            ]
        )
    config = "\n".join(lines) + "\n"
    plan = {
        "dataset": f"{DATASET}@{DATASET_REF}",
        "full_plan_trials": QUEUE_TASKS * QUEUE_ATTEMPTS,
        "trial_limit": trial_limit,
        "distinct_tasks": QUEUE_TASKS,
        "concurrency": concurrency,
        "selection": "all-tasks-then-sha256-name-prefix-v1",
        "harbor_n_attempts": attempts,
        "task_attempts": counts,
        "config_sha256": hashlib.sha256(config.encode()).hexdigest(),
        "max_retries": 0,
        "performance_only": True,
    }
    return config, plan


def write_queue(
    job: Path, output: Path, config_path: Path, trial_limit: int, concurrency: int
) -> None:
    # The native planner resolves duplicate dataset entries concurrently.
    # Populate its task cache with the completed install-only job first.
    preparation_inventory(job, QUEUE_TASKS)
    if output.exists() or config_path.exists():
        raise ValueError("refusing to overwrite a queue plan")
    names = []
    for result_path in job.rglob("result.json"):
        trial_config = result_path.with_name("config.json")
        if trial_config.is_file() and json.loads(trial_config.read_text()).get(
            "trial_name"
        ):
            name = json.loads(result_path.read_text())["task_name"]
            if not name.startswith("terminal-bench/"):
                raise ValueError(
                    "environment task lacks Terminal-Bench package identity"
                )
            names.append(name.removeprefix("terminal-bench/"))
    config, plan = queue_config(names, trial_limit, concurrency)

    # JobPlan resolves the exact runnable count without starting containers,
    # provisioning Stella or invoking a model. print-config alone cannot do this.
    from importlib.metadata import version

    import yaml
    from harbor import JobConfig, JobPlan

    resolved = asyncio.run(
        JobPlan.from_config(
            JobConfig.model_validate(
                yaml.safe_load(config) | {"agents": [{"name": "nop"}]}
            )
        )
    )
    # Package tasks have a qualified name and no local path in TrialConfig.
    # Their cache directory basename is a digest, not a task identity.
    qualified_names = [trial.task.name for trial in resolved.trial_configs]
    if any(
        not name or not name.startswith("terminal-bench/") for name in qualified_names
    ):
        raise ValueError("Harbor queue resolved a non-Terminal-Bench task identity")
    resolved_names = [name.removeprefix("terminal-bench/") for name in qualified_names]
    counts = Counter(resolved_names)
    if dict(counts) != plan["task_attempts"]:
        raise ValueError("Harbor queue resolution differs from declared task attempts")
    plan["harbor_version"] = version("harbor")
    plan["resolved_trials"] = len(resolved.trial_configs)
    plan["trial_order"] = resolved_names
    with config_path.open("x") as stream:
        stream.write(config)
    with output.open("x") as stream:
        stream.write(json.dumps(plan, indent=2) + "\n")


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
            "p95_seconds": round(
                sorted(durations)[math.ceil(len(durations) * 0.95) - 1], 3
            )
            if durations
            else None,
        }
        for name, durations in phases.items()
    }
    output.write_text(json.dumps(summary, indent=2) + "\n")


def capacity_metrics(job: Path, output: Path, queue_plan: Path | None = None) -> None:
    # Use the same validity and timeout definitions as normal eval evidence.
    if __package__:
        from .aws_merge import inventory, ordered_trials, scoreability_reason
    else:
        from aws_merge import inventory, ordered_trials, scoreability_reason
    from stella_harbor.compare import _timeout_class

    add_phase_metrics(job, output)
    summary = json.loads(output.read_text())
    state = inventory(job, 1)
    state.pop("selected")
    rows = []
    events = []
    for path in ordered_trials(job):
        result = json.loads((path / "result.json").read_text())
        adapter_path = path / "agent/stella/result.json"
        adapter = json.loads(adapter_path.read_text()) if adapter_path.is_file() else {}
        reason = scoreability_reason(path)
        started, finished = result.get("started_at"), result.get("finished_at")
        if started:
            events.append((dt.datetime.fromisoformat(started), 1))
            events.append(
                (dt.datetime.fromisoformat(finished or summary["completed_at"]), -1)
            )
        rows.append(
            {
                "task": path.name.split("__", 1)[0],
                "scoreability_reason": reason,
                "reward": (
                    (result.get("verifier_result") or {}).get("rewards") or {}
                ).get("reward")
                if reason is None
                else None,
                "timeout_class": _timeout_class(result, adapter),
                "wall_seconds": (
                    dt.datetime.fromisoformat(finished)
                    - dt.datetime.fromisoformat(started)
                ).total_seconds()
                if started and finished
                else None,
            }
        )
    active = peak = 0
    for _, change in sorted(events):
        active += change
        peak = max(peak, active)
    reasons = []
    timeboxed = summary.get("stop_reason") == "sample_time_limit"
    if summary.get("stop_reason") and not timeboxed:
        reasons.append(summary["stop_reason"])
    if summary["exit_code"] != 0 and not timeboxed:
        reasons.append("command_failed")
    if queue_plan is not None:
        planned = json.loads(queue_plan.read_text())["task_attempts"]
        observed_counts = Counter(row["task"] for row in rows)
        if dict(observed_counts) != planned:
            reasons.append("queue_attempt_counts_mismatch")
        if timeboxed:
            reasons.append("queued_run_must_not_be_timeboxed")
        summary["planned_trials"] = sum(planned.values())
        summary["observed_task_attempts"] = dict(sorted(observed_counts.items()))
    elif not timeboxed and (state["tasks"] != 89 or state["trials"] != 89):
        reasons.append("incomplete_or_repeated_task_set")
    observed = summary.get("sample_inventory") if timeboxed else state
    if observed is None:
        reasons.append("sample_inventory_unavailable")
    elif observed["invalid"] >= 5:
        reasons.append("at_least_five_unscoreable_trials")
    summary.update(
        {
            "inventory": state,
            "trials": rows,
            "resolved": sum(row["reward"] == 1 for row in rows),
            "scoreable_per_hour": observed["scoreable"]
            * 3600
            / (summary.get("sample_seconds") or summary["wall_seconds"])
            if observed is not None
            else None,
            "incomplete_sample": timeboxed,
            "observed_peak_trial_overlap": peak,
            "capacity_stop_reasons": reasons,
        }
    )
    output.write_text(json.dumps(summary, indent=2) + "\n")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="action", required=True)
    phases = commands.add_parser("phases")
    phases.add_argument("--job", type=Path, required=True)
    phases.add_argument("--output", type=Path, required=True)
    capacity = commands.add_parser("capacity")
    capacity.add_argument("--job", type=Path, required=True)
    capacity.add_argument("--output", type=Path, required=True)
    capacity.add_argument("--queue-plan", type=Path)
    queue = commands.add_parser("queue")
    queue.add_argument("--job", type=Path, required=True)
    queue.add_argument("--output", type=Path, required=True)
    queue.add_argument("--config", type=Path, required=True)
    queue.add_argument("--trial-limit", type=int, required=True)
    queue.add_argument("--concurrency", type=int, required=True)
    for name in ("measure", "prepare"):
        command = commands.add_parser(name)
        command.add_argument("--output", type=Path, required=True)
        command.add_argument("--concurrency", type=int, required=True)
        if name == "measure":
            command.add_argument("--minimum-memory-gib", type=int, default=0)
            command.add_argument(
                "--stop-on-oom",
                action="store_true",
                help="legacy capacity guard; counter does not attribute OOM origin",
            )
            command.add_argument("--max-seconds", type=int, default=0)
            command.add_argument("--trial-root", type=Path)
            command.add_argument("command", nargs=argparse.REMAINDER)
        else:
            command.add_argument("--taskset", type=Path)
            command.add_argument("--expected-tasks", type=int, required=True)
    args = parser.parse_args(argv)
    if args.action == "queue":
        write_queue(
            args.job, args.output, args.config, args.trial_limit, args.concurrency
        )
        return 0
    if args.action == "capacity":
        capacity_metrics(args.job, args.output, args.queue_plan)
        return 0
    if args.action == "phases":
        add_phase_metrics(args.job, args.output)
        return 0
    if args.action == "measure":
        command = args.command[1:] if args.command[:1] == ["--"] else args.command
        if args.minimum_memory_gib < 0:
            parser.error("memory floor must not be negative")
        return measure(
            command,
            args.output,
            args.concurrency,
            minimum_memory_bytes=args.minimum_memory_gib * 1024**3,
            stop_on_oom=args.stop_on_oom,
            max_seconds=args.max_seconds,
            trial_root=args.trial_root,
        )
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
