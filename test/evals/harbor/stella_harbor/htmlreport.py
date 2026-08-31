"""Render a job's metrics as one self-contained HTML file.

The text report is for a terminal; this is for reading. Everything is inlined
(no CSS or JS fetched at open time) so the file can be attached to an issue or
copied to another machine and still render.

The report opens on the question a reviewer actually has, "did Stella get
better", so the headline is this job read against Stella's own archived
releases. Other agents are peer references and stay off unless asked for: a
rival's line next to ours reads as a target, and Stella's release KPI is its
own trend.
"""

from __future__ import annotations

import html
from datetime import datetime, timezone
from itertools import pairwise
from typing import Any

from . import timeline
from .report import _int, _usd, reliability
from .taxonomy import breakdown

# Phase colours for the timing bar. model/tool are the two that matter; the
# harness phases share one colour because their job is to stay invisible.
PHASE_COLORS = {"model": "#4c8bf5", "tool": "#e8873a", "other": "#8a8f98"}

# Every column a reader has to interpret carries its explanation, because a
# report nobody can read is a report nobody checks.
HEADER_HELP = {
    "task": "the benchmark task; one row per attempt, so a task repeats",
    "reward": "the task's own grader. 1.00 is fully solved; anything less is not",
    "valid": "did we prove the agent worked inside the trial container. NO means the "
             "attempt is excluded from the score entirely, as neither pass nor fail",
    "state": "how the turn ended",
    "wall": "total time for the attempt",
    "model": "time the model spent thinking between messages",
    "tool": "time tool calls took, measured from the message timeline, so it includes "
            "Stella's own dispatch overhead",
    "bridge": "time actually spent executing inside the container. wall minus this is "
              "overhead outside the task",
    "turns": "how many times the model replied",
    "orch": "provider-visible orchestration calls, including Code Mode's outer code call",
    "exec": "comparable execution calls, read from the trusted bridge ledger",
    "errs": "how many of those tool calls failed",
    "cmd!0": "commands that ran and exited nonzero — the container answering, not a tool failing; "
             "a dash means this trial never measured the field: a Stella run archived before the "
             "split, or an agent that writes no adapter metrics. It never means zero",
    "in.tok": "prompt tokens, as reported by the provider",
    "out.tok": "completion tokens, as reported by the provider",
    "cost": "USD, priced at the model's configured rate. A dash means the provider "
            "reported no usage or the model has no price; it never means free",
    "trials": "attempts at this task",
    "scoreable": "attempts that produced usable evidence",
    "resolved": "attempts that earned full reward",
    "pass^k": "did every scoreable attempt resolve. One lucky pass out of five is not "
              "a task the agent can do",
}


def _esc(value: Any) -> str:
    return html.escape("" if value is None else str(value))


def _secs(ms: Any) -> str:
    try:
        return f"{int(ms) / 1000:.1f}s"
    except (TypeError, ValueError):
        return "-"


def _table(headers: list[str], rows: list[list[str]], cls: str = "") -> str:
    if not rows:
        return ""
    head = "".join(
        f'<th title="{_esc(HEADER_HELP[h])}"><abbr>{_esc(h)}</abbr></th>' if h in HEADER_HELP
        else f"<th>{_esc(h)}</th>" for h in headers)
    body = "".join("<tr>" + "".join(f"<td>{c}</td>" for c in r) + "</tr>" for r in rows)
    return f'<table class="{cls}"><thead><tr>{head}</tr></thead><tbody>{body}</tbody></table>'


def _timing_bar(timing: dict[str, Any]) -> str:
    total = int(timing.get("total") or 0)
    if total <= 0:
        return ""
    model = int(timing.get("model") or 0)
    tool = int(timing.get("tool") or 0)
    other = max(0, total - model - tool)
    segments = []
    for label, value in (("model", model), ("tool", tool), ("other", other)):
        if value <= 0:
            continue
        pct = value / total * 100
        segments.append(
            f'<span class="seg" style="width:{pct:.2f}%;background:{PHASE_COLORS[label]}" '
            f'title="{label} {_secs(value)} ({pct:.0f}%)"></span>')
    legend = " ".join(
        f'<span class="key"><i style="background:{PHASE_COLORS[l]}"></i>{l} {_secs(v)}</span>'
        for l, v in (("model", model), ("tool", tool), ("other", other)) if v > 0)
    return f'<div class="bar">{"".join(segments)}</div><div class="legend">{legend}</div>'


