"""Turn a Harbor job directory into a reviewable metrics table.

A reward tells you whether one trial passed. This tells you what it cost, where
the time went, and how reliably the result repeats, which is what a reviewer
needs in order to decide whether a run is worth trusting.

    python -m stella_harbor.report dist/evals/jobs/<job>
    python -m stella_harbor.report dist/evals/jobs/<job> --html report.html
"""

from __future__ import annotations

import argparse
import json
import math
import sys
from collections import defaultdict
from pathlib import Path
from typing import Any

from .taxonomy import breakdown

TRIAL_COLUMNS = [
    ("task", 24), ("reward", 6), ("valid", 5), ("state", 9), ("wall", 7),
    ("model", 7), ("tool", 7), ("bridge", 7), ("turns", 5), ("calls", 5),
    ("errs", 4), ("in.tok", 8), ("out.tok", 8), ("cost", 8),
]

# Terminal-Bench scores a trial as resolved only on a full reward.
RESOLVED = 1.0


def _seconds(ms: Any) -> str:
    try:
        return f"{int(ms) / 1000:.1f}s"
    except (TypeError, ValueError):
        return "-"


def _int(value: Any) -> str:
    return "-" if value is None else str(value)


def _usd(value: Any) -> str:
    # "-" means the provider reported no usage or the model has no configured
    # price. It is never 0.00: a free-looking number is the one mistake a cost
    # column cannot afford.
    return "-" if value is None else f"${value:.4f}"


def wilson_interval(successes: int, trials: int, z: float = 1.96) -> tuple[float, float]:
    """95% Wilson score interval.

    Wilson rather than the normal approximation because agent evals routinely
    land at 0/5 or 5/5, where the normal interval collapses to zero width and
    claims a certainty the sample cannot support.
    """
    if trials == 0:
        return (0.0, 0.0)
    p = successes / trials
    denom = 1 + z * z / trials
    center = (p + z * z / (2 * trials)) / denom
    margin = z * math.sqrt(p * (1 - p) / trials + z * z / (4 * trials * trials)) / denom
    return (max(0.0, center - margin), min(1.0, center + margin))


def collect(job_dir: Path) -> list[dict[str, Any]]:
    """Read one row per trial."""
    rows: list[dict[str, Any]] = []
    for trial in sorted(job_dir.glob("*/*/result.json")):
        adapter_path = trial.parent / "agent" / "stella" / "result.json"
        harbor = json.loads(trial.read_text())
        adapter = json.loads(adapter_path.read_text()) if adapter_path.exists() else {}
        metrics = adapter.get("metrics") or {}
        timing = metrics.get("timing_ms") or {}
        rewards = (harbor.get("verifier_result") or {}).get("rewards") or {}
        rows.append({
            "task": trial.parent.name.split("__")[0],
            # A trial that raised has no verifier result at all; that is not a zero.
            "reward": rewards.get("reward"),
            "valid": adapter.get("valid"),
            "state": adapter.get("turn_terminal_state") or ("exception" if harbor.get("exception_info") else "-"),
            "wall_ms": timing.get("total"),
            "model_ms": timing.get("model"),
            "tool_ms": timing.get("tool"),
            "bridge_ms": (metrics.get("bridge") or {}).get("total_ms"),
            "adapter_faults": (metrics.get("bridge") or {}).get("adapter_faults") or [],
            "turns": metrics.get("turns"),
            "calls": metrics.get("tool_call_total"),
            "tool_errors": metrics.get("tool_error_total"),
            "est_tokens": (metrics.get("tokens_estimated") or {}).get("total"),
            "usage": metrics.get("usage") or {},
            "timed_out": adapter.get("timed_out"),
            "stream_errors": adapter.get("stream_errors") or [],
            "tools": metrics.get("tools") or {},
            "violations": adapter.get("predicate_violations") or [],
            # Kept whole for the HTML report, which shows per-trial detail the
            # terminal table has no room for.
            "metrics": metrics,
            "ledger": adapter.get("bridge_ledger") or [],
        })
    return rows


