"""Classify why a trial did not resolve.

Terminal-Bench asks for a failure breakdown, not just a pass rate, because
"60% resolved" gives a reader nothing to act on while "half the failures were
verification" does.

Every rule here is deterministic and reads evidence the trial already produced.
Nothing is guessed: a trial whose failure no rule explains is labelled
`unclassified` and counted, so the gap stays visible instead of being absorbed
into whichever bucket looks plausible. That is also the honest boundary for a
future LLM judge, which should only ever be offered the unclassified pile.
"""

from __future__ import annotations

from typing import Any

RESOLVED = 1.0

# Ordered: the first rule that fits wins, and the order encodes precedence, not
# preference. A trial that ran out of time is a timeout even if its last tool
# call also failed, because the deadline is what determined the outcome.
LABELS = ("resolved", "invalid", "timeout", "execution", "coherence", "verification", "unclassified")

DESCRIPTIONS = {
    "resolved": "the verifier awarded full reward",
    "invalid": "no usable evidence; excluded from the score, not a failure",
    "timeout": "the deadline stopped the agent mid-task",
    "execution": "the agent was working and the machinery failed under it",
    "coherence": "the agent stopped engaging with the task before finishing it",
    "verification": "the agent finished and was wrong; it never checked its own work",
    "unclassified": "no rule explains this failure; label it by hand",
}


def classify(row: dict[str, Any]) -> tuple[str, str]:
    """Return a label and the evidence that produced it."""
    if row.get("valid") is not True:
        return "invalid", "trial produced no usable evidence"
    reward = row.get("reward")
    if reward is not None and reward >= RESOLVED:
        return "resolved", f"reward {reward}"

    if row.get("timed_out"):
        return "timeout", "the trial hit its deadline"

    state = row.get("state")
    if state == "errored" or row.get("stream_errors"):
        detail = (row.get("stream_errors") or ["turn ended in an error state"])[0]
        return "execution", str(detail)[:200]

    calls = row.get("calls") or 0
    errors = row.get("tool_errors") or 0
    if calls == 0:
        # It held a turn and never touched the container. Whatever it was doing,
        # it was not the task.
        return "coherence", "the agent made no tool calls at all"
    if errors and errors == calls:
        return "execution", f"every one of the {calls} tool calls failed"

    if reward is None:
        return "unclassified", "the verifier produced no reward"
    if state == "completed":
        return "verification", f"the agent finished but scored {reward}"
    return "unclassified", f"terminal state {state!r} with reward {reward}"


def breakdown(rows: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Count labels in a fixed order, keeping one example of each failure."""
    counts: dict[str, int] = {}
    examples: dict[str, tuple[str, str]] = {}
    for row in rows:
        label, why = classify(row)
        counts[label] = counts.get(label, 0) + 1
        examples.setdefault(label, (row.get("task", "?"), why))
    return [{"label": label, "count": counts[label], "description": DESCRIPTIONS[label],
             "example_task": examples[label][0], "example_reason": examples[label][1]}
            for label in LABELS if label in counts]