def _trial_detail(row: dict[str, Any]) -> str:
    metrics = row.get("metrics") or {}
    timing = metrics.get("timing_ms") or {}
    tokens = metrics.get("tokens_estimated") or {}
    bridge = metrics.get("bridge") or {}
    verdict = {True: "ok", False: "bad", None: ""}[row.get("valid")]
    verdict_text = {True: "valid", False: "INVALID", None: "no verdict"}[row.get("valid")]
    reward = "-" if row.get("reward") is None else format(row["reward"], ".2f")
    summary = (f'<summary><b>{_esc(row["task"])}</b> '
               f'<span class="pill {verdict}">{verdict_text}</span> '
               f'<span class="pill">reward {reward}</span> '
               f'<span class="dim">{_esc(row.get("state"))} · {_secs(timing.get("total"))}</span></summary>')

    parts = [_timing_bar(timing)]

    phases = [[k, _secs(v)] for k, v in timing.items() if k not in {"total", "model", "tool"}]
    parts.append("<h4>phases</h4>" + _table(["phase", "time"], phases))

    tools = [[_esc(n), str(s.get("calls", 0)),
              f'<span class="{"bad-text" if s.get("errors") else ""}">{s.get("errors", 0)}</span>',
              _secs(s.get("total_ms")), _secs(s.get("max_ms"))]
             for n, s in sorted((metrics.get("tools") or {}).items(),
                                key=lambda kv: -kv[1].get("total_ms", 0))]
    parts.append("<h4>tools</h4>" + _table(["tool", "calls", "errors", "total", "slowest"], tools))

    ops = [[_esc(n), str(s.get("calls", 0)),
            f'<span class="{"bad-text" if s.get("failures") else ""}">{s.get("failures", 0)}</span>',
            _secs(s.get("total_ms")), _secs(s.get("max_ms"))]
           for n, s in sorted((bridge.get("operations") or {}).items(),
                              key=lambda kv: -kv[1].get("total_ms", 0))]
    parts.append(f'<h4>bridge operations <span class="dim">in-container, '
                 f'{_secs(bridge.get("total_ms"))} total</span></h4>'
                 + _table(["op", "calls", "failures", "total", "slowest"], ops))

    u = metrics.get("usage") or {}
    if u:
        parts.append("<h4>provider-reported usage</h4>" + _table(
            ["metric", "value"],
            [["input", _int(u.get("input_tokens"))], ["output", _int(u.get("output_tokens"))],
             ["cache read", _int(u.get("cache_read_tokens"))],
             ["cache write", _int(u.get("cache_write_tokens"))],
             ["cost", _usd(u.get("cost_usd"))],
             ["calls (reported / priced)",
              f'{u.get("call_count", 0)} ({_int(u.get("reported_call_count"))} / '
              f'{_int(u.get("priced_call_count"))})']]))
    if tokens:
        parts.append('<h4>estimated tokens <span class="dim">len/4, not usage</span></h4>' + _table(
            ["scope", "est.tok"], [[k, str(v)] for k, v in tokens.items()]))

    ledger = row.get("ledger") or []
    if ledger:
        entries = [[str(e.get("seq")), _esc(e.get("op")),
                    _esc(e.get("path") or e.get("command") or ""),
                    ("<span class='ok-text'>ok</span>" if e.get("ok")
                     else f"<span class='bad-text'>{_esc(e.get('code'))}</span>"),
                    _secs(e.get("elapsed_ms"))] for e in ledger]
        parts.append(f"<h4>bridge ledger <span class='dim'>{len(ledger)} calls</span></h4>"
                     + _table(["#", "op", "target", "result", "time"], entries, cls="ledger"))

    if row.get("violations"):
        items = "".join(f"<li>{_esc(v)}</li>" for v in row["violations"])
        parts.append(f'<h4 class="bad-text">predicate violations</h4><ul>{items}</ul>')

    return f'<details>{summary}<div class="detail">{"".join(parts)}</div></details>'


def _pct(value: Any, digits: int = 1) -> str:
    return "-" if value is None else f"{value * 100:.{digits}f}%"


