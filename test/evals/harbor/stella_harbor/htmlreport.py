"""Render a job's metrics as one self-contained HTML file.

The text report is for a terminal; this is for reading. Everything is inlined
(no CSS or JS fetched at open time) so the file can be attached to an issue or
copied to another machine and still render.

This is a view, never the record: `--csv` writes the run's raw numbers, and
this is rendered from them whenever someone wants to look. Nothing here is
archived, so it is free to change shape.

The view opens on the question a reviewer actually has, "did Stella get
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
from .report import _int, _usd, reliability, summarize
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


def _format(value: Any, unit: str) -> str:
    """A metric's value in the unit a reader thinks in, or a dash if unmeasured."""
    if value is None:
        return "-"
    if unit == "rate":
        return f"{value * 100:.1f}%"
    if unit == "usd":
        return f"${value:.4f}"
    if unit == "ms":
        return f"{value / 1000:.0f}s"
    return str(value)


def _sparkline(points: list[tuple[str, float]], higher_is_better: bool) -> str:
    """One metric's shape across releases, in the space of a table cell.

    No axis: at three or four releases an axis costs more attention than it
    returns, and the numbers beside it carry the magnitude. The shape is here to
    answer "which way is this going", nothing else.
    """
    W, H, PAD = 200, 46, 6
    values = [v for _, v in points]
    lo, hi = min(values), max(values)
    span = (hi - lo) or (abs(hi) or 1.0)

    def x(i: int) -> float:
        return PAD if len(points) == 1 else PAD + i * (W - 2 * PAD) / (len(points) - 1)

    def y(value: float) -> float:
        return H - PAD - (value - lo) / span * (H - 2 * PAD)

    path = "L".join(f"{x(i):.1f} {y(v):.1f}" for i, (_, v) in enumerate(points))
    improving = (values[-1] >= values[0]) == higher_is_better
    tone = "good" if improving else "bad"
    dots = "".join(
        f'<circle cx="{x(i):.1f}" cy="{y(v):.1f}" r="{3 if i == len(points) - 1 else 2}" '
        f'class="spark-dot"><title>{_esc(label)}</title></circle>'
        for i, (label, v) in enumerate(points))
    line = f'<path d="M{path}" class="spark-line"/>' if len(points) > 1 else ""
    return (f'<svg class="spark {tone}" viewBox="0 0 {W} {H}" preserveAspectRatio="none" '
            f'role="img" aria-hidden="true">{line}{dots}</svg>')


