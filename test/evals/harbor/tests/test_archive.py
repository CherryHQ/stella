import hashlib
import json
from pathlib import Path

import pytest

from stella_harbor.archive import build_archive


NONCE = "0123456789abcdef0123456789abcdef"


def _write_trial(job: Path, name: str, *, reward, valid=True, trajectory=None):
    trial = job / "2026-08-20__00-00-00" / f"{name}__abc"
    (trial / "agent" / "stella").mkdir(parents=True)
    (trial / "config.json").write_text(
        json.dumps({"trial_name": f"{name}__abc", "agent": {"name": "stella"}})
    )
    (trial / "result.json").write_text(
        json.dumps({"verifier_result": {"rewards": {"reward": reward}}})
    )
    (trial / "agent" / "stella" / "result.json").write_text(
        json.dumps({"valid": valid, "bridge_nonce": NONCE})
    )
    if trajectory is not None:
        (trial / "trajectory.json").write_text(json.dumps(trajectory, ensure_ascii=False))
    return trial


def _manifest(output):
    return json.loads((output / "manifest.json").read_text())


def _trial(manifest, name):
    return next(item for item in manifest["trials"] if name in item["trial"])


def test_archive_defaults_to_public_payload_without_trajectories(tmp_path):
    job = tmp_path / "job"
    _write_trial(
        job,
        "fail",
        reward=0.0,
        trajectory={"messages": [{"text": "private failure context"}]},
    )
    output = tmp_path / "archive"

    manifest = build_archive(job, output)

    assert manifest["include_trajectories"] is False
    assert _trial(manifest, "fail")["trajectory"] == {
        "status": "disabled",
        "reason": "trajectory inclusion not requested",
    }
    assert not list(output.glob("**/trajectory.json"))
    assert all(Path(entry["path"]).name != "trajectory.json" for entry in manifest["files"])


def test_archive_keeps_results_and_configs_and_omits_pass_trajectory(tmp_path):
    job = tmp_path / "job"
    pass_trajectory = {"messages": [{"role": "assistant", "text": "passed"}]}
    fail_trajectory = {"messages": [{"role": "assistant", "text": "ordinary output"}]}
    _write_trial(job, "pass", reward=1.0, trajectory=pass_trajectory)
    _write_trial(job, "fail", reward=0.0, trajectory=fail_trajectory)
    _write_trial(
        job,
        "invalid",
        reward=1.0,
        valid=False,
        trajectory={"messages": [{"text": "invalid evidence"}]},
    )
    output = tmp_path / "archive"

    manifest = build_archive(job, output, include_trajectories=True)

    assert _trial(manifest, "pass")["trajectory"]["status"] == "omitted"
    assert _trial(manifest, "pass")["trajectory"]["reason"] == "pass"
    assert _trial(manifest, "pass")["trajectory"]["source_path"].endswith(
        "pass__abc/trajectory.json"
    )
    included = _trial(manifest, "fail")["trajectory"]
    assert included["status"] == "included"
    assert (output / included["path"]).exists()
    assert not list(output.glob("**/pass__*/trajectory.json"))
    assert (output / "2026-08-20__00-00-00/pass__abc/result.json").exists()
    assert (output / "2026-08-20__00-00-00/pass__abc/config.json").exists()
    assert (output / "2026-08-20__00-00-00/pass__abc/agent/stella/result.json").exists()
    assert _trial(manifest, "invalid")["classification"] == "invalid"
    assert _trial(manifest, "invalid")["trajectory"]["status"] == "included"

    for entry in manifest["files"]:
        path = output / entry["path"]
        assert hashlib.sha256(path.read_bytes()).hexdigest() == entry["sha256"]
    assert "manifest.json" in (output / "SHA256SUMS").read_text()


