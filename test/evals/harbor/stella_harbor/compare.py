"""Compare a candidate loop run against a reference run.

This is the loop comparator described in ../PROTOCOL.md. The protocol is the
authority for every rule below; where a docstring paraphrases it, the protocol
wins. Two things it insists on and this module enforces literally: the
candidate must cover every task its reference declares (no silent
intersection), and a task is judged only when both sides hold exactly k
scoreable trials.

    python -m stella_harbor.compare dist/evals/jobs/candidate dist/evals/jobs/reference
    python -m stella_harbor.compare CAND REF --confirm      # single-task k=5

Only CONFIRMED_REGRESSION exits nonzero. Everything else reports.
"""

from __future__ import annotations

import argparse
import json
import sys
from collections import defaultdict
from datetime import datetime
from pathlib import Path
from typing import Any

from .fingerprint import (
    FingerprintMismatchError,
    collect_fingerprint_details,
    comparison_mode,
    fingerprint_mismatches,
    format_mismatches,
)
from .report import RESOLVED, _command_outcomes, wilson_interval

# PROTOCOL.md "Thresholds and calibration": 25% paired per-task delta, either
# direction. A starting value the pilot recalibrates, recorded there.
EFFICIENCY_THRESHOLD = 0.25

# PROTOCOL.md "Sequential A/B discipline": one class per trial, first match
# wins, in this order.
TIMEOUT_CLASSES = ("harness_timeout", "agent_deadline", "command_timeout", "none")


def latest_run(job_dir: Path) -> Path:
    """Accept either a job root or one timestamped run inside it."""
    if (job_dir / "result.json").exists():
        return job_dir
    runs = sorted(p for p in job_dir.iterdir() if p.is_dir() and (p / "result.json").exists())
    if not runs:
        raise SystemExit(f"no completed run under {job_dir}")
    return runs[-1]


def _timeout_class(harbor: dict[str, Any], adapter: dict[str, Any]) -> str:
    """Classify one trial, first matching rule wins (PROTOCOL.md).

    `harness_timeout` reads Harbor's own exception: its timeout types are
    AgentTimeoutError, AgentSetupTimeoutError and VerifierTimeoutError, which
    is what "names an agent or verifier timeout" denotes. An environment-start
    timeout is neither, and a trial that died there carries no agent evidence
    to compare anyway. `command_timeout` reads the killed-command sentinel,
    exit code -1, which survives structurally in the bridge ledger rather than
    in the message text.
    """
    exception = harbor.get("exception_info") or {}
    if isinstance(exception, dict):
        kind = f"{exception.get('exception_type') or ''} {exception.get('exception_message') or ''}".lower()
        if "timeout" in kind and ("agent" in kind or "verifier" in kind):
            return "harness_timeout"
    if adapter.get("timed_out"):
        return "agent_deadline"
    _, killed = _command_outcomes(adapter.get("metrics") or {}, adapter.get("bridge_ledger") or [])
    if killed:
        return "command_timeout"
    return "none"


def _wall_ms(harbor: dict[str, Any], adapter: dict[str, Any]) -> float | None:
    timing = ((adapter.get("metrics") or {}).get("timing_ms") or {}).get("total")
    if timing is not None:
        return float(timing)
    started, finished = harbor.get("started_at"), harbor.get("finished_at")
    if isinstance(started, str) and isinstance(finished, str):
        try:
            return (datetime.fromisoformat(finished) - datetime.fromisoformat(started)).total_seconds() * 1000
        except ValueError:
            return None
    return None