def _card(label: str, value: str, note: str, tone: str = "") -> str:
    return (f'<div class="card"><div class="card-label">{_esc(label)}</div>'
            f'<div class="card-value {tone}">{value}</div>'
            f'<div class="card-note">{note}</div></div>')


def _trend_chart(runs: list[dict[str, Any]], peers: bool) -> str:
    """Stella's resolution over its own releases, peers only on request.

    A line between two points is a claim that they measure the same thing, so a
    segment whose endpoints disagree on benchmark, model, k, harness or host is
    drawn dashed. The reader should be able to see an incomparable jump without
    reading the caption.
    """
    subject = [r for r in runs if timeline.is_subject(r) and r.get("resolution") is not None]
    if len(subject) < 2:
        return ""

    plotted = timeline.select(runs, peers)
    values = [r["resolution"] for r in plotted if r.get("resolution") is not None]
    lo = max(0.0, min(values) - 0.08)
    hi = min(1.0, max(values) + 0.08)
    span = hi - lo or 1.0
    W, H, PAD_L, PAD_R, PAD_T, PAD_B = 680, 240, 52, 22, 18, 34

    dates = sorted({str(r["date"]) for r in plotted})
    # Points sit inset from the axis so the first and last value labels have
    # room; a label clipped at the edge is the one a reader most wants.
    INSET = 34
    left, right = PAD_L + INSET, W - PAD_R - INSET

    def x(run: dict[str, Any]) -> float:
        if len(dates) == 1:
            return (left + right) / 2
        return left + dates.index(str(run["date"])) * (right - left) / (len(dates) - 1)

    def y(value: float) -> float:
        return PAD_T + (hi - value) / span * (H - PAD_T - PAD_B)

    grid, labels = [], []
    step = 0.05 if span <= 0.25 else 0.1
    tick = (int(lo / step) + 1) * step
    while tick < hi:
        gy = y(tick)
        grid.append(f'<path d="M{PAD_L} {gy:.1f}H{W - PAD_R}"/>')
        labels.append(f'<text x="{PAD_L - 10}" y="{gy + 4:.1f}" text-anchor="end">{tick * 100:.0f}%</text>')
        tick += step

    segments = []
    for prev, cur in pairwise(subject):
        dash = "" if timeline.comparable(prev, cur) else ' stroke-dasharray="7 5"'
        segments.append(f'<path d="M{x(prev):.1f} {y(prev["resolution"]):.1f}L{x(cur):.1f} '
                        f'{y(cur["resolution"]):.1f}" class="trend"{dash}/>')
    for run in subject:
        segments.append(f'<circle cx="{x(run):.1f}" cy="{y(run["resolution"]):.1f}" r="5" class="trend-dot">'
                        f'<title>{_esc(run.get("label") or run["date"])}</title></circle>')
        segments.append(f'<text x="{x(run):.1f}" y="{y(run["resolution"]) - 13:.1f}" '
                        f'class="trend-value" text-anchor="middle">{_pct(run["resolution"])}</text>')

    peer_marks = []
    for run in plotted:
        if timeline.is_subject(run) or run.get("resolution") is None:
            continue
        peer_marks.append(f'<circle cx="{x(run):.1f}" cy="{y(run["resolution"]):.1f}" r="5" class="peer-dot">'
                          f'<title>{_esc(run.get("agent"))} {_pct(run["resolution"])}</title></circle>')

    axis_labels = "".join(
        f'<text x="{x({"date": d}):.1f}" y="{H - 10}" text-anchor="middle">{_esc(d)}</text>'
        for d in dates)

    incomparable = any(not timeline.comparable(a, b) for a, b in pairwise(subject))
    caption = ("A dashed segment joins two runs whose configuration differs. That movement is "
               "descriptive context, not evidence about a code change."
               if incomparable else
               "Every segment joins two runs measured the same way.")
    legend = ('<span class="key"><i class="trend-swatch"></i>Stella release</span>'
              + ('<span class="key"><i class="peer-swatch"></i>peer overlay</span>' if peer_marks else
                 '<span class="key dim">peer overlay off</span>'))

    return (f'<article class="panel"><h2>Stella resolution by release</h2>'
            f'<div class="legend">{legend}</div>'
            f'<svg class="chart" viewBox="0 0 {W} {H}" role="img" '
            f'aria-label="Stella resolution rate across archived releases">'
            f'<g class="grid">{"".join(grid)}</g>'
            f'<g class="axis">{"".join(labels)}{axis_labels}</g>'
            f'{"".join(segments)}{"".join(peer_marks)}</svg>'
            f'<p class="caption">{caption}</p></article>')


