"""Turn a Harbor job directory into a reviewable metrics table.

A reward tells you whether the trial passed. This tells you what it cost and
where the time went, which is what a reviewer needs in order to decide whether
a run is worth trusting or worth optimizing.

    python -m stella_harbor.report dist/evals/jobs/<job>
"""

from __future__ import annotations

import json
import sys
from pathlib import Path
from typing import Any

COLUMNS = [
    ("task", 24), ("reward", 6), ("valid", 5), ("state", 9), ("wall", 7),
    ("model", 7), ("tool", 7), ("bridge", 7), ("turns", 5), ("calls", 5),
    ("errs", 4), ("tokens", 7),
]


def _seconds(ms: Any) -> str:
    try:
        return f"{int(ms) / 1000:.1f}s"
    except (TypeError, ValueError):
        return "-"


def collect(job_dir: Path) -> list[dict[str, Any]]:
    """Read one row per trial, newest job run first."""
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
            "turns": metrics.get("turns"),
            "calls": metrics.get("tool_call_total"),
            "tool_errors": metrics.get("tool_error_total"),
            "tokens": (metrics.get("tokens") or {}).get("total"),
            "tools": metrics.get("tools") or {},
            "slowest": metrics.get("slowest_tool_call"),
            "violations": adapter.get("predicate_violations") or [],
        })
    return rows


def render(rows: list[dict[str, Any]]) -> str:
    """Render the table plus the per-tool and failure detail worth reading."""
    if not rows:
        return "no trials found"
    header = "  ".join(name.ljust(width) for name, width in COLUMNS)
    lines = [header, "-" * len(header)]
    for row in rows:
        cells = [
            str(row["task"])[:24], "-" if row["reward"] is None else f"{row['reward']:.2f}",
            {True: "yes", False: "NO", None: "-"}[row["valid"]], str(row["state"])[:9],
            _seconds(row["wall_ms"]), _seconds(row["model_ms"]), _seconds(row["tool_ms"]),
            _seconds(row["bridge_ms"]), str(row["turns"] if row["turns"] is not None else "-"),
            str(row["calls"] if row["calls"] is not None else "-"),
            str(row["tool_errors"] if row["tool_errors"] is not None else "-"),
            str(row["tokens"] if row["tokens"] is not None else "-"),
        ]
        lines.append("  ".join(cell.ljust(width) for cell, (_, width) in zip(cells, COLUMNS)))

    scored = [r for r in rows if r["valid"] and r["reward"] is not None]
    lines.append("")
    lines.append(f"{len(scored)}/{len(rows)} trials scoreable" + (
        f", mean reward {sum(r['reward'] for r in scored) / len(scored):.3f}" if scored else ""))

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
    return "\n".join(lines)


def main(argv: list[str]) -> int:
    if len(argv) != 1:
        print(__doc__, file=sys.stderr)
        return 2
    print(render(collect(Path(argv[0]))))
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
