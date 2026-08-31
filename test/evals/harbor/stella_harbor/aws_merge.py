"""Select the first five valid trials per task from ordered Harbor jobs.

Selection deliberately ignores whether a scoreable trial passed. It reads reward
only to distinguish scoreable evidence from a trial with no verifier result.
This preserves the predeclared k=5 sample instead of cherry-picking outcomes.
"""

from __future__ import annotations

import argparse
import json
import re
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


def scoreability_reason(trial: Path) -> str | None:
    result = _json(trial / "result.json")
    adapter_path = trial / "agent" / "stella" / "result.json"
    if not adapter_path.is_file():
        return "missing_adapter_result"
    adapter = _json(adapter_path)
    rewards = (result.get("verifier_result") or {}).get("rewards") or {}
    bridge = (adapter.get("metrics") or {}).get("bridge") or {}
    if adapter.get("valid") is not True:
        return "adapter_invalid"
    if not adapter.get("bridge_nonce"):
        return "missing_bridge_nonce"
    if adapter.get("predicate_violations"):
        return "predicate_violations"
    if bridge.get("adapter_faults"):
        return "bridge_adapter_faults"
    if rewards.get("reward") is None:
        return "missing_reward"
    return None


def scoreable(trial: Path) -> bool:
    return scoreability_reason(trial) is None


SAFE_EXCEPTION_WORDS = {
    "adapter",
    "address",
    "agent",
    "already",
    "async",
    "attribute",
    "binding",
    "bridge",
    "closed",
    "connection",
    "container",
    "denied",
    "directory",
    "discover",
    "docker",
    "environment",
    "error",
    "event",
    "exec",
    "exists",
    "failed",
    "file",
    "found",
    "install",
    "loop",
    "missing",
    "no",
    "not",
    "path",
    "permission",
    "requires",
    "result",
    "runtime",
    "server",
    "socket",
    "stella",
    "task",
    "timeout",
    "too",
    "upload",
    "use",
    "workdir",
}


def exception_signature(result: dict[str, Any]) -> str | None:
    exception = result.get("exception_info") or {}
    if not isinstance(exception, dict):
        return None
    message = str(exception.get("exception_message") or "").lower()
    words = re.findall(r"[a-z]+", message)
    safe = [word for word in words if word in SAFE_EXCEPTION_WORDS]
    return "_".join(safe[:16]) or ("unclassified" if exception else None)


def exception_attribute_name(result: dict[str, Any]) -> str | None:
    """Return only a Python-identifier attribute from an AttributeError."""
    exception = result.get("exception_info") or {}
    if not isinstance(exception, dict) or exception.get("exception_type") != "AttributeError":
        return None
    match = re.search(r"has no attribute ['\"]([A-Za-z_][A-Za-z0-9_]{0,63})['\"]", str(exception.get("exception_message") or ""))
    return match.group(1) if match else None


def exception_categories(result: dict[str, Any]) -> list[str]:
    exception = result.get("exception_info") or {}
    if not isinstance(exception, dict):
        return []
    message = str(exception.get("exception_message") or "").lower()
    categories = []
    checks = {
        "adapter_configuration": "stella adapter needs" in message,
        "workdir_discovery": "discover task workdir" in message,
        "bridge_discovery": "bridge discover failed" in message,
        "bridge_timeout_binary_missing": "bridge requires the task container to provide timeout" in message,
        "agent_result_missing": "did not write result" in message,
        "adapter_evidence_failure": "adapter evidence failure" in message,
        "agent_process_exit": "stella-eval-agent exited" in message,
        "docker_copy_failure": "docker cp failed" in message,
        "permission_denied": "permission denied" in message,
        "connection_refused": "connection refused" in message,
        "no_such_file": "no such file" in message,
        "auth_failed": bool(re.search(r"\b(?:401|403|unauthorized|forbidden)\b", message)),
        "provider_error": "provider" in message,
        "timeout": "timeout" in message or "timed out" in message,
    }
    categories.extend(name for name, matched in checks.items() if matched)
    return categories or (["unclassified"] if exception else [])


def ordered_trials(source: Path) -> list[Path]:
    """Find Harbor trial roots in lexical pass order, excluding agent results."""
    return [
        path.parent
        for path in sorted(source.rglob("result.json"))
        if (path.parent / "config.json").is_file() and (path.parent / "agent").is_dir()
    ]


def inventory(source: Path, k: int) -> dict[str, Any]:
    trials = ordered_trials(source)
    observed: dict[str, int] = defaultdict(int)
    valid: dict[str, list[Path]] = defaultdict(list)
    invalid: dict[str, int] = defaultdict(int)
    invalid_reasons: dict[str, int] = defaultdict(int)
    exception_types: dict[str, int] = defaultdict(int)
    exception_categories_seen: dict[str, int] = defaultdict(int)
    exception_signatures: dict[str, int] = defaultdict(int)
    exception_attributes: dict[str, int] = defaultdict(int)
    for trial in trials:
        task = task_name(trial)
        observed[task] += 1
        result = _json(trial / "result.json")
        exception = result.get("exception_info") or {}
        if isinstance(exception, dict) and exception.get("exception_type"):
            exception_type = re.sub(r"[^A-Za-z0-9_.-]", "_", str(exception["exception_type"]))[:80]
            exception_types[exception_type] += 1
        for category in exception_categories(result):
            exception_categories_seen[category] += 1
        signature = exception_signature(result)
        if signature:
            exception_signatures[signature] += 1
        attribute = exception_attribute_name(result)
        if attribute:
            exception_attributes[attribute] += 1
        reason = scoreability_reason(trial)
        if reason is None:
            valid[task].append(trial)
        else:
            invalid[task] += 1
            invalid_reasons[reason] += 1

    tasks = sorted(observed)
    missing = {task: max(0, k - len(valid[task])) for task in tasks if len(valid[task]) < k}
    return {
        "tasks": len(tasks),
        "task_names": tasks,
        "trials": len(trials),
        "scoreable": sum(len(items) for items in valid.values()),
        "invalid": sum(invalid.values()),
        "invalid_reasons": dict(sorted(invalid_reasons.items())),
        "exception_types": dict(sorted(exception_types.items())),
        "exception_categories": dict(sorted(exception_categories_seen.items())),
        "exception_signatures": dict(sorted(exception_signatures.items())),
        "exception_attributes": dict(sorted(exception_attributes.items())),
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