def _quality_panel(rows: list[dict[str, Any]], stats: dict[str, Any]) -> str:
    """The dimensions that decide whether the headline number is trustworthy."""
    total_calls = sum((r.get("execution_calls") or r.get("calls") or 0) for r in rows)
    tool_errors = sum((r["tool_errors"] or 0) for r in rows if r.get("tool_errors") is not None)
    split = [r for r in rows if r.get("command_nonzero_total") is not None]
    priced = [r for r in rows if (r.get("usage") or {}).get("cost_usd") is not None]
    cost = sum(r["usage"]["cost_usd"] for r in priced)
    faults = sum(len(r.get("adapter_faults") or []) for r in rows)

    items = [
        ("Selected timeouts", f'{stats["timeouts"]} / {stats["trials"]}', ""),
        ("Tool faults", f'{tool_errors} / {total_calls} calls', "ok-text" if not tool_errors else "bad-text"),
        ("Bridge adapter faults", str(faults), "ok-text" if not faults else "bad-text"),
        ("Command non-zero",
         (str(sum(r["command_nonzero_total"] for r in split))
          if len(split) == len(rows) else "-"), ""),
        ("Priced coverage", f'{len(priced)} / {len(rows)}' if rows else "-", ""),
        ("Cost / priced trial", f"${cost / len(priced):.4f}" if priced else "-", ""),
        ("Invalid, excluded", str(stats["invalid"]), "ok-text" if not stats["invalid"] else "warn-text"),
    ]
    body = "".join(f'<div class="stat"><span>{_esc(label)}</span>'
                   f'<b class="{tone}">{_esc(value)}</b></div>' for label, value, tone in items)
    return (f'<article class="panel"><h2>Is this number trustworthy</h2>{body}'
            f'<p class="caption">A dash is a field this job never measured. It never means zero.</p>'
            f'</article>')


def _headline_cards(stats: dict[str, Any], rows: list[dict[str, Any]],
                    history: list[dict[str, Any]]) -> str:
    if stats["resolution_rate"] is None:
        return _card("Release resolution", "no score", "no scoreable trials in this job", "bad-text")

    low, high = stats["ci95"]
    cards = [_card("Release resolution", _pct(stats["resolution_rate"]),
                   f'{stats["resolved"]} / {stats["scoreable"]} scoreable trials · '
                   f'Wilson {low * 100:.1f}–{high * 100:.1f}')]

    prior = timeline.previous_subject(history) if history else None
    latest = timeline.latest_subject(history) if history else None
    if prior and latest:
        delta = latest["resolution"] - prior["resolution"]
        kind = ("matched configuration" if timeline.comparable(prior, latest)
                else "descriptive: configuration changed")
        cards.append(_card("vs prior Stella release", f'{delta * 100:+.1f}pp',
                           f'{latest["resolved"] - prior["resolved"]:+d} resolved · {kind}',
                           "up" if delta > 0 else "warn-text" if delta < 0 else ""))
    else:
        cards.append(_card("vs prior Stella release", "-",
                           "no earlier Stella run in results/timeline.json"))

    if stats["k"] >= 2:
        prior_pass = prior.get("pass_k") if prior else None
        note = (f'{len(stats["tasks"])} tasks · every attempt resolved'
                if prior_pass is None else
                f'{len(stats["tasks"])} tasks · was {_pct(prior_pass)}')
        tone = "up" if prior_pass is not None and stats["pass_hat_k"] > prior_pass else ""
        cards.append(_card(f'Stability · pass^{stats["k"]}', _pct(stats["pass_hat_k"]), note, tone))
    else:
        cards.append(_card("Stability · pass^k", "-",
                           "needs at least 2 scoreable trials per task; run with -k 5"))

    healthy = not stats["invalid"] and not any(r.get("adapter_faults") for r in rows)
    cards.append(_card("Evidence health", f'{stats["scoreable"]} / {stats["trials"]}',
                       f'{len(stats["tasks"])} tasks · '
                       + ("every trial proved it ran in-container" if healthy
                          else "some trials carry no usable evidence"),
                       "up" if healthy else "warn-text"))
    return "".join(cards)


