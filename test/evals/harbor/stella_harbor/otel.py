"""Fetch and summarize the spans belonging to a Harbor job from Tempo."""

from __future__ import annotations

import argparse
import json
import math
import re
import time
from collections import Counter, defaultdict
from pathlib import Path
from typing import Any
from urllib.parse import urlencode
from urllib.request import urlopen


def _value(value: dict[str, Any]) -> Any:
    """Unwrap one OTLP JSON AnyValue, including Tempo's string-encoded ints."""
    if "intValue" in value:
        return int(value["intValue"])
    if "doubleValue" in value:
        return float(value["doubleValue"])
    for key in ("stringValue", "boolValue"):
        if key in value:
            return value[key]
    return None


def _attrs(items: list[dict[str, Any]]) -> dict[str, Any]:
    return {item.get("key", ""): _value(item.get("value", {})) for item in items}


def _span_rows(trace: dict[str, Any]) -> list[dict[str, Any]]:
    """Normalize Tempo's OTLP JSON trace response to the fields this report needs."""
    rows: list[dict[str, Any]] = []
    for resource_span in trace.get("resourceSpans", trace.get("batches", [])):
        scopes = resource_span.get("scopeSpans", resource_span.get("instrumentationLibrarySpans", []))
        for scope in scopes:
            for span in scope.get("spans", []):
                attrs = _attrs(span.get("attributes", []))
                start = int(span.get("startTimeUnixNano", 0))
                end = int(span.get("endTimeUnixNano", 0))
                if end < start:
                    continue
                rows.append({"name": span.get("name", "unknown"), "duration_ms": (end - start) / 1_000_000,
                             "attributes": attrs})
    return rows


def trial_sessions(job: Path) -> dict[str, str]:
    """Return session id -> trial label from the adapter evidence saved per trial."""
    sessions: dict[str, str] = {}
    for result in job.glob("*/**/agent/stella/result.json"):
        try:
            session = json.loads(result.read_text()).get("session_id")
        except (OSError, json.JSONDecodeError):
            continue
        if session:
            sessions[str(session)] = result.parents[2].name
    return sessions


def _get_json(url: str) -> dict[str, Any]:
    with urlopen(url, timeout=20) as response:  # nosec B310: localhost Tempo chosen by this script
        return json.load(response)


def _trace_ids(grafana_url: str, session: str, start: int, end: int) -> list[str]:
    # Tempo's search API accepts TraceQL in q. The conversation id is set on
    # agent.loop and gen_ai.chat by Stella's trace hook; fetching whole traces
    # afterward includes child spans which legitimately do not repeat it.
    query = '{ span.gen_ai.conversation.id = "' + session.replace('"', '\\"') + '" }'
    params = urlencode({"q": query, "start": start, "end": end, "limit": 100})
    data = _get_json(f"{grafana_url}/api/datasources/proxy/uid/tempo/api/search?{params}")
    return [str(item["traceID"]) for item in data.get("traces", []) if item.get("traceID")]


def _trace_window(job: Path) -> tuple[int, int]:
    mtimes = [path.stat().st_mtime for path in job.rglob("*") if path.is_file()]
    if not mtimes:
        now = int(time.time())
        return now - 3600, now + 300
    return int(min(mtimes)) - 300, int(max(mtimes)) + 300


def fetch(job: Path, grafana_url: str) -> tuple[dict[str, str], list[dict[str, Any]]]:
    sessions = trial_sessions(job)
    rows: list[dict[str, Any]] = []
    seen: set[tuple[str, str]] = set()
    pending = set(sessions)
    start, end = _trace_window(job)
    deadline = time.monotonic() + 15
    while pending:
        for session in list(pending):
            trace_ids = _trace_ids(grafana_url, session, start, end)
            if not trace_ids:
                continue
            pending.remove(session)
            for trace_id in trace_ids:
                key = (session, trace_id)
                if key in seen:
                    continue
                seen.add(key)
                trace_rows = _span_rows(_get_json(
                    f"{grafana_url}/api/datasources/proxy/uid/tempo/api/traces/{trace_id}"
                ))
                for row in trace_rows:
                    row["trial_session_id"] = session
                rows.extend(trace_rows)
        if not pending or time.monotonic() >= deadline:
            break
        time.sleep(1)
    return sessions, rows