def load(job_dirs: Path | list[Path]) -> list[dict[str, Any]]:
    """Read one row per trial from one or more job directories.

    Several job directories per side is not a default-to-latest shortcut: a
    side's k trials can be split across top-up jobs, and the caller still names
    every path explicitly, which is what the protocol requires.
    """
    if isinstance(job_dirs, Path):
        job_dirs = [job_dirs]
    rows: list[dict[str, Any]] = []
    for job_dir in job_dirs:
        run = latest_run(job_dir)
        for trial in sorted(p for p in run.iterdir() if p.is_dir()):
            result = trial / "result.json"
            if not result.exists():
                continue
            data = json.loads(result.read_text())
            agent = data.get("agent_result") or {}
            adapter_path = trial / "agent" / "stella" / "result.json"
            adapter = json.loads(adapter_path.read_text()) if adapter_path.exists() else {}
            rows.append(_row(trial, data, agent, adapter))
    return rows


def _row(trial: Path, data: dict[str, Any], agent: dict[str, Any], adapter: dict[str, Any]) -> dict[str, Any]:
    metrics = adapter.get("metrics") or {}
    usage = metrics.get("usage") or {}
    reward = ((data.get("verifier_result") or {}).get("rewards") or {}).get("reward")
    # PROTOCOL.md "Definitions": when adapter evidence is present its verdict is
    # final, even against a verifier reward. The reward fallback is only for
    # trials that carry no adapter evidence at all, such as pi.
    valid = bool(adapter.get("valid")) if adapter else reward is not None
    tools = metrics.get("tools") or {}
    return {
        "task": trial.name.rsplit("__", 1)[0],
        "trial": trial.name,
        "reward": reward,
        "valid": valid,
        # A valid trial with no reward means the verifier's infrastructure
        # failed. That is not an agent failure, so it never counts as unresolved.
        "scoreable": valid and reward is not None,
        "resolved": valid and reward is not None and reward >= RESOLVED,
        "cost_usd": agent.get("cost_usd") if agent.get("cost_usd") is not None else usage.get("cost_usd"),
        "input_tokens": agent.get("n_input_tokens") if agent.get("n_input_tokens") is not None else usage.get("input_tokens"),
        "output_tokens": agent.get("n_output_tokens") if agent.get("n_output_tokens") is not None else usage.get("output_tokens"),
        "turns": metrics.get("turns"),
        "tool_calls": metrics.get("tool_call_total"),
        "tool_errors": metrics.get("tool_error_total"),
        "errors_by_tool": {name: stat.get("errors", 0) for name, stat in tools.items()} if tools else None,
        # PROTOCOL.md tier 1: "Error counts are trustworthy only after #1077."
        # A trial that never split the counters folded nonzero command exits
        # into its error count, so the number exists but must not be judged.
        "errors_trusted": metrics.get("command_nonzero_total") is not None,
        "wall_ms": _wall_ms(data, adapter),
        "timeout_class": _timeout_class(data, adapter),
        # Absent adapter means an agent that carries no evidence contract;
        # that is not the same as a trial that failed one.
        "adapter": bool(adapter),
    }


def summarize(rows: list[dict[str, Any]]) -> dict[str, Any]:
    scoreable = [r for r in rows if r["scoreable"]]
    resolved = [r for r in scoreable if r["resolved"]]
    costs = [r["cost_usd"] for r in rows if r["cost_usd"] is not None]
    low, high = wilson_interval(len(resolved), len(scoreable))
    return {
        "trials": len(rows),
        "invalid": sum(1 for r in rows if not r["valid"]),
        "scoreable": len(scoreable),
        "resolved": len(resolved),
        "rate": len(resolved) / len(scoreable) if scoreable else 0.0,
        "ci": (low, high),
        "cost": sum(costs) if costs else None,
    }


def by_task(rows: list[dict[str, Any]]) -> dict[str, list[dict[str, Any]]]:
    grouped: dict[str, list[dict[str, Any]]] = defaultdict(list)
    for row in rows:
        grouped[row["task"]].append(row)
    return dict(grouped)


def _mean(values: list[Any]) -> float | None:
    present = [float(v) for v in values if v is not None]
    return sum(present) / len(present) if present else None


def _tool_names(trials: list[dict[str, Any]]) -> set[str]:
    return {name for t in trials if t["errors_by_tool"] for name in t["errors_by_tool"]}


