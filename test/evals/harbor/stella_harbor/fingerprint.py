"""Extract and compare the configuration identity of a Harbor run."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

FINGERPRINT_FIELDS = (
    "dataset_id",
    "dataset_hash",
    "model",
    "budget",
    "concurrency",
    "timeout_multiplier",
    "tool_strategy",
    "capability_profile_digest",
    "candidate_commit",
)

# A candidate commit is deliberately excluded from compatibility checks: the
# whole point of this comparison is often to measure a new candidate against a
# control run. Every other field must describe the same evaluation conditions.
COMPARISON_FIELDS = tuple(field for field in FINGERPRINT_FIELDS if field != "candidate_commit")

FINGERPRINT_SOURCES = {
    "dataset_id": "run config.json: datasets[].name",
    "dataset_hash": "run config.json: datasets[].ref",
    "model": "driver result.json: model",
    "budget": "run config.json: n_attempts",
    "concurrency": "run config.json: n_concurrent_trials",
    "timeout_multiplier": "effective trial config: agent_timeout_multiplier or timeout_multiplier",
    "tool_strategy": "run/trial config: tool_strategy or tool_policy",
    "capability_profile_digest": "driver result.json: capability_profile_digest",
    "candidate_commit": "driver result.json: candidate_commit",
}


def _read_json(path: Path) -> dict[str, Any]:
    if not path.exists():
        return {}
    value = json.loads(path.read_text())
    return value if isinstance(value, dict) else {}


def _unique(values: list[Any]) -> Any:
    """Return one observed value, or all values when a run is internally mixed."""
    distinct: list[Any] = []
    for value in values:
        if value is None or value in distinct:
            continue
        distinct.append(value)
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
    trials: list[tuple[Path, dict[str, Any], dict[str, Any]]] = []
    for trial in sorted(p for p in run.iterdir() if p.is_dir()):
        result = trial / "result.json"
        if not result.exists():
            continue
        config = _read_json(trial / "config.json")
        result_data = _read_json(result)
        # Older Harbor exports put the complete effective trial config in the
        # trial result while config.json contains only the task identity.
        effective_config = result_data.get("config")
        if isinstance(effective_config, dict):
            config = {**effective_config, **config}
        trials.append((trial, config, result_data))
    return trials


def collect_fingerprint(job_dir: Path) -> dict[str, Any]:
    """Derive a run fingerprint from the job's persisted Harbor artifacts.

    Missing values remain ``None``. They are not guessed from the current
    checkout or from a human-maintained note, because doing so would turn an
    historical run into a false claim about its configuration.
    """
    # Import locally to keep fingerprint extraction usable without creating a
    # second implementation of Harbor's timestamp-directory selection.
    from .compare import latest_run

    run = latest_run(job_dir)
    config = _read_json(run / "config.json")
    trials = _trial_files(run)
    trial_configs = [trial_config for _, trial_config, _ in trials]
    results = [result for _, _, result in trials]
    adapter_results = [
        _read_json(trial / "agent" / "stella" / "result.json")
        for trial, _, _ in trials
    ]

    datasets = config.get("datasets") or []
    dataset = datasets[0] if datasets and isinstance(datasets[0], dict) else {}
    dataset_id = dataset.get("name")
    dataset_hash = dataset.get("ref")
    if dataset_id is None:
        dataset_id = _unique([cfg.get("task", {}).get("source") for cfg in trial_configs if isinstance(cfg.get("task"), dict)])
    if dataset_hash is None:
        dataset_hash = _unique([cfg.get("task", {}).get("dataset_hash") for cfg in trial_configs if isinstance(cfg.get("task"), dict)])

    agent_configs = [
        config.get("agents", [{}])[0] if config.get("agents") else {},
        *[cfg.get("agent") or {} for cfg in trial_configs],
    ]
    model = _unique([
        agent.get("model_name") for agent in agent_configs if isinstance(agent, dict)
    ])
    if model is None:
        model = _unique([
            result.get("model") for result in results if isinstance(result.get("model"), str)
        ])
    if model is None:
        model = _unique([
            _model_from_info(result.get("agent_info", {}).get("model_info"))
            for result in results
            if isinstance(result.get("agent_info"), dict)
        ])

    timeout_values: list[Any] = [
        config.get("agent_timeout_multiplier"),
        config.get("timeout_multiplier"),
    ]
    for trial_config in trial_configs:
        timeout_values.extend([
            trial_config.get("agent_timeout_multiplier"),
            trial_config.get("timeout_multiplier"),
        ])

    explicit_tool_strategy = _walk_values(
        {"config": config, "trials": trial_configs},
        {"tool_strategy", "tool_policy"},
    )
    tool_strategy = _unique(explicit_tool_strategy)
    if tool_strategy is None:
        # Harbor persists the adapter name but not a generic tool allow/deny
        # policy. Keep that observable proxy so Stella and Pi cannot silently
        # compare as if they used the same tool strategy.
        tool_strategy = _agent_name(config)

    capability_digests = _walk_values(
        {"results": results, "adapter_results": adapter_results},
        {"capability_profile_digest"},
    )
    candidate_commits = _walk_values(
        {"config": config, "trials": trial_configs, "results": results, "adapter_results": adapter_results},
        {"candidate_commit", "candidate_commit_sha"},
    )

    return {
        "dataset_id": dataset_id,
        "dataset_hash": dataset_hash,
        "model": model,
        "budget": config.get("n_attempts"),
        "concurrency": config.get("n_concurrent_trials"),
        "timeout_multiplier": _unique(timeout_values),
        "tool_strategy": tool_strategy,
        "capability_profile_digest": _unique(capability_digests),
        "candidate_commit": _unique(candidate_commits),
    }


def fingerprint_mismatches(
    left: dict[str, Any], right: dict[str, Any]
) -> list[dict[str, Any]]:
    """Return configuration differences and fields whose identity is unknown.

    Missing values are not equal values. Candidate commits may differ, but a
    missing candidate commit still makes the comparison unverifiable.
    """
    issues: list[dict[str, Any]] = []
    for field in FINGERPRINT_FIELDS:
        left_value, right_value = left.get(field), right.get(field)
        if left_value is None or right_value is None:
            issues.append({
                "kind": "unverifiable",
                "field": field,
                "left": left_value,
                "right": right_value,
                "source": FINGERPRINT_SOURCES[field],
            })
        elif field in COMPARISON_FIELDS and left_value != right_value:
            issues.append({
                "kind": "different",
                "field": field,
                "left": left_value,
                "right": right_value,
            })
    return issues


def format_value(value: Any) -> str:
    return json.dumps(value, sort_keys=True, ensure_ascii=False)


def format_mismatches(mismatches: list[dict[str, Any]]) -> list[str]:
    lines: list[str] = []
    different = [item for item in mismatches if item["kind"] == "different"]
    unverifiable = [item for item in mismatches if item["kind"] == "unverifiable"]
    if different:
        lines.append("CONFIGURATION DIFFERENT:")
        lines.extend(
            f"  - {item['field']}: left={format_value(item['left'])}; right={format_value(item['right'])}"
            for item in different
        )
    if unverifiable:
        lines.append("CANNOT VERIFY CONFIGURATION:")
        lines.extend(
            f"  - {item['field']}: left={format_value(item['left'])}; "
            f"right={format_value(item['right'])}; expected at {item['source']}"
            for item in unverifiable
        )
    return lines


class FingerprintMismatchError(ValueError):
    """Raised when comparison would mix incompatible run configurations."""

    def __init__(self, mismatches: list[dict[str, Any]]) -> None:
        self.mismatches = mismatches
        detail = "\n".join(format_mismatches(mismatches))
        super().__init__("REFUSING COMPARISON: fingerprint validation failed\n" + detail)