def _metric_trends(history: list[dict[str, Any]], current: dict[str, Any] | None,
                   peers: bool) -> str:
    """Every metric's release trend, side by side.

    One number from one run is not a measurement anyone can act on. The question
    is always which direction it moved and whether that movement is real, so
    each metric gets its own line and its own delta rather than a paragraph
    explaining the headline.
    """
    subject = [r for r in history if timeline.is_subject(r)]
    if current is not None:
        # The job being reported is usually not archived yet. Showing it as the
        # trailing point is the whole reason a reader opens this file.
        last = subject[-1] if subject else None
        if not last or (last.get("resolved"), last.get("scoreable")) != (
                current.get("resolved"), current.get("scoreable")):
            subject = [*subject, {**current, "date": "this run", "agent": "Stella"}]

    cells = []
    for key, label, unit, higher_is_better in timeline.METRICS:
        points = [(str(r["date"]), r[key]) for r in timeline.series(subject, key)]
        latest = points[-1][1] if points else None
        delta_html = '<span class="dim">first measurement</span>'
        if len(points) >= 2:
            delta = points[-1][1] - points[-2][1]
            better = (delta >= 0) == higher_is_better
            shown = ("=" if delta == 0 else
                     f'{delta * 100:+.1f}pp' if unit == "rate" else
                     f'{delta / 1000:+.0f}s' if unit == "ms" else f'{delta:+.4f}')
            delta_html = (f'<span class="{"good" if better else "bad"}">{shown}</span>'
                          f'<span class="dim"> vs {_esc(points[-2][0])}</span>')
        elif not points:
            delta_html = '<span class="dim">never measured</span>'
        cells.append(
            f'<div class="metric"><div class="metric-label">{_esc(label)}</div>'
            f'<div class="metric-value">{_format(latest, unit)}</div>'
            f'{_sparkline(points, higher_is_better) if points else ""}'
            f'<div class="metric-delta">{delta_html}</div></div>')

    peer_note = ""
    if peers:
        peer_rows = [r for r in history if not timeline.is_subject(r)]
        if peer_rows:
            peer_note = ('<p class="caption">Peer runs are in the release table below and are '
                         "deliberately absent from these lines: a peer is a reference, not a "
                         "target, and does not belong in Stella's own trend.</p>")
    incomparable = any(not timeline.comparable(a, b) for a, b in pairwise(subject))
    caption = ("Some adjacent releases differ in benchmark, model, k, harness or host. Those "
               "movements are descriptive context, not evidence about a code change."
               if incomparable else
               "Every adjacent pair was measured the same way.")
    return (f'<h2>Metric trends <span class="dim">Stella releases, newest on the right</span></h2>'
            f'<section class="grid metrics">{"".join(cells)}</section>'
            f'<p class="caption">{caption} A metric with no line was never measured by these '
            f"runs, which is not the same as measuring zero.</p>{peer_note}")


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
                           "good" if delta > 0 else "bad" if delta < 0 else ""))
    else:
        cards.append(_card("vs prior Stella release", "-",
                           "no earlier Stella run in results/timeline.csv"))

    if stats["k"] >= 2:
        prior_pass = prior.get("pass_k") if prior else None
        note = (f'{len(stats["tasks"])} tasks · every attempt resolved'
                if prior_pass is None else
                f'{len(stats["tasks"])} tasks · was {_pct(prior_pass)}')
        tone = "good" if prior_pass is not None and stats["pass_hat_k"] > prior_pass else ""
        cards.append(_card(f'Stability · pass^{stats["k"]}', _pct(stats["pass_hat_k"]), note, tone))
    else:
        cards.append(_card("Stability · pass^k", "-",
                           "needs at least 2 scoreable trials per task; run with -k 5"))

    healthy = not stats["invalid"] and not any(r.get("adapter_faults") for r in rows)
    cards.append(_card("Evidence health", f'{stats["scoreable"]} / {stats["trials"]}',
                       f'{len(stats["tasks"])} tasks · '
                       + ("every trial proved it ran in-container" if healthy
                          else "some trials carry no usable evidence"),
                       "good" if healthy else "bad"))
    return "".join(cards)