def reliability(rows: list[dict[str, Any]]) -> dict[str, Any]:
    """Aggregate trials into the numbers a leaderboard entry has to state.

    Only valid trials count. An invalid trial is not a failure either: it is a
    trial that produced no evidence, so it is excluded from the denominator and
    reported separately. Hiding it inside a pass rate would let a broken harness
    read as a weak model.
    """
    by_task: dict[str, list[dict[str, Any]]] = defaultdict(list)
    for row in rows:
        by_task[row["task"]].append(row)

    tasks: list[dict[str, Any]] = []
    for task, trials in sorted(by_task.items()):
        scoreable = [t for t in trials if t["valid"] and t["reward"] is not None]
        resolved = [t for t in scoreable if t["reward"] >= RESOLVED]
        tasks.append({
            "task": task,
            "trials": len(trials),
            "scoreable": len(scoreable),
            "invalid": len(trials) - len(scoreable),
            "resolved": len(resolved),
            # pass^k demands every scoreable trial resolve; one trial is a
            # pass^1 and must not be presented as reliability evidence.
            "pass_hat_k": bool(scoreable) and len(resolved) == len(scoreable),
            "timeouts": sum(1 for t in trials if t["timed_out"]),
        })

    scoreable = sum(t["scoreable"] for t in tasks)
    resolved = sum(t["resolved"] for t in tasks)
    k = min((t["scoreable"] for t in tasks), default=0)
    return {
        "tasks": tasks,
        "trials": sum(t["trials"] for t in tasks),
        "scoreable": scoreable,
        "invalid": sum(t["invalid"] for t in tasks),
        "resolved": resolved,
        "resolution_rate": resolved / scoreable if scoreable else None,
        "ci95": wilson_interval(resolved, scoreable),
        "k": k,
        "pass_hat_k": sum(1 for t in tasks if t["pass_hat_k"]) / len(tasks) if tasks else None,
        "timeouts": sum(t["timeouts"] for t in tasks),
    }


