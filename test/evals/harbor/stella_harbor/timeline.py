"""The release history a single job's report is read against.

One job answers "how did this run go". A reader always asks the next question,
"is that better than last time", and the job directory cannot answer it: it
holds one run. This module loads the committed history so a report can draw
Stella's own trend and say plainly when two points are not comparable.

The file is `results/timeline.csv`, next to the human scoreboard in
`results/README.md`. CSV because the record has to survive without this code:
it diffs one line per run in review, opens in a spreadsheet, and `sort` and
`awk` read it. Nothing renders it ahead of time; a view is generated when
someone wants to look.

Peer agents may appear in it, but they are never Stella's target: they render
only when a reader explicitly asks for the overlay.
"""

from __future__ import annotations

import csv
from pathlib import Path
from typing import Any

# The agent whose trend is the record's subject. Everything else is a peer.
SUBJECT = "stella"

# Columns parsed as numbers. A blank stays None rather than becoming 0, because
# a run that never measured a field is not a run that measured zero.
INTS = ("k", "resolved", "scoreable")
FLOATS = ("resolution", "pass_k", "cost_usd")


def default_path() -> Path:
    """`results/timeline.csv` as shipped in the repo, whether or not it exists."""
    return Path(__file__).resolve().parent.parent / "results" / "timeline.csv"


def _number(value: str, cast: type) -> Any:
    try:
        return cast(value)
    except (TypeError, ValueError):
        return None


def load(path: Path | None = None) -> list[dict[str, Any]]:
    """Read the history oldest-first, or return nothing if there is none.

    A missing or unreadable file is not an error: the report is still a valid
    report of one job, it just cannot draw a trend. No caller has to handle an
    exception in order to render.
    """
    path = path or default_path()
    try:
        with Path(path).open(newline="") as handle:
            rows = [r for r in csv.DictReader(handle) if r.get("date")]
    except (OSError, csv.Error):
        return []
    for row in rows:
        for field in INTS:
            if field in row:
                row[field] = _number(row[field], int)
        for field in FLOATS:
            if field in row:
                row[field] = _number(row[field], float)
    return sorted(rows, key=lambda r: str(r["date"]))


def is_subject(run: dict[str, Any]) -> bool:
    return str(run.get("agent", "")).strip().lower() == SUBJECT


def select(runs: list[dict[str, Any]], peers: bool = False) -> list[dict[str, Any]]:
    """Stella's own runs, plus peers only when the reader asked for them."""
    return [r for r in runs if peers or is_subject(r)]


def latest_subject(runs: list[dict[str, Any]]) -> dict[str, Any] | None:
    subject = [r for r in runs if is_subject(r)]
    return subject[-1] if subject else None


def previous_subject(runs: list[dict[str, Any]]) -> dict[str, Any] | None:
    subject = [r for r in runs if is_subject(r)]
    return subject[-2] if len(subject) >= 2 else None


def comparable(a: dict[str, Any], b: dict[str, Any]) -> bool:
    """Do two runs measure the same thing.

    Movement between two runs is causal evidence only when the whole
    configuration matched. Anything else is descriptive context, and a view has
    to label it that way rather than let a rising line imply a win.
    """
    keys = ("benchmark", "model", "k", "harness", "host")
    return all(a.get(key) == b.get(key) for key in keys)