def render_html(rows: list[dict[str, Any]], job_dir: str = "",
                history: list[dict[str, Any]] | None = None, peers: bool = False) -> str:
    stats = reliability(rows)
    generated = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M UTC")
    history = history or []

    notes = []
    if stats["k"] < 2:
        notes.append("pass^k unavailable: a task has fewer than 2 scoreable trials (run with -k 5)")
    if stats["invalid"]:
        notes.append(f'{stats["invalid"]} trial(s) excluded as invalid: missing evidence, not failures')

    faults = [(r, f) for r in rows for f in (r.get("adapter_faults") or [])]
    fault_block = ""
    if faults:
        items = "".join(
            f'<li><b>{_esc(r["task"])}</b>: {_esc(f.get("op"))} '
            f'{_esc(f.get("path") or "")} → <code>{_esc(f.get("code"))}</code></li>'
            for r, f in faults)
        fault_block = (f'<section class="alert"><h2>{len(faults)} bridge adapter fault(s)</h2>'
                       f'<p>The harness broke, not the task. A capable agent routes around a broken '
                       f'tool and still scores, so reward hides these.</p><ul>{items}</ul></section>')

    failures = [b for b in breakdown(rows) if b["label"] not in {"resolved", "invalid"}]
    failure_rows = [[_esc(b["label"]), str(b["count"]), _esc(b["description"]),
                     f'<span class="dim">{_esc(b["example_task"])}: {_esc(b["example_reason"])}</span>']
                    for b in failures]

    trial_rows = [[
        _esc(r["task"]),
        "-" if r["reward"] is None else format(r["reward"], ".2f"),
        # A trial that raised has no verdict at all; calling that "NO" would
        # read as an evidence failure it never got far enough to have.
        {True: '<span class="pill ok">yes</span>', False: '<span class="pill bad">NO</span>',
         None: '<span class="pill">-</span>'}[r["valid"]],
        _esc(r["state"]), _secs(r["wall_ms"]), _secs(r["model_ms"]), _secs(r["tool_ms"]),
        _secs(r["bridge_ms"]), str(r["turns"] if r["turns"] is not None else "-"),
        str(r.get("orchestration_calls", r["calls"]) if r.get("orchestration_calls", r["calls"]) is not None else "-"),
        str(r.get("execution_calls") if r.get("execution_calls") is not None else "-"),
        f'<span class="{"bad-text" if r["tool_errors"] else ""}">{r["tool_errors"] if r["tool_errors"] is not None else "-"}</span>',
        str(r["command_nonzero_total"]) if r.get("command_nonzero_total") is not None else "-",
        _int((r.get("usage") or {}).get("input_tokens")),
        _int((r.get("usage") or {}).get("output_tokens")),
        _usd((r.get("usage") or {}).get("cost_usd")),
    ] for r in rows]

    task_rows = [[_esc(t["task"]), str(t["trials"]), str(t["scoreable"]), str(t["resolved"]),
                  "yes" if t["pass_hat_k"] else "no"] for t in stats["tasks"]]

    tools: dict[str, dict[str, int]] = {}
    for r in rows:
        for name, stat in (r.get("tools") or {}).items():
            agg = tools.setdefault(name, {"calls": 0, "errors": 0, "total_ms": 0, "max_ms": 0})
            agg["calls"] += stat.get("calls", 0)
            agg["errors"] += stat.get("errors", 0)
            agg["total_ms"] += stat.get("total_ms", 0)
            agg["max_ms"] = max(agg["max_ms"], stat.get("max_ms", 0))
    tool_rows = [[_esc(n), str(s["calls"]),
                  f'<span class="{"bad-text" if s["errors"] else ""}">{s["errors"]}</span>',
                  _secs(s["total_ms"]), _secs(s["max_ms"])]
                 for n, s in sorted(tools.items(), key=lambda kv: -kv[1]["total_ms"])]

    failure_block = ""
    if failure_rows:
        failure_block = ('<h2>Why trials failed <span class="dim">'
                         "a pass rate says how often, this says why</span></h2>"
                         + _table(["failure", "count", "meaning", "example"], failure_rows))

    history_rows = [[
        _esc(r.get("date")), _esc(r.get("agent")), _esc(r.get("label") or ""),
        _esc(r.get("harness") or "-"), _pct(r.get("resolution")), _pct(r.get("pass_k")),
        # A run total, not a per-trial price, so cents are the useful precision.
        "-" if r.get("cost_usd") is None else f'${r["cost_usd"]:.2f}',

    ] for r in reversed(timeline.select(history, peers))]
    history_block = ""
    if history_rows:
        history_block = ('<h2>Archived releases <span class="dim">'
                         "results/timeline.json</span></h2>"
                         + _table(["date", "agent", "release", "harness", "resolution", "pass^k", "cost"],
                                  history_rows))

    details = "".join(_trial_detail(r) for r in rows)
    chart = _trend_chart(history, peers) if history else ""
    panels = chart + _quality_panel(rows, stats)

    return f"""<!doctype html>
<meta charset="utf-8">
<title>Stella agent performance</title>
<style>
:root {{ color-scheme: light dark; --line: rgba(128,128,128,.24); --dim: #8a8f98;
  --up: #2ea043; --warn: #b8860b; --bad: #d64545; --peer: #4c8bf5; }}
body {{ font: 14px/1.55 ui-sans-serif, -apple-system, "Segoe UI", sans-serif;
  margin: 0 auto; max-width: 68rem; padding: 2rem 1.25rem 4rem; }}
.eyebrow {{ font-size: .72rem; font-weight: 700; letter-spacing: .12em; text-transform: uppercase;
  color: var(--up); }}
h1 {{ font-size: 1.6rem; margin: .25rem 0 .2rem; letter-spacing: -.02em; }}
h2 {{ font-size: 1rem; margin: 2rem 0 .6rem; }}
.panel h2 {{ margin-top: 0; }}
h4 {{ font-size: .82rem; margin: 1.1rem 0 .35rem; text-transform: uppercase; letter-spacing: .04em; }}
.dim, .caption {{ color: var(--dim); }}
.caption {{ font-size: .78rem; margin: .6rem 0 0; }}
.up {{ color: var(--up); }} .warn-text {{ color: var(--warn); }}
.ok-text {{ color: var(--up); }} .bad-text {{ color: var(--bad); font-weight: 600; }}
.grid {{ display: grid; gap: .8rem; margin-top: 1.2rem; }}
.cards {{ grid-template-columns: repeat(auto-fit, minmax(13rem, 1fr)); }}
.panels {{ grid-template-columns: 1.7fr 1fr; }}
.card, .panel {{ border: 1px solid var(--line); border-radius: 10px; padding: .9rem 1rem; }}
.card-label {{ color: var(--dim); font-size: .78rem; }}
.card-value {{ font-size: 1.9rem; font-weight: 650; letter-spacing: -.03em; margin: .15rem 0; }}
.card-note {{ color: var(--dim); font-size: .78rem; }}
.chart {{ width: 100%; height: 15rem; display: block; }}
.grid-lines path, .chart .grid path {{ stroke: var(--line); fill: none; }}
.chart .axis text {{ fill: var(--dim); font-size: 11px; }}
.chart .trend {{ stroke: var(--up); stroke-width: 3; fill: none; }}
.chart .trend-dot {{ fill: var(--up); }}
.chart .trend-value {{ fill: var(--up); font-size: 11px; font-weight: 700; }}
.chart .peer-dot {{ fill: var(--peer); }}
.legend {{ font-size: .78rem; color: var(--dim); margin-bottom: .3rem; }}
.key {{ margin-right: 1rem; }} .key i {{ display: inline-block; width: .6rem; height: .6rem;
  border-radius: 2px; margin-right: .3rem; }}
.trend-swatch {{ background: var(--up); }} .peer-swatch {{ background: var(--peer); }}
.stat {{ display: flex; justify-content: space-between; gap: 1rem; padding: .38rem 0;
  border-bottom: 1px solid var(--line); font-variant-numeric: tabular-nums; }}
.stat:last-of-type {{ border-bottom: 0; }}
table {{ border-collapse: collapse; width: 100%; margin: .3rem 0; font-variant-numeric: tabular-nums; }}
th, td {{ text-align: left; padding: .3rem .55rem; border-bottom: 1px solid var(--line); }}
th {{ font-size: .75rem; text-transform: uppercase; letter-spacing: .04em; color: var(--dim); font-weight: 500; }}
td:not(:first-child), th:not(:first-child) {{ text-align: right; }}
.ledger td:nth-child(3) {{ text-align: left; font-family: ui-monospace, monospace; font-size: .78rem;
  max-width: 28rem; overflow-wrap: anywhere; }}
.pill {{ display: inline-block; padding: 0 .4rem; border-radius: 4px; font-size: .75rem;
  background: rgba(128,128,128,.16); }}
.pill.ok {{ background: rgba(46,160,67,.18); }} .pill.bad {{ background: rgba(220,60,60,.22); }}
.alert {{ border: 1px solid rgba(214,69,69,.5); border-radius: 8px; padding: .1rem 1rem 1rem; margin-top: 2rem; }}
.alert h2 {{ color: var(--bad); }}
.bar {{ display: flex; height: 12px; border-radius: 6px; overflow: hidden; margin: .4rem 0 .3rem; }}
.seg {{ display: block; }}
.howto {{ background: rgba(128,128,128,.07); }}
.howto p {{ margin: .5rem 0; }}
abbr {{ text-decoration: underline dotted; text-underline-offset: 3px; cursor: help; }}
details {{ border: 1px solid var(--line); border-radius: 8px; padding: .6rem .9rem; margin: .5rem 0; }}
summary {{ cursor: pointer; }} .detail {{ padding-top: .5rem; }}
ul {{ margin: .3rem 0; padding-left: 1.1rem; }}
footer {{ margin-top: 3rem; color: var(--dim); font-size: .8rem; }}
@media (max-width: 60rem) {{ .panels {{ grid-template-columns: 1fr; }} }}
</style>
<div class="eyebrow">Agent performance · release timeline</div>
<h1>Stella eval report</h1>
<div class="dim">{_esc(job_dir)} · generated {generated}</div>
<section class="grid cards">{_headline_cards(stats, rows, history)}</section>
{"<ul class='dim'>" + "".join(f"<li>{_esc(n)}</li>" for n in notes) + "</ul>" if notes else ""}
<section class="grid panels">{panels}</section>
{fault_block}
<details class="howto"><summary>How to read this</summary>
<p>The cards describe this job. The trend describes Stella across archived
releases, and it is the comparison that matters: another agent is a peer
reference, never Stella's target, and appears only when the report is rendered
with <code>--peers</code>.</p>
<p>One trial row per attempt, so the same task appears once per repeat. Two columns
decide the outcome: <b>reward</b> is the task's own grader, and <b>valid</b> is
our own evidence check that the agent really worked inside the trial container.
A <b>NO</b> row counts as neither a pass nor a failure; it produced no evidence,
so it leaves the score entirely and is listed separately.</p>
<p>The resolution rate carries a confidence interval, and the interval is the
part that matters: five attempts cannot tell you much, however they land.
<b>pass^k</b> asks the stricter question of whether every attempt at a task
succeeded, which is what separates a capability from a lucky run.</p>
<p>Hover any column heading for what it measures.</p></details>

{history_block}
{failure_block}
<h2>Trials</h2>
{_table(["task", "reward", "valid", "state", "wall", "model", "tool", "bridge", "turns", "orch", "exec", "errs", "cmd!0", "in.tok", "out.tok", "cost"], trial_rows)}
<h2>Per task</h2>
{_table(["task", "trials", "scoreable", "resolved", "pass^k"], task_rows)}
<h2>Tool cost</h2>
{_table(["tool", "calls", "errors", "total", "slowest"], tool_rows)}
<h2>Trial detail</h2>
{details}
<footer>
<b>wall</b> is the driver's total, <b>model</b> is time the model held between messages,
<b>tool</b> is measured from the message timeline and includes Stella's dispatch overhead,
<b>bridge</b> is time actually spent inside the trial container.<br>
<b>in.tok</b>, <b>out.tok</b> and <b>cost</b> are provider-reported. A <code>-</code> means
the provider reported no usage or the model has no configured price; it never means zero.
</footer>
"""