def efficiency(candidate: list[dict[str, Any]], reference: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """The two frozen EFFICIENCY_SIGNAL metrics for one task.

    PROTOCOL.md "Process metrics": provider-reported cost and per-tool error
    counts, nothing else. Per task and side, the mean over *valid* trials; the
    delta is (candidate - reference) / reference; a reference mean of zero or a
    missing value leaves that metric unjudged.
    """
    cand_valid = [t for t in candidate if t["valid"]]
    ref_valid = [t for t in reference if t["valid"]]
    metrics: list[dict[str, Any]] = [{
        "metric": "cost",
        "candidate": _mean([t["cost_usd"] for t in cand_valid]),
        "reference": _mean([t["cost_usd"] for t in ref_valid]),
        "trusted": True,
    }]
    for name in sorted(_tool_names(cand_valid) | _tool_names(ref_valid)):
        def errors(trials: list[dict[str, Any]]) -> list[Any]:
            return [t["errors_by_tool"].get(name, 0) if t["errors_by_tool"] else None for t in trials]

        metrics.append({
            "metric": f"errors:{name}",
            "candidate": _mean(errors(cand_valid)),
            "reference": _mean(errors(ref_valid)),
            # Pre-#1077 counts exist but fold command exits in; display, never judge.
            "trusted": all(t["errors_trusted"] for t in cand_valid + ref_valid if t["errors_by_tool"]),
        })
    for metric in metrics:
        ref, cand = metric["reference"], metric["candidate"]
        unjudged = ref in (None, 0) or cand is None or not metric["trusted"]
        metric["delta"] = None if unjudged else (cand - ref) / ref
    return metrics


def _timeout_counts(trials: list[dict[str, Any]]) -> dict[str, int]:
    counts = dict.fromkeys(TIMEOUT_CLASSES, 0)
    for trial in trials:
        counts[trial["timeout_class"]] += 1
    return counts


def _timeout_flip(candidate: list[dict[str, Any]], reference: list[dict[str, Any]], delta: int) -> bool:
    """Whether a task's only outcome change is a timeout-class flip.

    PROTOCOL.md "Sequential A/B discipline" marks such a delta untrusted rather
    than judging it. Trials are not paired one to one inside a task, so this
    reads the counts: the classes moved, and the movement in timed-out trials
    is large enough to account for the whole outcome change.
    """
    if delta == 0:
        return False
    cand, ref = _timeout_counts(candidate), _timeout_counts(reference)
    if cand == ref:
        return False
    timed_out = abs((sum(cand.values()) - cand["none"]) - (sum(ref.values()) - ref["none"]))
    return timed_out >= abs(delta)


def task_verdicts(
    candidate: list[dict[str, Any]],
    reference: list[dict[str, Any]],
    k: int | None,
) -> list[dict[str, Any]]:
    """Pair every task, judging only where both sides hold exactly k scoreable."""
    cand_tasks, ref_tasks = by_task(candidate), by_task(reference)
    rows: list[dict[str, Any]] = []
    for task in sorted(set(cand_tasks) | set(ref_tasks)):
        cand, ref = cand_tasks.get(task, []), ref_tasks.get(task, [])
        cand_scoreable = sum(1 for t in cand if t["scoreable"])
        ref_scoreable = sum(1 for t in ref if t["scoreable"])
        cand_resolved = sum(1 for t in cand if t["resolved"])
        ref_resolved = sum(1 for t in ref if t["resolved"])
        judged = k is not None and cand_scoreable == k and ref_scoreable == k
        delta = cand_resolved - ref_resolved
        untrusted = judged and _timeout_flip(cand, ref, delta)
        rows.append({
            "task": task,
            "candidate_scoreable": cand_scoreable,
            "reference_scoreable": ref_scoreable,
            "candidate_resolved": cand_resolved,
            "reference_resolved": ref_resolved,
            "delta": delta,
            "judged": judged,
            # PROTOCOL.md: a guard is any task the reference resolved k of k.
            "guard": judged and ref_resolved == k,
            "untrusted": untrusted,
            "timeout_classes": (_timeout_counts(cand), _timeout_counts(ref)),
            "efficiency": efficiency(cand, ref),
            "process": _process_metrics(cand, ref),
        })
    return rows


def _process_metrics(candidate: list[dict[str, Any]], reference: list[dict[str, Any]]) -> dict[str, Any]:
    """Paired means for the three trust tiers. All are displayed."""
    cand_valid = [t for t in candidate if t["valid"]]
    ref_valid = [t for t in reference if t["valid"]]

    def pair(field: str) -> tuple[float | None, float | None]:
        return _mean([t[field] for t in cand_valid]), _mean([t[field] for t in ref_valid])

    return {
        "behavioral": {
            "tool_calls": pair("tool_calls"),
            "tool_errors": pair("tool_errors"),
            "turns": pair("turns"),
        },
        "gateway": {
            "input_tokens": pair("input_tokens"),
            "output_tokens": pair("output_tokens"),
            "cost_usd": pair("cost_usd"),
        },
        # Wall time is reported and never judged: an emulating host makes it noise.
        "wall": {"wall_ms": pair("wall_ms")},
        "errors_trusted": all(t["errors_trusted"] for t in cand_valid + ref_valid if t["errors_by_tool"]),
    }


def efficiency_signals(row: dict[str, Any]) -> list[dict[str, Any]]:
    """Judged efficiency metrics past the threshold, on an unchanged resolved count."""
    if not row["judged"] or row["delta"] != 0:
        return []
    return [m for m in row["efficiency"] if m["delta"] is not None and abs(m["delta"]) > EFFICIENCY_THRESHOLD]


def verdicts(rows: list[dict[str, Any]], k: int | None) -> dict[str, Any]:
    judged = [r for r in rows if r["judged"] and not r["untrusted"]]
    suspected = [r for r in judged if (r["guard"] and r["candidate_resolved"] < k) or r["delta"] <= -2]
    signal = [r for r in judged if r["delta"] != 0]
    return {
        "signal": signal,
        "suspected_regression": suspected,
        "insufficient": [r for r in rows if not r["judged"]],
        "untrusted": [r for r in rows if r["untrusted"]],
        "efficiency": [(r, efficiency_signals(r)) for r in rows if efficiency_signals(r)],
    }


def confirm(rows: list[dict[str, Any]]) -> dict[str, Any]:
    """The frozen single-task k=5 confirmation predicates."""
    judged = [r for r in rows if r["judged"]]
    if len(judged) != 1:
        return {"verdict": "INSUFFICIENT_EVIDENCE", "row": judged[0] if len(judged) == 1 else None}
    row = judged[0]
    if row["delta"] <= -2:
        verdict = "CONFIRMED_REGRESSION"
    elif row["delta"] >= 2:
        verdict = "CONFIRMED_IMPROVEMENT"
    else:
        verdict = "DISMISSED"
    return {"verdict": verdict, "row": row}


def _pct(value: float | None) -> str:
    return "-" if value is None else f"{value * 100:+.1f}%"


def _num(value: float | None, digits: int = 1) -> str:
    return "-" if value is None else f"{value:.{digits}f}"


def _pair(values: tuple[float | None, float | None], digits: int = 1) -> str:
    return f"{_num(values[0], digits)} / {_num(values[1], digits)}"


def render(
    candidate: list[dict[str, Any]],
    reference: list[dict[str, Any]],
    names: tuple[str, str],
    *,
    k: int | None = None,
    mismatches: list[dict[str, Any]] | None = None,
    mode: str | None = None,
    agent_names: tuple[Any, Any] | None = None,
    subset: list[str] | None = None,
) -> str:
    rows = task_verdicts(candidate, reference, k)
    result = verdicts(rows, k)
    issues = mismatches or []
    untrusted = any(issue.get("reject") for issue in issues)
    marker = "[UNTRUSTWORTHY COMPARISON] "

    def mark(line: str) -> str:
        return marker + line if untrusted else line

    out = [mark(f"candidate {names[0]}  vs  reference {names[1]}"), ""]
    if mode:
        if mode == "cross-agent":
            cand_agent, ref_agent = agent_names or (None, None)
            identity = f"CROSS-AGENT COMPARISON: candidate agent={cand_agent!r}; reference agent={ref_agent!r}"
        else:
            identity = f"{mode.upper()}: agent identity is part of the report, not the run-condition gate"
        out.append(mark(identity))
    if issues:
        out.extend(mark(line) for line in [
            "Fingerprint validation failed; this output must not be used to attribute score changes."
            if untrusted else "Agent identity diagnostics; run-condition validation passed.",
            *format_mismatches(issues),
        ])
        out.append("")

    if subset is not None:
        out.append(mark(
            f"EXPLICIT SUBSET (--tasks): {len(subset)} task(s) compared — {', '.join(subset)}. "
            "The task-set dimension of the fingerprint is relaxed for this run; "
            "model and dataset are not."))
    else:
        out.append(mark(f"task set: all {len({r['task'] for r in reference})} task(s) the reference declares"))
    out.append(mark(
        f"k = {k} (both sides must hold exactly k scoreable trials for a task to be judged)"
        if k is not None else
        "k is unknown: no run recorded a budget, so no task can be judged. "
        "Pass --k to state it."))
    out.append("")

    width = max([24, *(len(r["task"]) for r in rows)]) if rows else 24
    out.append(mark(f"{'task':<{width}}  {'cand':>7}  {'ref':>7}  {'Δres':>5}  {'Δcost':>8}  {'Δerrs':>8}  notes"))
    out.append(mark("-" * (width + 48)))
    for row in rows:
        cost = next((m for m in row["efficiency"] if m["metric"] == "cost"), None)
        errs = [m for m in row["efficiency"] if m["metric"].startswith("errors:") and m["delta"] is not None]
        worst = max(errs, key=lambda m: abs(m["delta"])) if errs else None
        notes = []
        if not row["judged"]:
            notes.append("INSUFFICIENT_EVIDENCE")
        if row["untrusted"]:
            notes.append("UNTRUSTED (timeout-class flip)")
        if row in result["suspected_regression"]:
            notes.append("SUSPECTED_REGRESSION")
        elif row in result["signal"]:
            notes.append("SIGNAL")
        if row["guard"]:
            notes.append("guard")
        if efficiency_signals(row):
            notes.append("EFFICIENCY_SIGNAL")
        out.append(mark(
            f"{row['task']:<{width}}  "
            f"{f'{row["candidate_resolved"]}/{row["candidate_scoreable"]}':>7}  "
            f"{f'{row["reference_resolved"]}/{row["reference_scoreable"]}':>7}  "
            f"{row['delta']:+5d}  {_pct(cost['delta'] if cost else None):>8}  "
            f"{_pct(worst['delta'] if worst else None):>8}  {', '.join(notes)}"))

    out.append("")
    out.append(mark("Process metrics — paired means over valid trials, three trust tiers."))
    out.append(mark(f"{'task':<{width}}  {'calls c/r':>13}  {'errs c/r':>13}  {'turns c/r':>13}  "
                    f"{'in.tok c/r':>17}  {'cost c/r':>17}  {'wall s c/r':>15}"))
    out.append(mark("-" * (width + 96)))
    for row in rows:
        p = row["process"]
        errors = _pair(p["behavioral"]["tool_errors"])
        if not p["errors_trusted"]:
            errors += "*"
        wall = tuple(None if v is None else v / 1000 for v in p["wall"]["wall_ms"])
        out.append(mark(
            f"{row['task']:<{width}}  {_pair(p['behavioral']['tool_calls']):>13}  {errors:>13}  "
            f"{_pair(p['behavioral']['turns']):>13}  {_pair(p['gateway']['input_tokens'], 0):>17}  "
            f"{_pair(p['gateway']['cost_usd'], 4):>17}  {_pair(wall):>15}"))
    if any(not r["process"]["errors_trusted"] for r in rows):
        out.append(mark("* error counts predate #1077: they fold nonzero command exits in, so they are "
                        "displayed and never judged."))
    out.append(mark("Wall time is displayed and never judged: an emulating host makes it noise."))

    out.append("")
    for name, trials in ((names[0], candidate), (names[1], reference)):
        s = summarize(trials)
        cost = "-" if s["cost"] is None else f"${s['cost']:.2f}"
        invalid = f", {s['invalid']} invalid" if s["invalid"] else ""
        out.append(mark(
            f"{name}: {s['resolved']}/{s['scoreable']} resolved "
            f"({s['rate'] * 100:.1f}%, 95% CI {s['ci'][0] * 100:.1f}-{s['ci'][1] * 100:.1f}%), "
            f"total {cost}{invalid}"
        ))

    out.append("")
    out.extend(mark(line) for line in _verdict_lines(result))
    out.append("")
    out.append(mark("Rates are per task and paired; loop trial counts are far too small for a headline rate."))
    out.append(mark("Costs come from each agent's own usage reporting, so they are comparable"))
    out.append(mark("only when both runs used the same model and price table."))
    return "\n".join(out)


def _verdict_lines(result: dict[str, Any]) -> list[str]:
    lines: list[str] = []
    for row in result["insufficient"]:
        lines.append(
            f"INSUFFICIENT_EVIDENCE {row['task']}: candidate {row['candidate_scoreable']} scoreable, "
            f"reference {row['reference_scoreable']}; excluded from every verdict")
    for row in result["untrusted"]:
        cand, ref = row["timeout_classes"]
        lines.append(
            f"UNTRUSTED {row['task']}: the only outcome change is a timeout-class flip "
            f"(candidate {cand}, reference {ref}); not judged")
    for row in result["suspected_regression"]:
        why = "a guard dropped below k/k" if row["guard"] else f"down {abs(row['delta'])} resolved"
        lines.append(
            f"SUSPECTED_REGRESSION {row['task']}: {row['candidate_resolved']} vs "
            f"{row['reference_resolved']} resolved ({why}); reported loudly, does not gate")
    for row in result["signal"]:
        lines.append(
            f"SIGNAL {row['task']}: {row['candidate_resolved']} vs {row['reference_resolved']} resolved "
            f"({row['delta']:+d}); reported, never gates")
    for row, metrics in result["efficiency"]:
        detail = "; ".join(f"{m['metric']} {_pct(m['delta'])}" for m in metrics)
        lines.append(
            f"EFFICIENCY_SIGNAL {row['task']}: resolved unchanged at {row['candidate_resolved']}, {detail}; "
            "shown, not a gate — direction alone proves nothing")
    if not lines:
        lines.append("No movement: every judged task holds its resolved count.")
    return lines


def _missing_tasks(candidate: list[dict[str, Any]], reference: list[dict[str, Any]]) -> list[str]:
    return sorted({r["task"] for r in reference} - {r["task"] for r in candidate})


def _select(rows: list[dict[str, Any]], tasks: list[str] | None) -> list[dict[str, Any]]:
    return rows if tasks is None else [r for r in rows if r["task"] in tasks]


def _budget(details: dict[str, Any]) -> int | None:
    value = details["fingerprint"].get("budget")
    return value if isinstance(value, int) else None


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Compare a candidate Harbor job against a reference.")
    parser.add_argument("candidate", type=Path)
    parser.add_argument("reference", type=Path)
    parser.add_argument("--candidate-job", type=Path, action="append", default=[],
                        help="additional job directory belonging to the candidate side")
    parser.add_argument("--reference-job", type=Path, action="append", default=[],
                        help="additional job directory belonging to the reference side")
    parser.add_argument("--names", nargs=2, metavar=("CANDIDATE", "REFERENCE"))
    parser.add_argument("--tasks", help="comma-separated explicit subset; echoed in the output")
    parser.add_argument("--k", type=int, help="scoreable trials per side required to judge a task")
    parser.add_argument("--confirm", action="store_true",
                        help="apply the frozen single-task k=5 confirmation predicates")
    parser.add_argument(
        "--allow-mismatch",
        action="store_true",
        help="render an explicitly untrusted comparison despite fingerprint mismatches",
    )
    args = parser.parse_args(argv)
    names = tuple(args.names) if args.names else (args.candidate.name, args.reference.name)
    candidate_jobs = [args.candidate, *args.candidate_job]
    reference_jobs = [args.reference, *args.reference_job]

    candidate_details = collect_fingerprint_details(args.candidate)
    reference_details = collect_fingerprint_details(args.reference)
    candidate_fingerprint = candidate_details["fingerprint"]
    reference_fingerprint = reference_details["fingerprint"]
    mismatches = fingerprint_mismatches(
        candidate_fingerprint,
        reference_fingerprint,
        candidate_details["evidence"],
        reference_details["evidence"],
    )
    blocking = [issue for issue in mismatches if issue.get("reject")]
    mode = comparison_mode(
        candidate_fingerprint,
        reference_fingerprint,
        (candidate_details["evidence"], reference_details["evidence"]),
    )
    if blocking and not args.allow_mismatch:
        print(str(FingerprintMismatchError(mismatches)), file=sys.stderr)
        return 2

    candidate_rows, reference_rows = load(candidate_jobs), load(reference_jobs)
    subset = [t.strip() for t in args.tasks.split(",") if t.strip()] if args.tasks else None
    if subset is not None:
        candidate_rows, reference_rows = _select(candidate_rows, subset), _select(reference_rows, subset)
        unknown = sorted(set(subset) - {r["task"] for r in reference_rows})
        if unknown:
            print("REFUSING COMPARISON: --tasks names task(s) the reference never ran: "
                  + ", ".join(unknown), file=sys.stderr)
            return 2

    # No silent intersection: a candidate missing any task its reference
    # declares is refused by name, subset flag or not.
    missing = _missing_tasks(candidate_rows, reference_rows)
    if missing:
        print("REFUSING COMPARISON: the candidate is missing task(s) the reference declares: "
              + ", ".join(missing)
              + "\nRe-run those tasks, or select an explicit subset with --tasks.", file=sys.stderr)
        return 2

    budgets = {_budget(candidate_details), _budget(reference_details)}
    k = args.k if args.k is not None else (budgets.pop() if len(budgets) == 1 else None)

    if args.confirm and k != 5:
        print(f"REFUSING CONFIRMATION: confirmation is a single-task k=5 run on both sides; k={k}.",
              file=sys.stderr)
        return 2
    if args.confirm and len({r["task"] for r in reference_rows}) != 1:
        print("REFUSING CONFIRMATION: confirmation is a single-task run; select one task with --tasks.",
              file=sys.stderr)
        return 2

    print(render(
        candidate_rows,
        reference_rows,
        names,
        k=k,
        mismatches=mismatches or None,
        mode=mode,
        agent_names=(candidate_fingerprint.get("agent_name"), reference_fingerprint.get("agent_name")),
        subset=subset,
    ))

    if args.confirm:
        outcome = confirm(task_verdicts(candidate_rows, reference_rows, k))
        row = outcome["row"]
        if row is None:
            print("\nINSUFFICIENT_EVIDENCE: the task is not judged at k=5 on both sides; "
                  "re-run the short side.")
            return 0
        counts = f"candidate {row['candidate_resolved']}/5 vs reference {row['reference_resolved']}/5"
        print(f"\n{outcome['verdict']}: {counts}")
        return 1 if outcome["verdict"] == "CONFIRMED_REGRESSION" else 0
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
