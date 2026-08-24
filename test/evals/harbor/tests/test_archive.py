import hashlib
import json
from pathlib import Path

import pytest

from stella_harbor.archive import build_archive


NONCE = "0123456789abcdef0123456789abcdef"


def _write_trial(job: Path, name: str, *, reward, valid=True, trajectory=None, pi_stream=None):
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
    if pi_stream is not None:
        payload = pi_stream if isinstance(pi_stream, bytes) else "".join(
            json.dumps(event, ensure_ascii=False) + "\n" for event in pi_stream
        ).encode("utf-8")
        (trial / "agent" / "pi.txt").write_bytes(payload)
    return trial


def _manifest(output):
    return json.loads((output / "manifest.json").read_text())


def _trial(manifest, name):
    return next(item for item in manifest["trials"] if name in item["trial"])


def _transcript(manifest, name, kind="stella"):
    return next(entry for entry in _trial(manifest, name)["transcripts"] if entry["kind"] == kind)


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
    disabled = _transcript(manifest, "fail")
    assert disabled["status"] == "disabled"
    assert disabled["reason"] == "transcript inclusion not requested"
    assert not list(output.glob("**/trajectory.json"))
    assert all(Path(entry["path"]).name != "trajectory.json" for entry in manifest["files"])


def test_archive_keeps_results_and_configs_and_includes_every_verdict(tmp_path):
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

    # A passing trial is the evidence for how it passed; the verdict never
    # decided how safe a transcript was, redaction does.
    passed = _transcript(manifest, "pass")
    assert passed["status"] == "included"
    assert json.loads((output / passed["path"]).read_text()) == pass_trajectory
    included = _transcript(manifest, "fail")
    assert included["status"] == "included"
    assert (output / included["path"]).exists()
    assert (output / "2026-08-20__00-00-00/pass__abc/result.json").exists()
    assert (output / "2026-08-20__00-00-00/pass__abc/config.json").exists()
    assert (output / "2026-08-20__00-00-00/pass__abc/agent/stella/result.json").exists()
    assert _trial(manifest, "invalid")["classification"] == "invalid"
    assert _transcript(manifest, "invalid")["status"] == "included"

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

    record = _transcript(manifest, "known-secrets")
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

    record = _transcript(manifest, "normal")
    assert record["status"] == "included"
    assert json.loads((output / record["path"]).read_text()) == trajectory
    assert record["redactions"] == 0


def test_archive_drops_an_unknown_credential_shape_and_keeps_the_rest(tmp_path):
    """Fail closed per value: an unclassifiable secret cannot be trimmed, so it goes whole."""
    job = tmp_path / "job"
    trajectory = {"messages": [{"text": "kept"}, {"tool_result": {"token": "mystery-token"}}]}
    _write_trial(job, "unknown", reward=0.0, trajectory=trajectory)
    output = tmp_path / "archive"

    manifest = build_archive(job, output, include_trajectories=True)

    record = _transcript(manifest, "unknown")
    assert record["status"] == "included"
    assert record["dropped"] == ["$.messages[1].tool_result.token:credential-field"]
    saved = json.loads((output / record["path"]).read_text())
    assert saved["messages"][0]["text"] == "kept"
    assert saved["messages"][1]["tool_result"]["token"] == "[redacted_secret]"


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


def test_payload_credentials_are_redacted_rather_than_refused(tmp_path):
    """Benchmark tasks put passwords in the agent's own commands, and those live in result.json."""
    job = tmp_path / "job"
    trial = _write_trial(job, "leak", reward=0.0)
    (trial / "result.json").write_text(
        json.dumps({
            "verifier_result": {"rewards": {"reward": 0.0}},
            "agent_result": {"metadata": {"api_key": "real-secret-value"}},
            "bridge_ledger": [{"command": "echo done"}],
        })
    )
    output = tmp_path / "archive"

    manifest = build_archive(job, output)

    saved = json.loads((output / "2026-08-20__00-00-00/leak__abc/result.json").read_text())
    assert saved["agent_result"]["metadata"]["api_key"] == "[redacted_secret]"
    assert saved["bridge_ledger"][0]["command"] == "echo done"
    entry = next(f for f in manifest["files"] if f["path"].endswith("leak__abc/result.json"))
    assert entry["redactions"] == 1
    assert entry["source_sha256"]


def test_archive_still_refuses_a_payload_it_cannot_parse(tmp_path):
    job = tmp_path / "job"
    trial = _write_trial(job, "broken", reward=0.0)
    (trial / "result.json").write_text("{not json")
    output = tmp_path / "archive"

    with pytest.raises(ValueError, match="invalid JSON"):
        build_archive(job, output)

    assert not output.exists()


def test_archive_includes_a_redacted_pi_stream(tmp_path):
    """pi writes JSON lines, not one document; both agents must archive alike."""
    job = tmp_path / "job"
    _write_trial(
        job,
        "pi-run",
        reward=1.0,
        pi_stream=[
            {"type": "message_end", "message": {"role": "assistant", "text": "ran ls"}},
            {"type": "message_end", "message": {"role": "assistant",
                                                "text": "export API_KEY=sk-abcdefghijklmnopqrstuvwxyz123456"}},
        ],
    )
    output = tmp_path / "archive"

    manifest = build_archive(job, output, include_trajectories=True)

    record = _transcript(manifest, "pi-run", kind="pi")
    assert record["status"] == "included"
    assert record["path"].endswith("pi-run__abc/agent/pi.txt")
    lines = (output / record["path"]).read_text().splitlines()
    assert len(lines) == 2
    assert json.loads(lines[0])["message"]["text"] == "ran ls"
    assert "sk-abcdefghijklmnopqrstuvwxyz123456" not in lines[1]
    assert record["redactions"] >= 1


def test_archive_excludes_a_pi_stream_it_cannot_parse(tmp_path):
    """The run that produced this bug wrote a half-line; raw copy is not the answer."""
    job = tmp_path / "job"
    complete = json.dumps({"type": "message_end"}, ensure_ascii=False).encode("utf-8")
    _write_trial(job, "truncated", reward=0.0,
                 pi_stream=complete + b"\n" + '{"type":"message_end","text":"中'.encode("utf-8")[:-1])
    output = tmp_path / "archive"

    manifest = build_archive(job, output, include_trajectories=True)

    record = _transcript(manifest, "truncated", kind="pi")
    assert record["status"] == "excluded"
    assert record["reason"] == "transcript is not valid UTF-8"
    assert record["source_sha256"]
    assert not list(output.glob("**/pi.txt"))


def test_archive_records_both_transcripts_when_a_trial_has_two(tmp_path):
    job = tmp_path / "job"
    _write_trial(job, "both", reward=0.0, trajectory={"messages": []},
                 pi_stream=[{"type": "message_end"}])
    output = tmp_path / "archive"

    manifest = build_archive(job, output, include_trajectories=True)

    kinds = {entry["kind"]: entry["status"] for entry in _trial(manifest, "both")["transcripts"]}
    assert kinds == {"stella": "included", "pi": "included"}
