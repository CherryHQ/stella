"""Select the first five valid trials per task from ordered Harbor jobs.

Selection deliberately ignores whether a scoreable trial passed. It reads reward
only to distinguish scoreable evidence from a trial with no verifier result.
This preserves the predeclared k=5 sample instead of cherry-picking outcomes.
"""

from __future__ import annotations

import argparse
import json
import shutil
from collections import defaultdict
from pathlib import Path
from typing import Any

EXPECTED_TASKS = 89


def _json(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text())
    if not isinstance(value, dict):
        raise TypeError(f"expected JSON object: {path}")
    return value


def task_name(trial: Path) -> str:
    return trial.name.split("__", 1)[0]


def scoreable(trial: Path) -> bool:
    result = _json(trial / "result.json")
    adapter_path = trial / "agent" / "stella" / "result.json"
    if not adapter_path.is_file():
        return False
    adapter = _json(adapter_path)
    rewards = (result.get("verifier_result") or {}).get("rewards") or {}
    bridge = (adapter.get("metrics") or {}).get("bridge") or {}
    return (
        adapter.get("valid") is True
        and bool(adapter.get("bridge_nonce"))
        and not adapter.get("predicate_violations")
        and not bridge.get("adapter_faults")
        and rewards.get("reward") is not None
    )


def ordered_trials(source: Path) -> list[Path]:
    """Find Harbor trial roots in lexical pass order, excluding agent results."""
    return [
        path.parent
        for path in sorted(source.rglob("result.json"))
        if (path.parent / "config.json").is_file()
    ]


def inventory(source: Path, k: int) -> dict[str, Any]:
    trials = ordered_trials(source)
    observed: dict[str, int] = defaultdict(int)
    valid: dict[str, list[Path]] = defaultdict(list)
    invalid: dict[str, int] = defaultdict(int)
    for trial in trials:
        task = task_name(trial)
        observed[task] += 1
        if scoreable(trial):
            valid[task].append(trial)
        else:
            invalid[task] += 1

    tasks = sorted(observed)
    missing = {task: max(0, k - len(valid[task])) for task in tasks if len(valid[task]) < k}
    return {
        "tasks": len(tasks),
        "trials": len(trials),
        "scoreable": sum(len(items) for items in valid.values()),
        "invalid": sum(invalid.values()),
        "missing": missing,
        "selected": {task: items[:k] for task, items in valid.items()},
    }


def merge(
    source: Path, output: Path, k: int, expected_tasks: int = EXPECTED_TASKS, concurrency: int = 16
) -> dict[str, Any]:
    state = inventory(source, k)
    if state["tasks"] != expected_tasks:
        raise ValueError(f"expected {expected_tasks} tasks, found {state['tasks']}")
    if state["missing"]:
        raise ValueError(f"tasks lack {k} scoreable trials: {json.dumps(state['missing'], sort_keys=True)}")
    if output.exists():
        shutil.rmtree(output)
    output.mkdir(parents=True)

    copied = 0
    selections: list[dict[str, Any]] = []
    for task in sorted(state["selected"]):
        for attempt, trial in enumerate(state["selected"][task], 1):
            group = output / f"attempt-{attempt}"
            group.mkdir(exist_ok=True)
            target = group / trial.name
            if target.exists():
                target = group / f"{trial.name}-{copied:04d}"
            shutil.copytree(trial, target)
            selections.append({"task": task, "attempt": attempt, "source": str(trial.relative_to(source))})
            copied += 1
    if copied != expected_tasks * k:
        raise ValueError(f"expected {expected_tasks * k} selected trials, copied {copied}")
    (output / "config.json").write_text(
        json.dumps(
            {
                "datasets": [
                    {
                        "name": "terminal-bench/terminal-bench-2-1",
                        "ref": "sha256:7d7bdc1cbedad549fc1140404bd4dc45e5fd0ea7c4186773687d177ad3a0699a",
                    }
                ],
                "n_attempts": k,
                "n_concurrent_trials": concurrency,
                "selected_trial_count": copied,
            },
            indent=2,
        )
        + "\n"
    )
    return {key: value for key, value in state.items() if key != "selected"} | {
        "copied": copied,
        "selections": selections,
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", type=Path, help="directory of ordered pass/retry job groups")
    parser.add_argument("--k", type=int, default=5)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--expected-tasks", type=int, default=EXPECTED_TASKS)
    parser.add_argument("--concurrency", type=int, default=16)
    args = parser.parse_args(argv)
    if args.k < 1 or args.expected_tasks < 1 or args.concurrency < 1:
        parser.error("--k, --expected-tasks, and --concurrency must be positive")
    try:
        state = (
            merge(args.source, args.output, args.k, args.expected_tasks, args.concurrency)
            if args.output
            else inventory(args.source, args.k)
        )
    except (OSError, TypeError, ValueError, json.JSONDecodeError) as exc:
        parser.error(str(exc))
    print(json.dumps({key: value for key, value in state.items() if key != "selected"}, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
