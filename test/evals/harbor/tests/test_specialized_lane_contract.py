import json

from stella_harbor.compare import main
from stella_harbor.fingerprint import collect_fingerprint_details, fingerprint_mismatches

from test_compare import FINGERPRINT_ADAPTER, RUN, write_lock, write_run_config


SPECIALIZED_TASKS = (
    "stella-specialized/skill-bash-guard",
    "stella-specialized/memory-library-evidence",
    "stella-specialized/mcp-recally",
)
LANE_FIELDS = (
    "provider_surface_digest",
    "runtime_specialized_catalog_digest",
    "capability_profile_digest",
)


def write_specialized_lane_job(tmp_path, name):
    job = tmp_path / name
    write_run_config(job, RUN, n_attempts=3, n_concurrent_trials=1)
    run = job / RUN
    (run / "result.json").write_text(json.dumps({"n_total_trials": len(SPECIALIZED_TASKS)}))
    for index, task in enumerate(SPECIALIZED_TASKS):
        trial = run / f"trial-{index}"
        (trial / "agent" / "stella").mkdir(parents=True)
        (trial / "config.json").write_text(json.dumps({"task": {"name": task}}))
        (trial / "result.json").write_text(json.dumps({
            "verifier_result": {"rewards": {"reward": 1.0}},
            "agent_result": {"cost_usd": 0.01, "n_input_tokens": 10, "n_output_tokens": 5},
        }))
        (trial / "agent" / "stella" / "result.json").write_text(json.dumps(
            FINGERPRINT_ADAPTER | {"valid": True}
        ))
    write_lock(job, RUN, list(SPECIALIZED_TASKS))
    return job


def adapter_path(job, index):
    return job / RUN / f"trial-{index}" / "agent" / "stella" / "result.json"


def test_specialized_tasks_have_exactly_one_lane_wide_catalog_contract(tmp_path):
    job = write_specialized_lane_job(tmp_path, "specialized")

    details = collect_fingerprint_details(job)

    for field in LANE_FIELDS:
        evidence = details["evidence"][field]
        assert evidence["status"] == "complete"
        assert evidence["coverage"] == "3/3"
        assert evidence["required"] is True
    assert details["fingerprint"]["provider_surface_digest"] == "sha256:surface-a"
    assert details["fingerprint"]["runtime_specialized_catalog_digest"] == "sha256:catalog-a"
    assert details["fingerprint"]["capability_profile_digest"] == "capability-a"


def test_specialized_catalog_missing_from_any_task_refuses_by_default(tmp_path, capsys):
    candidate = write_specialized_lane_job(tmp_path, "candidate")
    reference = write_specialized_lane_job(tmp_path, "reference")
    adapter = json.loads(adapter_path(candidate, 1).read_text())
    adapter.pop("runtime_specialized_catalog_digest")
    adapter_path(candidate, 1).write_text(json.dumps(adapter))

    details = collect_fingerprint_details(candidate)
    assert details["evidence"]["runtime_specialized_catalog_digest"]["status"] == "partial"
    assert details["evidence"]["runtime_specialized_catalog_digest"]["required"] is True
    assert main([str(candidate), str(reference)]) == 2
    assert "runtime_specialized_catalog_digest" in capsys.readouterr().err


def test_specialized_catalog_mismatch_in_any_task_is_an_internal_failure(tmp_path):
    job = write_specialized_lane_job(tmp_path, "specialized")
    adapter = json.loads(adapter_path(job, 2).read_text())
    adapter["provider_surface_digest"] = "sha256:other-surface"
    adapter_path(job, 2).write_text(json.dumps(adapter))

    details = collect_fingerprint_details(job)
    issues = fingerprint_mismatches(
        details["fingerprint"], details["fingerprint"], details["evidence"], details["evidence"]
    )

    assert any(issue["kind"] == "internal" and issue["field"] == "provider_surface_digest" and issue["reject"] for issue in issues)