def _p95(values: list[float]) -> float:
    if not values:
        return 0
    return sorted(values)[min(len(values) - 1, math.ceil(len(values) * 0.95) - 1)]


def _group_name(span: dict[str, Any]) -> str:
    name = str(span["name"])
    if span["attributes"].get("db.system"):
        return "db.query"
    if re.fullmatch(r"turn [0-9]+", name):
        return "turn"
    return name


def summarize(
    sessions: dict[str, str], spans: list[dict[str, Any]]
) -> tuple[list[dict[str, Any]], dict[str, int], Counter[str]]:
    per_trial: Counter[str] = Counter()
    grouped: dict[str, list[float]] = defaultdict(list)
    model = {"request_spans": 0, "attempts": 0, "retries": 0}
    for span in spans:
        attrs = span["attributes"]
        session = span.get("trial_session_id") or attrs.get("gen_ai.conversation.id")
        if session in sessions:
            per_trial[str(session)] += 1
        grouped[_group_name(span)].append(float(span["duration_ms"]))
        if span["name"] == "gen_ai.chat.request":
            model["request_spans"] += 1
        attempts = attrs.get("gen_ai.request.attempts")
        retries = attrs.get("gen_ai.request.retry_count")
        if isinstance(attempts, (int, float)):
            model["attempts"] += int(attempts)
        if isinstance(retries, (int, float)):
            model["retries"] += int(retries)
    stats = [{"name": name, "count": len(values), "total_ms": sum(values),
              "mean_ms": sum(values) / len(values), "p95_ms": _p95(values), "max_ms": max(values)}
             for name, values in grouped.items()]
    return sorted(stats, key=lambda row: (-row["total_ms"], row["name"])), model, per_trial


def harbor_retries(job: Path) -> int | None:
    for result in sorted(job.glob("*/result.json")):
        try:
            value = (json.loads(result.read_text()).get("stats") or {}).get("n_retries")
        except (OSError, json.JSONDecodeError):
            continue
        if isinstance(value, int):
            return value
    return None


def render(job: Path, sessions: dict[str, str], spans: list[dict[str, Any]]) -> str:
    stats, model, per_trial = summarize(sessions, spans)
    matched = [(session, sessions[session], per_trial[session]) for session in sessions if per_trial[session]]
    if not matched:
        raise RuntimeError("Tempo returned zero spans for every trial session in this job")
    lines = [f"OTel span analysis: {len(spans)} spans from {len(matched)}/{len(sessions)} trial session(s)",
             "span name                         count    total     mean      p95  slowest"]
    for stat in stats:
        lines.append(f"{stat['name'][:32]:32} {stat['count']:5} {stat['total_ms']:7.0f}ms"
                     f" {stat['mean_ms']:7.0f}ms {stat['p95_ms']:7.0f}ms {stat['max_ms']:7.0f}ms")
    assertion_session, assertion_trial, assertion_count = matched[0]
    lines.append(
        f"trial span assertion: PASS session={assertion_session} "
        f"trial={assertion_trial} spans={assertion_count}"
    )
    absent = len(sessions) - len(matched)
    if absent:
        lines.append(f"trial sessions with no retained Tempo trace: {absent}")
    attempts = model["attempts"] or model["request_spans"]
    lines.append(
        f"model requests: spans={model['request_spans']}, attempts={attempts}, "
        f"retries={model['retries']}"
    )
    harbor = harbor_retries(job)
    harbor_text = "unknown" if harbor is None else str(harbor)
    lines.append(f"Harbor trial retries: {harbor_text}")
    return "\n".join(lines)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("job", type=Path)
    parser.add_argument("--grafana-url", required=True)
    args = parser.parse_args(argv)
    sessions, spans = fetch(args.job, args.grafana_url.rstrip("/"))
    print(render(args.job, sessions, spans))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
