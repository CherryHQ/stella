"""Put two Harbor jobs side by side.

The Stella adapter writes its own evidence, but a baseline run (upstream pi,
Terminus, a published community job) writes only Harbor's per-trial result.json.
Comparing has to work off that common denominator, so this reads reward and the
provider-reported usage Harbor records for every agent, and reports validity
only when the Stella adapter happens to be present.

    python -m stella_harbor.compare dist/evals/jobs/sample dist/evals/jobs/pi-sample
"""

from __future__ import annotations

import argparse
import json
import sys
from collections import defaultdict
from pathlib import Path
from typing import Any

from .fingerprint import (
    FingerprintMismatchError,
    collect_fingerprint,
    fingerprint_mismatches,
    format_mismatches,
)
from .report import RESOLVED, wilson_interval


def latest_run(job_dir: Path) -> Path:
    """Accept either a job root or one timestamped run inside it."""
    if (job_dir / "result.json").exists():
        return job_dir
    runs = sorted(p for p in job_dir.iterdir() if p.is_dir() and (p / "result.json").exists())
    if not runs:
        raise SystemExit(f"no completed run under {job_dir}")
    return runs[-1]


def load(job_dir: Path) -> list[dict[str, Any]]:
    run = latest_run(job_dir)
    rows: list[dict[str, Any]] = []
    for trial in sorted(p for p in run.iterdir() if p.is_dir()):
        result = trial / "result.json"
        if not result.exists():
            continue
        data = json.loads(result.read_text())
        agent = data.get("agent_result") or {}
        adapter_path = trial / "agent" / "stella" / "result.json"
        adapter = json.loads(adapter_path.read_text()) if adapter_path.exists() else {}
        rows.append({
            "task": trial.name.rsplit("__", 1)[0],
            "reward": ((data.get("verifier_result") or {}).get("rewards") or {}).get("reward"),
            "cost_usd": agent.get("cost_usd"),
            "input_tokens": agent.get("n_input_tokens"),
            "output_tokens": agent.get("n_output_tokens"),
            # Absent adapter means an agent that carries no evidence contract;
            # that is not the same as a trial that failed one.
            "valid": adapter.get("valid") if adapter else None,
        })
    return rows


def summarize(rows: list[dict[str, Any]]) -> dict[str, Any]:
    scoreable = [r for r in rows if r["valid"] is not False]
    resolved = [r for r in scoreable if (r["reward"] or 0) >= RESOLVED]
    costs = [r["cost_usd"] for r in rows if r["cost_usd"] is not None]
    low, high = wilson_interval(len(resolved), len(scoreable))
    return {
        "trials": len(rows),
        "invalid": len(rows) - len(scoreable),
        "scoreable": len(scoreable),
        "resolved": len(resolved),
        "rate": len(resolved) / len(scoreable) if scoreable else 0.0,
        "ci": (low, high),
        "cost": sum(costs) if costs else None,
    }


def _by_task(rows: list[dict[str, Any]]) -> dict[str, dict[str, Any]]:
    grouped: dict[str, list[dict[str, Any]]] = defaultdict(list)
    for row in rows:
        grouped[row["task"]].append(row)
    return {task: summarize(trials) for task, trials in grouped.items()}


def render(
    left: list[dict[str, Any]],
    right: list[dict[str, Any]],
    names: tuple[str, str],
    *,
    mismatches: list[dict[str, Any]] | None = None,
) -> str:
    left_tasks, right_tasks = _by_task(left), _by_task(right)
    tasks = sorted(set(left_tasks) | set(right_tasks))
    width = max([24, *(len(t) for t in tasks)])

    def cell(stats: dict[str, Any] | None) -> str:
        if not stats:
            return f"{'-':>16}"
        cost = "-" if stats["cost"] is None else f"${stats['cost']:.3f}"
        return f"{stats['resolved']}/{stats['scoreable']} {cost:>9}"

    untrusted = bool(mismatches)
    marker = "[UNTRUSTWORTHY COMPARISON] "

    def mark(line: str) -> str:
        return marker + line if untrusted else line

    out = [mark(f"{names[0]}  vs  {names[1]}"), ""]
    if untrusted:
        out.extend(mark(line) for line in [
            "Fingerprint validation failed; this output must not be used to attribute score changes.",
            *format_mismatches(mismatches or []),
        ])
        out.append("")
    out.append(mark(f"{'task':<{width}}  {names[0][:16]:>16}  {names[1][:16]:>16}"))
    out.append(mark("-" * (width + 38)))
    for task in tasks:
        out.append(mark(f"{task:<{width}}  {cell(left_tasks.get(task)):>16}  {cell(right_tasks.get(task)):>16}"))

    out.append("")
    for name, rows in ((names[0], left), (names[1], right)):
        s = summarize(rows)
        cost = "-" if s["cost"] is None else f"${s['cost']:.2f}"
        invalid = f", {s['invalid']} invalid" if s["invalid"] else ""
        out.append(mark(
            f"{name}: {s['resolved']}/{s['scoreable']} resolved "
            f"({s['rate'] * 100:.1f}%, 95% CI {s['ci'][0] * 100:.1f}-{s['ci'][1] * 100:.1f}%), "
            f"total {cost}{invalid}"
        ))
    out.append("")
    out.append(mark("Costs come from each agent's own usage reporting, so they are comparable"))
    out.append(mark("only when both runs used the same model and price table."))
    return "\n".join(out)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Compare two Harbor job directories.")
    parser.add_argument("left", type=Path)
    parser.add_argument("right", type=Path)
    parser.add_argument("--names", nargs=2, metavar=("LEFT", "RIGHT"))
    parser.add_argument(
        "--allow-mismatch",
        action="store_true",
        help="render an explicitly untrusted comparison despite fingerprint mismatches",
    )
    args = parser.parse_args(argv)
    names = tuple(args.names) if args.names else (args.left.name, args.right.name)

    left_fingerprint = collect_fingerprint(args.left)
    right_fingerprint = collect_fingerprint(args.right)
    mismatches = fingerprint_mismatches(left_fingerprint, right_fingerprint)
    if mismatches and not args.allow_mismatch:
        print(str(FingerprintMismatchError(mismatches)), file=sys.stderr)
        return 2

    print(render(load(args.left), load(args.right), names, mismatches=mismatches or None))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