def test_archive_redacts_known_credential_shapes_and_bridge_nonce(tmp_path):
    job = tmp_path / "job"
    high_entropy = "Aa1Bb2Cc3Dd4Ee5Ff6Gg7Hh8Ii9Jj0Kk1Ll2Mm3Nn4Oo5Pp6"
    trajectory = {
        "messages": [
            {
                "text": (
                    "password=supersecretvalue "
                    "ghp_abcdefghijklmnopqrstuvwxyz123456 "
                    "github_pat_abcdefghijklmnopqrstuvwxyz123456 "
                    "sk-abcdefghijklmnopqrstuvwxyz123456 "
                    "postgres://user:correct-horse-battery-staple@db.example/app "
                    "Authorization: Bearer bearer-token-123456 "
                    "Authorization: Basic dXNlcjpwYXNz "
                    "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0.signature123 "
                    "-----BEGIN RSA PRIVATE KEY----- "
                    f"{high_entropy} {NONCE}"
                )
            }
        ]
    }
    trial = _write_trial(job, "known-secrets", reward=0.0, trajectory=trajectory)
    original = (trial / "trajectory.json").read_bytes()
    output = tmp_path / "archive"

    manifest = build_archive(job, output, include_trajectories=True)

    record = _trial(manifest, "known-secrets")["trajectory"]
    assert record["status"] == "included"
    assert (trial / "trajectory.json").read_bytes() == original
    redacted = json.loads((output / record["path"]).read_text())
    text = redacted["messages"][0]["text"]
    for secret in [
        "supersecretvalue",
        "ghp_abcdefghijklmnopqrstuvwxyz123456",
        "github_pat_abcdefghijklmnopqrstuvwxyz123456",
        "sk-abcdefghijklmnopqrstuvwxyz123456",
        "correct-horse-battery-staple",
        "bearer-token-123456",
        "dXNlcjpwYXNz",
        "eyJhbGciOiJIUzI1NiJ9",
        "-----BEGIN RSA PRIVATE KEY-----",
        high_entropy,
        NONCE,
    ]:
        assert secret not in text
    assert text.count("[redacted_secret]") >= 11


def test_archive_does_not_mistake_normal_content_for_a_credential(tmp_path):
    job = tmp_path / "job"
    trajectory = {
        "messages": [
            {
                "text": "The API key is rotated monthly; token_count: 12; no secret is shown."
            }
        ]
    }
    _write_trial(job, "normal", reward=0.0, trajectory=trajectory)
    output = tmp_path / "archive"

    manifest = build_archive(job, output, include_trajectories=True)

    record = _trial(manifest, "normal")["trajectory"]
    assert record["status"] == "included"
    assert json.loads((output / record["path"]).read_text()) == trajectory
    assert record["redactions"] == 0


def test_archive_excludes_unknown_credential_shape_fail_closed(tmp_path):
    job = tmp_path / "job"
    trajectory = {"messages": [{"tool_result": {"token": "mystery-token"}}]}
    _write_trial(job, "unknown", reward=0.0, trajectory=trajectory)
    output = tmp_path / "archive"

    manifest = build_archive(job, output, include_trajectories=True)

    record = _trial(manifest, "unknown")["trajectory"]
    assert record["status"] == "excluded"
    assert record["reason"] == "unclassified credential shape"
    assert record["locations"] == ["$.messages[0].tool_result.token:credential-field"]
    assert not list(output.glob("**/unknown__*/trajectory.json"))


def test_payload_scan_ignores_benchmark_fixtures_and_paths(tmp_path):
    job = tmp_path / "job"
    trial = _write_trial(job, "fixtures", reward=0.0)
    (trial / "result.json").write_text(
        json.dumps(
            {
                "bridge_ledger": [
                    {"command": "PASSWORD=[A-Z0-9]{23}"},
                    {"command": "https://[^[:space:]]+@"},
                    {"path": "/tmp/Aa1Bb2Cc3Dd4Ee5Ff6Gg7Hh8Ii9Jj0Kk1Ll2Mm3Nn4Oo5Pp6"},
                    {"token": "ghp_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789"},
                ]
            }
        )
    )
    output = tmp_path / "archive"

    manifest = build_archive(job, output)

    assert manifest["include_trajectories"] is False


def test_payload_scan_aborts_without_writing_output_on_real_credential(tmp_path):
    job = tmp_path / "job"
    trial = _write_trial(job, "leak", reward=0.0)
    (trial / "result.json").write_text(
        json.dumps({"agent_result": {"metadata": {"api_key": "real-secret-value"}}})
    )
    output = tmp_path / "archive"

    with pytest.raises(ValueError, match="credential-shaped payload content"):
        build_archive(job, output)

    assert not output.exists()
