"""Render a job's metrics as one self-contained HTML file.

The text report is for a terminal; this is for reading. Everything is inlined
(no CSS or JS fetched at open time) so the file can be attached to an issue or
copied to another machine and still render.
"""

from __future__ import annotations

import html
import json
from datetime import datetime, timezone
from typing import Any

from .report import RESOLVED, _int, _usd, reliability
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
    "calls": "how many tool calls it made",
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

    journal_rows = []
    for source in ("adapter", "driver"):
        for entry in (row.get("phase_journal") or {}).get(source, []):
            journal_rows.append([source, _esc(entry.get("phase")), _esc(entry.get("timestamp"))])
    if journal_rows:
        parts.append("<h4>durable phase journal <span class='dim'>diagnostic only</span></h4>" +
                     _table(["writer", "phase", "UTC timestamp"], journal_rows))

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


def render_html(rows: list[dict[str, Any]], job_dir: str = "") -> str:
    stats = reliability(rows)
    generated = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M UTC")

    if stats["resolution_rate"] is None:
        headline = '<div class="headline bad-text">no scoreable trials</div>'
    else:
        low, high = stats["ci95"]
        margin = (high - low) / 2 * 100
        headline = (f'<div class="headline">{stats["resolution_rate"] * 100:.1f}%'
                    f'<span class="ci"> ±{margin:.1f}% (95% CI {low * 100:.1f}–{high * 100:.1f})</span></div>'
                    f'<div class="dim">resolution rate, {stats["resolved"]}/{stats["scoreable"]} '
                    f'scoreable trials, reward ≥ {RESOLVED}</div>')

    notes = []
    if stats["k"] >= 2:
        notes.append(f'pass^{stats["k"]} {stats["pass_hat_k"] * 100:.1f}% of {len(stats["tasks"])} tasks')
    else:
        notes.append("pass^k unavailable: a task has fewer than 2 scoreable trials (run with -k 5)")
    if stats["invalid"]:
        notes.append(f'{stats["invalid"]} trial(s) excluded as invalid: missing evidence, not failures')
    if stats["timeouts"]:
        notes.append(f'{stats["timeouts"]} trial(s) hit the deadline')

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
        str(r["calls"] if r["calls"] is not None else "-"),
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

    details = "".join(_trial_detail(r) for r in rows)

    return f"""<!doctype html>
<meta charset="utf-8">
<title>Stella eval report</title>
<style>
:root {{ color-scheme: light dark; }}
body {{ font: 14px/1.55 ui-sans-serif, -apple-system, "Segoe UI", sans-serif;
  margin: 0 auto; max-width: 62rem; padding: 2rem 1.25rem 4rem; }}
h1 {{ font-size: 1.35rem; margin: 0 0 .2rem; }}
h2 {{ font-size: 1rem; margin: 2rem 0 .6rem; }}
h4 {{ font-size: .82rem; margin: 1.1rem 0 .35rem; text-transform: uppercase; letter-spacing: .04em; }}
.dim {{ color: #8a8f98; font-weight: 400; }}
.headline {{ font-size: 2.6rem; font-weight: 650; line-height: 1.1; }}
.ci {{ font-size: 1rem; font-weight: 400; color: #8a8f98; }}
table {{ border-collapse: collapse; width: 100%; margin: .3rem 0; font-variant-numeric: tabular-nums; }}
th, td {{ text-align: left; padding: .3rem .55rem; border-bottom: 1px solid rgba(128,128,128,.22); }}
th {{ font-size: .75rem; text-transform: uppercase; letter-spacing: .04em; color: #8a8f98; font-weight: 500; }}
td:not(:first-child), th:not(:first-child) {{ text-align: right; }}
.ledger td:nth-child(3) {{ text-align: left; font-family: ui-monospace, monospace; font-size: .78rem;
  max-width: 28rem; overflow-wrap: anywhere; }}
.pill {{ display: inline-block; padding: 0 .4rem; border-radius: 4px; font-size: .75rem;
  background: rgba(128,128,128,.16); }}
.pill.ok {{ background: rgba(46,160,67,.18); }} .pill.bad {{ background: rgba(220,60,60,.22); }}
.ok-text {{ color: #2ea043; }} .bad-text {{ color: #d64545; font-weight: 600; }}
.alert {{ border: 1px solid rgba(214,69,69,.5); border-radius: 8px; padding: .1rem 1rem 1rem; margin-top: 2rem; }}
.alert h2 {{ color: #d64545; }}
.bar {{ display: flex; height: 12px; border-radius: 6px; overflow: hidden; margin: .4rem 0 .3rem; }}
.seg {{ display: block; }}
.legend {{ font-size: .78rem; color: #8a8f98; }}
.key {{ margin-right: 1rem; }} .key i {{ display: inline-block; width: .6rem; height: .6rem;
  border-radius: 2px; margin-right: .3rem; }}
.howto {{ background: rgba(128,128,128,.07); }}
.howto p {{ margin: .5rem 0; }}
abbr {{ text-decoration: underline dotted; text-underline-offset: 3px; cursor: help; }}
details {{ border: 1px solid rgba(128,128,128,.25); border-radius: 8px; padding: .6rem .9rem; margin: .5rem 0; }}
summary {{ cursor: pointer; }} .detail {{ padding-top: .5rem; }}
ul {{ margin: .3rem 0; padding-left: 1.1rem; }}
footer {{ margin-top: 3rem; color: #8a8f98; font-size: .8rem; }}
</style>
<h1>Stella eval report</h1>
<div class="dim">{_esc(job_dir)} · generated {generated}</div>
<section>{headline}</section>
<ul class="dim">{"".join(f"<li>{_esc(n)}</li>" for n in notes)}</ul>
{fault_block}
<details class="howto"><summary>How to read this</summary>
<p>One row per attempt, so the same task appears once per repeat. Two columns
decide the outcome: <b>reward</b> is the task's own grader, and <b>valid</b> is
our own evidence check that the agent really worked inside the trial container.
A <b>NO</b> row counts as neither a pass nor a failure; it produced no evidence,
so it leaves the score entirely and is listed separately.</p>
<p>The resolution rate is a percentage with a confidence interval, and the
interval is the part that matters: five attempts cannot tell you much, however
they land. <b>pass^k</b> asks the stricter question of whether every attempt at
a task succeeded, which is what separates a capability from a lucky run.</p>
<p>Hover any column heading for what it measures.</p></details>

{failure_block}
<h2>Trials</h2>
{_table(["task", "reward", "valid", "state", "wall", "model", "tool", "bridge", "turns", "calls", "errs", "cmd!0", "est.tok"], trial_rows)}
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
<b>est.tok</b> is <code>len(text)/4</code>, Stella's own estimate, kept only for comparing
trials against each other.
</footer>
"""
