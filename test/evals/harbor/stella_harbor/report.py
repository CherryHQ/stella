"""Turn a Harbor job directory into a reviewable metrics table.

A reward tells you whether one trial passed. This tells you what it cost, where
the time went, and how reliably the result repeats, which is what a reviewer
needs in order to decide whether a run is worth trusting.

    python -m stella_harbor.report dist/evals/jobs/<job>
    python -m stella_harbor.report dist/evals/jobs/<job> --csv out/
    python -m stella_harbor.report dist/evals/jobs/<job> --html report.html

`--csv` is the one output worth keeping. It carries raw values, not formatted
ones, so a later reader can recompute anything without this code, and it diffs
and sorts with ordinary tools. The text table and the HTML are views: render
them when someone wants to look, never archive them as the record.
"""

from __future__ import annotations

import argparse
import csv
import json
import math
import sys
from collections import defaultdict
from pathlib import Path
from typing import Any

from .taxonomy import breakdown

TRIAL_COLUMNS = [
    ("task", 24), ("reward", 6), ("valid", 5), ("state", 9), ("wall", 7),
    ("model", 7), ("tool", 7), ("bridge", 7), ("turns", 5), ("orch", 5), ("exec", 5),
    ("errs", 4), ("cmd!0", 5), ("in.tok", 8), ("out.tok", 8), ("cost", 8),
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
        # Other Harbor agents (pi, and anything else we baseline against) write
        # no Stella adapter file, so the runtime columns stay empty. Their usage
        # still lives in Harbor's own agent_result, and a cost column that reads
        # "-" for a trial the provider did price is a wrong number, not a blank.
        usage = metrics.get("usage") or {}
        if not usage:
            agent_result = harbor.get("agent_result") or {}
            usage = {
                "input_tokens": agent_result.get("n_input_tokens"),
                "output_tokens": agent_result.get("n_output_tokens"),
                "cost_usd": agent_result.get("cost_usd"),
            }
            usage = {k: v for k, v in usage.items() if v is not None}
        timing = metrics.get("timing_ms") or {}
        nonzero, timeouts = _command_outcomes(metrics, adapter.get("bridge_ledger") or [])
        rewards = (harbor.get("verifier_result") or {}).get("rewards") or {}
        rows.append({
            "task": trial.parent.name.split("__")[0],
            # A trial that raised has no verifier result at all; that is not a zero.
            "reward": rewards.get("reward"),
            # Validity is Stella-adapter evidence. For an agent that writes none,
            # the absence is not missing evidence: Harbor's own verifier reward is
            # the evidence, so scoring must not silently drop every trial.
            "valid": adapter.get("valid") if adapter else rewards.get("reward") is not None,
            "state": adapter.get("turn_terminal_state") or ("exception" if harbor.get("exception_info") else "-"),
            "wall_ms": timing.get("total"),
            "model_ms": timing.get("model"),
            "tool_ms": timing.get("tool"),
            "bridge_ms": (metrics.get("bridge") or {}).get("total_ms"),
            "adapter_faults": (metrics.get("bridge") or {}).get("adapter_faults") or [],
            "turns": metrics.get("turns"),
            # Taxonomy and per-tool efficiency use invocation attempts, not
            # Code Mode's provider-visible outer call. The display keeps both
            # fields so orchestration stays observable without contaminating
            # cross-strategy execution comparisons.
            "calls": metrics.get("execution_tool_call_total", metrics.get("tool_call_total")),
            "orchestration_calls": metrics.get("orchestration_tool_call_total", metrics.get("tool_call_total")),
            "execution_calls": metrics.get("execution_tool_call_total", metrics.get("tool_call_total")),
            "tool_errors": metrics.get("execution_tool_error_total", metrics.get("tool_error_total")),
            # Outer-call failures (a `code` call that failed as a whole) stay
            # out of the execution comparison but must remain observable.
            "orchestration_tool_errors": metrics.get("orchestration_tool_error_total", metrics.get("tool_error_total")),
            # command_nonzero_total is the driver's own split: commands that ran
            # and exited nonzero, already kept out of tool_error_total. None
            # means the trial never measured it — a Stella run archived before
            # the split, or an agent that writes no adapter metrics at all —
            # and None is not 0, so nothing here may claim they saw none.
            # command_nonzero stays the ledger's recount, which is the only
            # number a pre-split Stella trial has; a non-Stella trial has no
            # ledger and no tool counts to correct.
            "command_nonzero_total": metrics.get("execution_command_nonzero_total", metrics.get("command_nonzero_total")),
            "command_nonzero": nonzero,
            "command_timeout": metrics.get("execution_command_timeout_total", metrics.get("command_timeout_total", timeouts)),
            "tool_faults": _tool_faults(metrics, nonzero + timeouts),
            "est_tokens": (metrics.get("tokens_estimated") or {}).get("total"),
            "usage": usage,
            "timed_out": adapter.get("timed_out"),
            "stream_errors": adapter.get("stream_errors") or [],
            "tools": metrics.get("execution_tools", metrics.get("tools")) or {},
            "violations": adapter.get("predicate_violations") or [],
            # Kept whole for the HTML report, which shows per-trial detail the
            # terminal table has no room for.
            "metrics": metrics,
            "ledger": adapter.get("bridge_ledger") or [],
        })
    return rows


def _command_outcomes(metrics: dict[str, Any], ledger: list[dict[str, Any]]) -> tuple[int, int]:
    """Commands the container answered nonzero, and commands we killed.

    The driver records both at trial end, but jobs run before it did still
    carry the raw ledger, which is where the counts came from in the first
    place. Recount from it so an old job reports the same numbers as a new one.
    """
    bridge = metrics.get("bridge") or {}
    if "command_nonzero" in bridge:
        return bridge.get("command_nonzero") or 0, bridge.get("command_timeout") or 0
    nonzero = timeouts = 0
    for entry in ledger:
        if not entry.get("ok") or (entry.get("op") or "") != "exec":
            continue
        code = entry.get("return_code")
        if code == -1:
            timeouts += 1
        elif isinstance(code, int) and code != 0:
            nonzero += 1
    return nonzero, timeouts


def _tool_faults(metrics: dict[str, Any], explained: int) -> int | None:
    """Tool calls that actually failed — the only number the taxonomy may read.

    A trial from a driver that splits the counters needs no correction: its
    tool_error_total already excludes commands that merely exited nonzero.
    Older trials get the historical treatment, the ledger's count subtracted
    back out, because that is the best evidence they carry and re-reading them
    under the new rule would rewrite what those runs measured.
    """
    # Do not re-infer a failure from the outer `code` transcript result or from
    # the bridge's low-level operations; the recorded execution-attempt total is
    # the only comparable count.
    if metrics.get("execution_tool_error_total") is not None:
        return metrics.get("execution_tool_error_total")
    errors = metrics.get("tool_error_total")
    if errors is None:
        return None
    if metrics.get("command_nonzero_total") is not None:
        return errors
    return max(0, errors - explained)


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
            str(row["orchestration_calls"] if row["orchestration_calls"] is not None else "-"),
            str(row["execution_calls"] if row["execution_calls"] is not None else "-"),
            str(row["tool_errors"] if row["tool_errors"] is not None else "-"),
            # "-", never 0: this trial did not measure the field (pre-split
            # Stella archive, or an agent with no adapter metrics).
            _int(row.get("command_nonzero_total")),
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

    tools: dict[str, dict[str, Any]] = {}
    for row in rows:
        for name, stat in row["tools"].items():
            agg = tools.setdefault(name, {"calls": 0, "errors": 0, "command_nonzero": 0,
                                          "command_nonzero_known": True, "total_ms": 0, "max_ms": 0})
            agg["calls"] += stat.get("calls", 0)
            agg["errors"] += stat.get("errors", 0)
            # A trial that never measured the split carries no per-tool count.
            # One of those in the job makes the whole column unknowable: adding
            # the rest and printing the sum would pass a partial number off as
            # the total.
            if "command_nonzero" in stat:
                agg["command_nonzero"] += stat.get("command_nonzero") or 0
            else:
                agg["command_nonzero_known"] = False
            agg["total_ms"] += stat.get("total_ms", 0)
            agg["max_ms"] = max(agg["max_ms"], stat.get("max_ms", 0))
    if tools:
        lines.append("")
        lines.append("tool                 calls  errs  cmd!0  total    slowest")
        for name in sorted(tools, key=lambda n: -tools[n]["total_ms"]):
            stat = tools[name]
            lines.append(
                f"{name[:20]:20} {stat['calls']:5}  {stat['errors']:4}  "
                f"{_int(stat['command_nonzero'] if stat['command_nonzero_known'] else None):5}  "
                f"{_seconds(stat['total_ms']):7}  {_seconds(stat['max_ms'])}")
        split = [r for r in rows if r.get("command_nonzero_total") is not None]
        # A trial with no tool-error count at all is not an old Stella trial,
        # it is another agent (pi has no Stella adapter file). It has nothing
        # to correct and must not be described as predating the split.
        legacy = [r for r in rows
                  if r.get("command_nonzero_total") is None and r.get("tool_errors") is not None]
        if split:
            lines.append(
                f"errs counts tools that failed. cmd!0 is the {sum(r['command_nonzero_total'] for r in split)}"
                " command(s) that ran and exited nonzero across those trials — the container"
                " answering, not a tool failing — and the failure classes below exclude them.")
        if legacy:
            nonzero = sum(r.get("command_nonzero") or 0 for r in legacy)
            timeouts = sum(r.get("command_timeout") or 0 for r in legacy)
            if nonzero or timeouts:
                lines.append(
                    f"{len(legacy)} trial(s) predate the split, so their errs still include"
                    f" {nonzero} nonzero exit(s) and {timeouts} timeout(s) recounted from the"
                    " bridge ledger; the failure classes below subtract those instead.")

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


# Raw values, in the units the harness measured them in. A CSV that says "1.2s"
# has thrown away the number and kept a rendering of it.
TRIAL_CSV_COLUMNS = [
    "task", "reward", "valid", "state", "timed_out", "wall_ms", "model_ms", "tool_ms",
    "bridge_ms", "turns", "orchestration_calls", "execution_calls", "tool_errors",
    "command_nonzero_total", "input_tokens", "output_tokens", "cost_usd",
]

TASK_CSV_COLUMNS = ["task", "trials", "scoreable", "resolved", "pass_k"]


def write_csv(rows: list[dict[str, Any]], out_dir: Path) -> list[Path]:
    """Write the job's two tables as CSV and return what was written.

    An empty cell means the field was never measured. It is never a zero: the
    whole point of keeping the raw file is that a later reader can tell those
    two apart, which no rendered report lets them do.
    """
    out_dir.mkdir(parents=True, exist_ok=True)
    trials_path = out_dir / "trials.csv"
    with trials_path.open("w", newline="") as handle:
        writer = csv.DictWriter(handle, TRIAL_CSV_COLUMNS, extrasaction="ignore")
        writer.writeheader()
        for row in rows:
            usage = row.get("usage") or {}
            record = {key: row.get(key) for key in TRIAL_CSV_COLUMNS}
            record["orchestration_calls"] = row.get("orchestration_calls", row.get("calls"))
            for field in ("input_tokens", "output_tokens", "cost_usd"):
                record[field] = usage.get(field)
            writer.writerow({k: ("" if v is None else v) for k, v in record.items()})

    tasks_path = out_dir / "tasks.csv"
    with tasks_path.open("w", newline="") as handle:
        writer = csv.DictWriter(handle, TASK_CSV_COLUMNS)
        writer.writeheader()
        for task in reliability(rows)["tasks"]:
            writer.writerow({"task": task["task"], "trials": task["trials"],
                             "scoreable": task["scoreable"], "resolved": task["resolved"],
                             "pass_k": int(bool(task["pass_hat_k"]))})
    return [trials_path, tasks_path]


def timeout_count(rows: list[dict[str, Any]]) -> int | None:
    """How many trials hit the deadline, or None if nothing measured it.

    `timed_out` comes from Stella's adapter file, so every trial of another
    agent leaves it unset. Counting those as False reports a clean zero for a
    run that never checked, which is the one answer a deadline column must
    never give.
    """
    measured = [r for r in rows if r.get("timed_out") is not None]
    return sum(1 for r in measured if r["timed_out"]) if measured else None


def _percentile(values: list[int], fraction: float) -> int | None:
    if not values:
        return None
    ordered = sorted(values)
    index = min(len(ordered) - 1, round(fraction * (len(ordered) - 1)))
    return ordered[index]


def summarize(rows: list[dict[str, Any]]) -> dict[str, Any]:
    """One release's worth of metrics: the row that joins the timeline.

    Every rate here has a denominator that can be zero and a numerator that can
    be unmeasured, and both come out as None rather than 0. A release row is
    read years later by someone who cannot re-run the job, so "we never measured
    this" has to survive as its own answer.
    """
    stats = reliability(rows)
    trials = len(rows)

    # A single trial that never measured the split makes the job total
    # unknowable; summing the rest would pass a partial count off as the whole.
    split = [r for r in rows if r.get("command_nonzero_total") is not None]
    command_nonzero = sum(r["command_nonzero_total"] for r in split) if len(split) == trials else None

    measured = [r for r in rows if r.get("tool_errors") is not None]
    tool_calls = sum((r.get("execution_calls") or r.get("calls") or 0) for r in measured)
    tool_errors = sum(r["tool_errors"] for r in measured) if measured else None

    priced = [r for r in rows if (r.get("usage") or {}).get("cost_usd") is not None]
    cost = sum(r["usage"]["cost_usd"] for r in priced) if priced else None
    walls = [r["wall_ms"] for r in rows if r.get("wall_ms") is not None]
    timeouts = timeout_count(rows)

    def tokens(field: str) -> int | None:
        seen = [(r.get("usage") or {}).get(field) for r in rows]
        present = [v for v in seen if v is not None]
        return sum(present) if present else None

    def rate(value: float | None) -> float | None:
        # Four places: enough to separate 445-trial runs, short enough that the
        # CSV line stays something a person can read in a diff.
        return None if value is None else round(value, 4)

    return {
        "trials": trials,
        "scoreable": stats["scoreable"],
        "resolved": stats["resolved"],
        "resolution": rate(stats["resolution_rate"]),
        "pass_k": rate(stats["pass_hat_k"]) if stats["k"] >= 2 else None,
        "invalid": stats["invalid"],
        "timeouts": timeouts,
        "timeout_rate": rate(timeouts / trials) if timeouts is not None and trials else None,
        "tool_calls": tool_calls if measured else None,
        "tool_errors": tool_errors,
        "tool_fault_rate": rate(tool_errors / tool_calls) if tool_errors is not None and tool_calls else None,
        "command_nonzero": command_nonzero,
        "priced_trials": len(priced),
        "priced_coverage": rate(len(priced) / trials) if trials else None,
        "cost_usd": round(cost, 4) if cost is not None else None,
        "cost_per_priced_trial": round(cost / len(priced), 6) if priced else None,
        "input_tokens": tokens("input_tokens"),
        "output_tokens": tokens("output_tokens"),
        "wall_p50_ms": _percentile(walls, 0.50),
        "wall_p90_ms": _percentile(walls, 0.90),
    }


def timeline_row(rows: list[dict[str, Any]], **identity: Any) -> str:
    """The job's release row as one CSV line, ready to append to timeline.csv.

    Generated rather than hand-typed: a scoreboard whose numbers are retyped
    from a rendered report is a scoreboard with typos nobody can audit.
    """
    from io import StringIO

    from . import timeline

    record = {**identity, **summarize(rows)}
    buffer = StringIO()
    writer = csv.DictWriter(buffer, timeline.COLUMNS, extrasaction="ignore")
    writer.writerow({k: ("" if record.get(k) is None else record[k]) for k in timeline.COLUMNS})
    return buffer.getvalue().rstrip("\r\n")


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(prog="stella_harbor.report", description=__doc__)
    parser.add_argument("job_dir", type=Path)
    parser.add_argument("--timeline-row", action="store_true",
                        help="print this job as one timeline.csv line, to append when the run is "
                             "archived. Identity fields come from --set")
    parser.add_argument("--set", action="append", default=[], metavar="KEY=VALUE",
                        help="an identity field for --timeline-row, e.g. --set date=2026-08-31 "
                             "--set agent=Stella. Repeatable")
    parser.add_argument("--csv", type=Path, metavar="DIR",
                        help="write trials.csv and tasks.csv to DIR: raw values, the form worth "
                             "keeping")
    parser.add_argument("--html", type=Path, metavar="FILE",
                        help="render a self-contained HTML view to FILE. A view, not a record: "
                             "regenerate it, do not archive it")
    parser.add_argument("--detail", action="store_true",
                        help="include the per-trial ledger and tool breakdown in the HTML. Off by "
                             "default because it makes the file megabytes; the same data is in "
                             "the job directory and in --csv")
    parser.add_argument("--timeline", type=Path, metavar="FILE",
                        help="release history for the HTML trend "
                             "(default: results/timeline.csv in this checkout)")
    parser.add_argument("--peers", action="store_true",
                        help="overlay non-Stella agents from the timeline. They are references, "
                             "never Stella's target, so they are off by default")
    args = parser.parse_args(argv)

    rows = collect(args.job_dir)
    if args.timeline_row:
        identity = dict(pair.split("=", 1) for pair in args.set)
        print(timeline_row(rows, **identity))
        return 0
    print(render(rows))
    if args.csv:
        for path in write_csv(rows, args.csv):
            print(f"wrote {path}")
    if args.html:
        from . import timeline
        from .htmlreport import render_html

        history = timeline.load(args.timeline)
        args.html.parent.mkdir(parents=True, exist_ok=True)
        args.html.write_text(
            render_html(rows, str(args.job_dir), history, args.peers, args.detail))
        print(f"wrote {args.html}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
