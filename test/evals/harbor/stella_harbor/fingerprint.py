"""Extract and validate the configuration identity of a Harbor run."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

CONDITION_FIELDS = (
    "dataset_id",
    "dataset_hash",
    "model",
    "budget",
    "concurrency",
    "timeout_multiplier",
)
AGENT_FIELDS = (
    "agent_name",
    "capability_profile_digest",
    "candidate_commit",
    "tool_strategy",
    "excluded_tools",
)
FINGERPRINT_FIELDS = CONDITION_FIELDS + AGENT_FIELDS
# Candidate commits are the variable being measured in a same-agent comparison.

FINGERPRINT_SOURCES = {
    "dataset_id": "run config.json: datasets[].name",
    "dataset_hash": "run config.json: datasets[].ref",
    "model": "driver result.json: model",
    "budget": "run config.json: n_attempts",
    "concurrency": "run config.json: n_concurrent_trials",
    "timeout_multiplier": "effective trial config: agent_timeout_multiplier or timeout_multiplier",
    "agent_name": "run config.json: agents[].name",
    "capability_profile_digest": "driver result.json: capability_profile_digest",
    "candidate_commit": "driver result.json: candidate_commit",
    "tool_strategy": "run/trial config: tool_strategy or tool_policy",
    "excluded_tools": "driver result.json: excluded_tools",
}


def _read_json(path: Path) -> dict[str, Any]:
    if not path.exists():
        return {}
    value = json.loads(path.read_text())
    return value if isinstance(value, dict) else {}


def _distinct(values: list[Any]) -> list[Any]:
    distinct: list[Any] = []
    for value in values:
        if value is not None and value not in distinct:
            distinct.append(value)
    return distinct


def _value(distinct: list[Any]) -> Any:
    if not distinct:
        return None
    return distinct[0] if len(distinct) == 1 else sorted(distinct, key=repr)


def _agent_name(config: dict[str, Any]) -> str | None:
    agents = config.get("agents") or []
    if not agents or not isinstance(agents[0], dict):
        return None
    name = agents[0].get("name")
    return name if isinstance(name, str) else None


def _model_from_info(info: Any) -> str | None:
    if not isinstance(info, dict):
        return None
    name = info.get("name")
    provider = info.get("provider")
    if isinstance(name, str) and isinstance(provider, str) and provider:
        return f"{provider}/{name}"
    return name if isinstance(name, str) else None


def _walk_values(value: Any, keys: set[str]) -> list[Any]:
    found: list[Any] = []
    if isinstance(value, dict):
        for key, child in value.items():
            if key in keys and child is not None:
                found.append(child)
            found.extend(_walk_values(child, keys))
    elif isinstance(value, list):
        for child in value:
            found.extend(_walk_values(child, keys))
    return found


def _trial_files(run: Path) -> list[tuple[Path, dict[str, Any], dict[str, Any]]]:
    """Read every trial directory, including trials without a result yet."""
    trials: list[tuple[Path, dict[str, Any], dict[str, Any]]] = []
    for trial in sorted(p for p in run.iterdir() if p.is_dir()):
        result_data = _read_json(trial / "result.json")
        config = _read_json(trial / "config.json")
        # Older Harbor exports put the complete effective trial config in the
        # trial result while config.json contains only the task identity.
        effective_config = result_data.get("config")
        if isinstance(effective_config, dict):
            config = {**effective_config, **config}
        trials.append((trial, config, result_data))
    return trials


def _run_total(run_result: dict[str, Any], trials: list[Any]) -> int:
    total = run_result.get("n_total_trials")
    return total if isinstance(total, int) and total > 0 else len(trials)


def _trial_values(
    field: str,
    config: dict[str, Any],
    result: dict[str, Any],
    adapter: dict[str, Any],
) -> list[Any]:
    if field == "model":
        agent = config.get("agent") or {}
        values = [agent.get("model_name"), result.get("model")]
        if isinstance(result.get("agent_info"), dict):
            values.append(_model_from_info(result["agent_info"].get("model_info")))
        return _distinct(values)
    if field == "agent_name":
        agent = config.get("agent") or {}
        return _distinct([agent.get("name"), (result.get("agent_info") or {}).get("name")])
    if field == "timeout_multiplier":
        return _distinct([config.get("agent_timeout_multiplier"), config.get("timeout_multiplier")])
    if field == "capability_profile_digest":
        return _distinct(_walk_values({"result": result, "adapter": adapter}, {field}))
    if field == "candidate_commit":
        return _distinct(_walk_values({"config": config, "result": result, "adapter": adapter}, {field, "candidate_commit_sha"}))
    if field == "tool_strategy":
        return _distinct(_walk_values(config, {"tool_strategy", "tool_policy"}))
    if field == "excluded_tools":
        # The field predates its artifact recording. Missing or explicit null
        # means the run requested no exclusions, the same semantics as [].
        value = adapter.get(field)
        return _distinct([[] if value is None else value])
    return []


def _summarize_units(units: list[list[Any]], total: int, source: str) -> tuple[Any, dict[str, Any]]:
    present = sum(bool(unit) for unit in units)
    distinct = _distinct([value for unit in units for value in unit])
    if len(distinct) > 1:
        status = "inconsistent"
    elif present == 0:
        status = "missing"
    elif present < total:
        status = "partial"
    else:
        status = "complete"
    return _value(distinct), {
        "status": status,
        "present": present,
        "total": total,
        "coverage": f"{present}/{total}",
        "values": distinct,
        "source": source,
    }


def _root_value(config: dict[str, Any], field: str) -> Any:
    if field == "dataset_id" or field == "dataset_hash":
        datasets = config.get("datasets") or []
        dataset = datasets[0] if datasets and isinstance(datasets[0], dict) else {}
        return dataset.get("name" if field == "dataset_id" else "ref")
    if field == "budget":
        return config.get("n_attempts")
    if field == "concurrency":
        return config.get("n_concurrent_trials")
    if field == "agent_name":
        return _agent_name(config)
    if field == "model":
        agents = config.get("agents") or []
        agent = agents[0] if agents and isinstance(agents[0], dict) else {}
        return agent.get("model_name")
    if field == "timeout_multiplier":
        return next(
            (config.get(key) for key in ("agent_timeout_multiplier", "timeout_multiplier") if config.get(key) is not None),
            None,
        )
    if field == "tool_strategy":
        return _value(_distinct(_walk_values(config, {"tool_strategy", "tool_policy"})))
    if field == "candidate_commit":
        return _value(_distinct(_walk_values(config, {"candidate_commit", "candidate_commit_sha"})))
    return None


def collect_fingerprint_details(job_dir: Path) -> dict[str, Any]:
    """Derive values plus evidence coverage and consistency from Harbor artifacts."""
    # Import locally to reuse Harbor's timestamp-directory selection without a
    # second implementation or an import cycle during module initialization.
    from .compare import latest_run

    run = latest_run(job_dir)
    config = _read_json(run / "config.json")
    run_result = _read_json(run / "result.json")
    trials = _trial_files(run)
    total_trials = _run_total(run_result, trials)
    trial_configs = [trial_config for _, trial_config, _ in trials]
    results = [result for _, _, result in trials]
    adapters = [_read_json(trial / "agent" / "stella" / "result.json") for trial, _, _ in trials]

    values: dict[str, Any] = {}
    evidence: dict[str, dict[str, Any]] = {}
    for field in FINGERPRINT_FIELDS:
        root = _root_value(config, field)
        if root is not None:
            value, info = _summarize_units([[root]], 1, FINGERPRINT_SOURCES[field])
        elif field in {"dataset_id", "dataset_hash"}:
            key = "source" if field == "dataset_id" else "dataset_hash"
            units = [
                _distinct([cfg.get("task", {}).get(key)])
                if isinstance(cfg.get("task"), dict) else []
                for cfg in trial_configs
            ]
            value, info = _summarize_units(units, total_trials, FINGERPRINT_SOURCES[field])
        else:
            units = [
                _trial_values(field, cfg, result, adapter)
                for cfg, result, adapter in zip(trial_configs, results, adapters)
            ]
            value, info = _summarize_units(units, total_trials, FINGERPRINT_SOURCES[field])
        values[field] = value
        evidence[field] = info

    return {"fingerprint": values, "evidence": evidence}


def collect_fingerprint(job_dir: Path) -> dict[str, Any]:
    """Return only fingerprint values, for callers that do not need diagnostics."""
    return collect_fingerprint_details(job_dir)["fingerprint"]


def _fallback_evidence(fingerprint: dict[str, Any]) -> dict[str, dict[str, Any]]:
    return {
        field: {
            "status": "complete" if fingerprint.get(field) is not None else "missing",
            "present": 1 if fingerprint.get(field) is not None else 0,
            "total": 1,
            "coverage": "1/1" if fingerprint.get(field) is not None else "0/1",
            "values": [fingerprint[field]] if fingerprint.get(field) is not None else [],
            "source": FINGERPRINT_SOURCES[field],
        }
        for field in FINGERPRINT_FIELDS
    }


def _is_complete(info: dict[str, Any]) -> bool:
    return info.get("status") == "complete"


def _issue(kind: str, field: str, left: dict[str, Any], right: dict[str, Any], reject: bool, *, source: str | None = None) -> dict[str, Any]:
    result = {
        "kind": kind,
        "field": field,
        "left": left.get("fingerprint", {}).get(field),
        "right": right.get("fingerprint", {}).get(field),
        "left_evidence": left.get("evidence", {}).get(field) or {},
        "right_evidence": right.get("evidence", {}).get(field) or {},
        "reject": reject,
    }
    if source is not None:
        result["source"] = source
    return result


def fingerprint_mismatches(
    left: dict[str, Any],
    right: dict[str, Any],
    left_evidence: dict[str, dict[str, Any]] | None = None,
    right_evidence: dict[str, dict[str, Any]] | None = None,
) -> list[dict[str, Any]]:
    """Return hard mismatches and non-blocking identity diagnostics.

    Condition fields are fail-closed. Agent identity fields are diagnostics for
    cross-agent comparisons, except an internally inconsistent run, which is
    never safe to compare.
    """
    left_bundle = {"fingerprint": left, "evidence": left_evidence or _fallback_evidence(left)}
    right_bundle = {"fingerprint": right, "evidence": right_evidence or _fallback_evidence(right)}
    left_values, right_values = left_bundle["fingerprint"], right_bundle["fingerprint"]
    left_info, right_info = left_bundle["evidence"], right_bundle["evidence"]
    issues: list[dict[str, Any]] = []

    internal_fields: set[str] = set()
    for field in FINGERPRINT_FIELDS:
        if left_info[field].get("status") == "inconsistent" or right_info[field].get("status") == "inconsistent":
            internal_fields.add(field)
            issues.append(_issue("internal", field, left_bundle, right_bundle, True, source=FINGERPRINT_SOURCES[field]))

    same_agent = (
        _is_complete(left_info["agent_name"])
        and _is_complete(right_info["agent_name"])
        and left_values.get("agent_name") == right_values.get("agent_name")
    )
    for field in CONDITION_FIELDS:
        if field in internal_fields:
            continue
        if not _is_complete(left_info[field]) or not _is_complete(right_info[field]):
            issues.append(_issue("unverifiable", field, left_bundle, right_bundle, True, source=FINGERPRINT_SOURCES[field]))
        elif left_values.get(field) != right_values.get(field):
            issues.append(_issue("different", field, left_bundle, right_bundle, True))

    for field in AGENT_FIELDS:
        if field == "agent_name":
            if field not in internal_fields and (not _is_complete(left_info[field]) or not _is_complete(right_info[field])):
                issues.append(_issue("agent_incomplete", field, left_bundle, right_bundle, False, source=FINGERPRINT_SOURCES[field]))
            continue
        if field in internal_fields:
            continue
        if not _is_complete(left_info[field]) or not _is_complete(right_info[field]):
            issues.append(_issue("agent_incomplete", field, left_bundle, right_bundle, False, source=FINGERPRINT_SOURCES[field]))
        elif same_agent and field != "candidate_commit" and left_values.get(field) != right_values.get(field):
            issues.append(_issue("different", field, left_bundle, right_bundle, True))
    return issues


def comparison_mode(left: dict[str, Any], right: dict[str, Any], evidence: tuple[dict[str, Any], dict[str, Any]]) -> str:
    left_info, right_info = evidence
    if not _is_complete(left_info.get("agent_name", {})) or not _is_complete(right_info.get("agent_name", {})):
        return "agent identity unavailable"
    return "same-agent" if left.get("agent_name") == right.get("agent_name") else "cross-agent"


def format_value(value: Any) -> str:
    return json.dumps(value, sort_keys=True, ensure_ascii=False)


def _side(value: Any, evidence: dict[str, Any]) -> str:
    return f"{format_value(value)} [{evidence.get('coverage', '?/?')}]"


def format_mismatches(mismatches: list[dict[str, Any]]) -> list[str]:
    lines: list[str] = []
    groups = (
        ("treatment", "DECLARED TREATMENT DIFFERENCE:"),
        ("different", "CONFIGURATION DIFFERENT:"),
        ("unverifiable", "CANNOT VERIFY CONFIGURATION:"),
        ("internal", "INTERNALLY INCONSISTENT RUN:"),
        ("asymmetric", "TOP-UP EVIDENCE RECORDED ON ONE SIDE ONLY:"),
        ("agent_incomplete", "AGENT IDENTITY INCOMPLETE (reported, not blocking):"),
        ("unrecorded", "IDENTITY NEVER RECORDED (reported, not blocking):"),
        ("coverage", "IDENTITY PARTIALLY COVERED (reported, not blocking):"),
    )
    for kind, title in groups:
        items = [item for item in mismatches if item["kind"] == kind]
        if not items:
            continue
        lines.append(title)
        for item in items:
            # An issue may carry its own wording when a value pair would say
            # less than the sentence does.
            if item.get("line"):
                lines.append(f"  - {item['line']}")
                continue
            left = _side(item["left"], item["left_evidence"])
            right = _side(item["right"], item["right_evidence"])
            source = f"; expected at {item['source']}" if item.get("source") else ""
            lines.append(f"  - {item['field']}: left={left}; right={right}{source}")
    return lines


class FingerprintMismatchError(ValueError):
    """Raised when comparison contains any blocking fingerprint issue."""

    def __init__(self, mismatches: list[dict[str, Any]]) -> None:
        self.mismatches = mismatches
        detail = "\n".join(format_mismatches(mismatches))
        super().__init__("REFUSING COMPARISON: fingerprint validation failed\n" + detail)
