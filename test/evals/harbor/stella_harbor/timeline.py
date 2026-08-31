"""The release history a single job's report is read against.

One job answers "how did this run go". A reader always asks the next question,
"is that better than last time", and the job directory cannot answer it: it
holds one run. This module loads the committed history so the report can draw
Stella's own trend and say plainly when two points are not comparable.

The file is `results/timeline.json`, kept next to the human scoreboard in
`results/README.md`. Peer agents may appear in it, but they are never Stella's
target: they render only when a reader explicitly asks for the overlay.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

# The agent whose trend is the report's subject. Everything else is a peer.
SUBJECT = "stella"


def default_path() -> Path:
    """`results/timeline.json` as shipped in the repo, whether or not it exists."""
    return Path(__file__).resolve().parent.parent / "results" / "timeline.json"


def load(path: Path | None = None) -> list[dict[str, Any]]:
    """Read the history oldest-first, or return nothing if there is none.

    A missing or unreadable file is not an error: the report is still a valid
    report of one job, it just cannot draw a trend. Silently dropping a
    malformed file would be worse than not having one, so callers that care can
    check for the empty list, but no caller has to handle an exception to
    render.
    """
    path = path or default_path()
    try:
        raw = json.loads(path.read_text())
    except (OSError, ValueError):
        return []
    runs = raw.get("runs") if isinstance(raw, dict) else raw
    if not isinstance(runs, list):
        return []
    return sorted((r for r in runs if isinstance(r, dict) and r.get("date")),
                  key=lambda r: str(r["date"]))


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
    configuration matched. Anything else is descriptive context, and the report
    has to label it that way rather than let a rising line imply a win.
    """
    keys = ("benchmark", "model", "k", "harness", "host")
    return all(a.get(key) == b.get(key) for key in keys)