def render(rows: list[dict[str, Any]]) -> str:
    if not rows:
        return "no trials found"
    header = "  ".join(name.ljust(width) for name, width in TRIAL_COLUMNS)
    lines = [header, "-" * len(header)]
    for row in rows:
        cells = [
            str(row["task"])[:24], "-" if row["reward"] is None else f"{row['reward']:.2f}",
            {True: "yes", False: "NO", None: "-"}[row["valid"]], str(row["state"])[:9],
            _seconds(row["wall_ms"]), _seconds(row["model_ms"]), _seconds(row["tool_ms"]),
            _seconds(row["bridge_ms"]), str(row["turns"] if row["turns"] is not None else "-"),
            str(row["calls"] if row["calls"] is not None else "-"),
            str(row["tool_errors"] if row["tool_errors"] is not None else "-"),
            _int((row.get("usage") or {}).get("input_tokens")),
            _int((row.get("usage") or {}).get("output_tokens")),
            _usd((row.get("usage") or {}).get("cost_usd")),
        ]
        lines.append("  ".join(cell.ljust(width) for cell, (_, width) in zip(cells, TRIAL_COLUMNS)))

    stats = reliability(rows)
    lines.append("")
    if stats["resolution_rate"] is None:
        lines.append(f"no scoreable trials ({stats['invalid']} invalid of {stats['trials']})")
    else:
        low, high = stats["ci95"]
        margin = (high - low) / 2 * 100
        lines.append(
            f"resolution rate {stats['resolution_rate'] * 100:.1f}% ±{margin:.1f}% "
            f"(95% CI {low * 100:.1f}–{high * 100:.1f}, {stats['resolved']}/{stats['scoreable']} trials)")
        if stats["k"] >= 2:
            lines.append(f"pass^{stats['k']} {stats['pass_hat_k'] * 100:.1f}% of {len(stats['tasks'])} tasks")
        else:
            lines.append("pass^k unavailable: at least one task has fewer than 2 scoreable trials "
                         "(run harbor with -k 5 for a reportable number)")
    if stats["invalid"]:
        lines.append(f"{stats['invalid']} trial(s) excluded as invalid; they are not failures, they are missing evidence")
    if stats["timeouts"]:
        lines.append(f"{stats['timeouts']} trial(s) hit the deadline")

    if any(t["trials"] > 1 for t in stats["tasks"]):
        lines.append("")
        lines.append("task                      trials  valid  resolved  pass^k")
        for t in stats["tasks"]:
            lines.append(f"{t['task'][:24]:24}  {t['trials']:6}  {t['scoreable']:5}  "
                         f"{t['resolved']:8}  {'yes' if t['pass_hat_k'] else 'no'}")

    faulted = [(row, fault) for row in rows for fault in row["adapter_faults"]]
    if faulted:
        lines.append("")
        lines.append(f"{len(faulted)} bridge adapter fault(s) — harness bugs, not task difficulty:")
        for row, fault in faulted[:10]:
            lines.append(f"  {row['task']}: {fault.get('op')} {fault.get('path') or ''} -> {fault.get('code')}")

    for row in rows:
        if row["violations"]:
            lines.append(f"  {row['task']}: invalid — {'; '.join(row['violations'])}")

    tools: dict[str, dict[str, int]] = {}
    for row in rows:
        for name, stat in row["tools"].items():
            agg = tools.setdefault(name, {"calls": 0, "errors": 0, "total_ms": 0, "max_ms": 0})
            agg["calls"] += stat.get("calls", 0)
            agg["errors"] += stat.get("errors", 0)
            agg["total_ms"] += stat.get("total_ms", 0)
            agg["max_ms"] = max(agg["max_ms"], stat.get("max_ms", 0))
    if tools:
        lines.append("")
        lines.append("tool                 calls  errs  total    slowest")
        for name in sorted(tools, key=lambda n: -tools[n]["total_ms"]):
            stat = tools[name]
            lines.append(
                f"{name[:20]:20} {stat['calls']:5}  {stat['errors']:4}  "
                f"{_seconds(stat['total_ms']):7}  {_seconds(stat['max_ms'])}")

    failures = [b for b in breakdown(rows) if b["label"] not in {"resolved", "invalid"}]
    if failures:
        lines.append("")
        lines.append("failure                 count  why")
        for b in failures:
            lines.append(f"{b['label'][:22]:22}  {b['count']:5}  {b['description']}")
            lines.append(f"{'':22}         e.g. {b['example_task']}: {b['example_reason']}")

    priced = [r for r in rows if (r.get("usage") or {}).get("cost_usd") is not None]
    lines.append("")
    if priced:
        total = sum(r["usage"]["cost_usd"] for r in priced)
        lines.append(f"cost ${total:.4f} across {len(priced)} of {len(rows)} trials, "
                     f"${total / len(priced):.4f} per priced trial")
    if len(rows) - len(priced):
        lines.append(f"{len(rows) - len(priced)} trial(s) have no cost: the provider reported no "
                     f"usage, or the model has no configured price. That is not $0.")
    lines.append("in.tok/out.tok/cost are provider-reported. The per-message estimate (len/4) stays "
                 "in the trial JSON and is never used here.")
    return "\n".join(lines)


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(prog="stella_harbor.report", description=__doc__)
    parser.add_argument("job_dir", type=Path)
    parser.add_argument("--html", type=Path, metavar="FILE",
                        help="also write a self-contained HTML report to FILE")
    args = parser.parse_args(argv)

    rows = collect(args.job_dir)
    print(render(rows))
    if args.html:
        from .htmlreport import render_html

        args.html.parent.mkdir(parents=True, exist_ok=True)
        args.html.write_text(render_html(rows, str(args.job_dir)))
        print(f"\nwrote {args.html}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