def render_html(rows: list[dict[str, Any]], job_dir: str = "",
                history: list[dict[str, Any]] | None = None, peers: bool = False,
                detail: bool = False) -> str:
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
                         "results/timeline.csv</span></h2>"
                         + _table(["date", "agent", "release", "harness", "resolution", "pass^k", "cost"],
                                  history_rows))

    # Every trial's ledger inlined turns a 445-trial job into megabytes of HTML
    # nobody scrolls. It is off unless asked for; the same data is in the job
    # directory and in the CSV.
    detail_block = ("<h2>Trial detail</h2>" + "".join(_trial_detail(r) for r in rows)
                    if detail else
                    "")
    trends = _metric_trends(history, summarize(rows), peers)

    return f"""<!doctype html>
<meta charset="utf-8">
<title>Stella agent performance</title>
<style>
:root {{ color-scheme: light dark; --line: rgba(128,128,128,.24); --dim: #8a8f98;
  --up: #2ea043; --bad: #d64545; }}
body {{ font: 14px/1.55 ui-sans-serif, -apple-system, "Segoe UI", sans-serif;
  margin: 0 auto; max-width: 68rem; padding: 2rem 1.25rem 4rem; }}
.eyebrow {{ font-size: .72rem; font-weight: 700; letter-spacing: .12em; text-transform: uppercase;
  color: var(--up); }}
h1 {{ font-size: 1.6rem; margin: .25rem 0 .2rem; letter-spacing: -.02em; }}
h2 {{ font-size: 1rem; margin: 2rem 0 .6rem; }}
h4 {{ font-size: .82rem; margin: 1.1rem 0 .35rem; text-transform: uppercase; letter-spacing: .04em; }}
.dim, .caption {{ color: var(--dim); }}
.caption {{ font-size: .78rem; margin: .6rem 0 0; }}
.good {{ color: var(--up); }} .bad {{ color: var(--bad); }}
.ok-text {{ color: var(--up); }} .bad-text {{ color: var(--bad); font-weight: 600; }}
.grid {{ display: grid; gap: .8rem; margin-top: 1.2rem; }}
.cards {{ grid-template-columns: repeat(auto-fit, minmax(13rem, 1fr)); }}
.metrics {{ grid-template-columns: repeat(auto-fit, minmax(12rem, 1fr)); }}
.card, .metric {{ border: 1px solid var(--line); border-radius: 10px; padding: .9rem 1rem; }}
.metric-label {{ color: var(--dim); font-size: .76rem; }}
.metric-value {{ font-size: 1.35rem; font-weight: 650; letter-spacing: -.02em; margin: .1rem 0 .3rem; }}
.metric-delta {{ font-size: .76rem; margin-top: .3rem; }}
.spark {{ width: 100%; height: 2.9rem; display: block; overflow: visible; }}
.spark .spark-line {{ fill: none; stroke-width: 2; vector-effect: non-scaling-stroke; }}
.spark.good .spark-line, .spark.good .spark-dot {{ stroke: var(--up); fill: var(--up); }}
.spark.bad .spark-line, .spark.bad .spark-dot {{ stroke: var(--bad); fill: var(--bad); }}
.card-label {{ color: var(--dim); font-size: .78rem; }}
.card-value {{ font-size: 1.9rem; font-weight: 650; letter-spacing: -.03em; margin: .15rem 0; }}
.card-note {{ color: var(--dim); font-size: .78rem; }}
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
.legend {{ font-size: .78rem; color: var(--dim); }}
.key {{ margin-right: 1rem; }} .key i {{ display: inline-block; width: .6rem;
  height: .6rem; border-radius: 2px; margin-right: .3rem; }}
.howto {{ background: rgba(128,128,128,.07); }}
.howto p {{ margin: .5rem 0; }}
abbr {{ text-decoration: underline dotted; text-underline-offset: 3px; cursor: help; }}
details {{ border: 1px solid var(--line); border-radius: 8px; padding: .6rem .9rem; margin: .5rem 0; }}
summary {{ cursor: pointer; }} .detail {{ padding-top: .5rem; }}
ul {{ margin: .3rem 0; padding-left: 1.1rem; }}
footer {{ margin-top: 3rem; color: var(--dim); font-size: .8rem; }}
</style>
<div class="eyebrow">Agent performance · release timeline</div>
<h1>Stella eval report</h1>
<div class="dim">{_esc(job_dir)} · generated {generated}</div>
<section class="grid cards">{_headline_cards(stats, rows, history)}</section>
{"<ul class='dim'>" + "".join(f"<li>{_esc(n)}</li>" for n in notes) + "</ul>" if notes else ""}
{trends}
{fault_block}
<details class="howto"><summary>How to read this</summary>
<p>The cards describe this job. Everything below is Stella across its archived
releases, because one number from one run is not something anyone can act on.
Each metric carries its own direction: a rising cost line is not good news, so
green always means improving and red always means worse.</p>
<p>A metric with no line was never measured by these runs. That is not zero,
and the record keeps the two apart on purpose. Adjacent releases that differ in
benchmark, model, k, harness or host are labelled descriptive context: the
movement is real, the causal story is not.</p>
<p>Per-trial rows are not here. They are in <code>trials.csv</code> next to this
file, which is the artifact worth keeping; re-render with <code>--detail</code>
if you want the ledger inline.</p></details>

{failure_block}
{history_block}
{detail_block}
<footer>
<b>wall</b> is the driver's total, <b>model</b> is time the model held between messages,
<b>tool</b> is measured from the message timeline and includes Stella's dispatch overhead,
<b>bridge</b> is time actually spent inside the trial container.<br>
<b>in.tok</b>, <b>out.tok</b> and <b>cost</b> are provider-reported. A <code>-</code> means
the provider reported no usage or the model has no configured price; it never means zero.
</footer>
"""
