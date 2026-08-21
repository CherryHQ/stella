"""Create a reviewable, fail-closed Harbor evidence archive.

Usage:
    python -m stella_harbor.archive <job> --output <directory>

The source job is never changed. Only result/config files and redacted
trajectories for non-passing or invalid trials are copied.
"""

from __future__ import annotations

import argparse
import base64
import binascii
import hashlib
import json
import re
import shutil
from pathlib import Path
from typing import Any

REDACTION_RULES_VERSION = "stella-reflect-secret-v1"
REDACTION_PLACEHOLDER = "[redacted_secret]"

_PRIVATE_KEY = re.compile(r"-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----", re.IGNORECASE)
_TOKEN_PREFIX = re.compile(
    r"\b(?:ghp_[a-z0-9_]{16,}|github_pat_[a-z0-9_]{16,}|sk-[a-z0-9_-]{16,})\b",
    re.IGNORECASE,
)
_ASSIGNMENT = re.compile(
    r"\b(?:password|passwd|secret|api[_-]?key|access[_-]?token|refresh[_-]?token)"
    r"\b\s*[:=]\s*[\"']?[^\s\"']{8,}",
    re.IGNORECASE,
)
_URL_USERINFO = re.compile(r"(\b[a-z][a-z0-9+.-]*://)[^\s/@]+@", re.IGNORECASE)
_AUTHORIZATION = re.compile(
    r"(\bauthorization\s*:\s*)(bearer|basic)(\s+)([^\s\"'<>]+)",
    re.IGNORECASE,
)
_JWT = re.compile(
    r"\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\b"
)
_LONG_TOKEN = re.compile(r"[A-Za-z0-9+/=_-]{48,}")
_UNKNOWN_ASSIGNMENT = re.compile(
    r"\b(password|passwd|secret|api[_-]?key|access[_-]?token|refresh[_-]?token|token)"
    r"\b\s*[:=]\s*[\"']?([^\s\"']*)",
    re.IGNORECASE,
)

_PLACEHOLDER_VALUES = {"", "none", "null", "unset", "<none>", "[redacted_secret]"}


def _looks_high_entropy(token: str) -> bool:
    if len(token) < 48:
        return False
    if not (
        any(char.islower() for char in token)
        and any(char.isupper() for char in token)
        and any(char.isdigit() for char in token)
    ):
        return False
    return len(set(token)) / len(token) > 0.35 and "..." not in token


def _looks_like_basic_credential(token: str) -> bool:
    for padding in ("", "=" * ((4 - len(token) % 4) % 4)):
        try:
            decoded = base64.b64decode(token + padding, validate=True)
        except (ValueError, binascii.Error):
            continue
        if b":" in decoded:
            return True
    return False


def _replace_known(text: str, nonce: str) -> tuple[str, int]:
    sanitized = text
    replacements = 0

    if nonce:
        sanitized, count = re.subn(re.escape(nonce), REDACTION_PLACEHOLDER, sanitized)
        replacements += count

    def replace(pattern: re.Pattern[str], replacement: str | None = None) -> None:
        nonlocal sanitized, replacements
        new, count = pattern.subn(replacement or REDACTION_PLACEHOLDER, sanitized)
        sanitized = new
        replacements += count

    replace(_URL_USERINFO, r"\1" + REDACTION_PLACEHOLDER + "@")

    def replace_authorization(match: re.Match[str]) -> str:
        token = match.group(4)
        if not re.fullmatch(r"[A-Za-z0-9\-._~+/]+={0,}", token):
            return match.group(0)
        if match.group(2).lower() == "basic" and not _looks_like_basic_credential(token):
            return match.group(0)
        return match.group(1) + match.group(2) + match.group(3) + REDACTION_PLACEHOLDER

    def replace_authorization_counted(match: re.Match[str]) -> str:
        nonlocal replacements
        replacement = replace_authorization(match)
        if replacement != match.group(0):
            replacements += 1
        return replacement

    sanitized = _AUTHORIZATION.sub(replace_authorization_counted, sanitized)
    replace(_JWT)
    replace(_PRIVATE_KEY)
    replace(_TOKEN_PREFIX)
    replace(_ASSIGNMENT)

    def replace_long_token(match: re.Match[str]) -> str:
        nonlocal replacements
        if not _looks_high_entropy(match.group(0)):
            return match.group(0)
        replacements += 1
        return REDACTION_PLACEHOLDER

    sanitized = _LONG_TOKEN.sub(replace_long_token, sanitized)
    return sanitized, replacements


def _unknown_credential_shapes(text: str) -> list[str]:
    """Return locations of suspicious values not covered by known rules.

    The existing Go policy intentionally has a minimum length for secret
    assignments. Values shorter than that, malformed Authorization headers, and
    generic ``token=...`` fields are kept out rather than partially sanitized.
    """
    unknown: list[str] = []
    for match in _UNKNOWN_ASSIGNMENT.finditer(text):
        key = match.group(1).lower()
        value = match.group(2).strip()
        if not value or value.lower() in _PLACEHOLDER_VALUES or value.isdigit():
            continue
        if key == "token":
            _, known_replacements = _replace_known(value, "")
            if known_replacements == 0:
                unknown.append("credential-assignment")
        elif len(value) < 8:
            unknown.append("credential-assignment")

    for match in _AUTHORIZATION.finditer(text):
        token = match.group(4)
        valid_token68 = re.fullmatch(r"[A-Za-z0-9\-._~+/]+={0,}", token)
        if valid_token68 and (
            match.group(2).lower() != "basic" or _looks_like_basic_credential(token)
        ):
            continue
        if token.lower() not in _PLACEHOLDER_VALUES:
            unknown.append("authorization-header")

    return unknown


def _redact_value(value: str, nonce: str, field: str = "") -> tuple[str, int, list[str]]:
    unknown = _unknown_credential_shapes(value)
    sanitized, replacements = _replace_known(value, nonce)
    field_name = field.lower().replace("-", "_")
    secret_field = re.fullmatch(
        r"(?:password|passwd|secret|api_key|access_token|refresh_token)", field_name
    )
    if secret_field and value and value.lower() not in _PLACEHOLDER_VALUES:
        if replacements == 0 and len(value) >= 8:
            sanitized, replacements = REDACTION_PLACEHOLDER, 1
        elif len(value) < 8 and not value.isdigit():
            unknown.append("credential-field")
    elif field_name == "token" and value and value.lower() not in _PLACEHOLDER_VALUES:
        if replacements == 0 and not value.isdigit():
            unknown.append("credential-field")
    return sanitized, replacements, unknown


def _redact_json(
    value: Any, nonce: str, path: str = "$", field: str = ""
) -> tuple[Any, int, list[str]]:
    if isinstance(value, str):
        sanitized, replacements, unknown = _redact_value(value, nonce, field)
        return sanitized, replacements, [f"{path}:{reason}" for reason in unknown]
    if isinstance(value, list):
        output = []
        replacements = 0
        unknown: list[str] = []
        for index, item in enumerate(value):
            sanitized, count, reasons = _redact_json(item, nonce, f"{path}[{index}]")
            output.append(sanitized)
            replacements += count
            unknown.extend(reasons)
        return output, replacements, unknown
    if isinstance(value, dict):
        output: dict[str, Any] = {}
        replacements = 0
        unknown = []
        for key, item in value.items():
            sanitized, count, reasons = _redact_json(
                item, nonce, f"{path}.{key}", str(key)
            )
            output[key] = sanitized
            replacements += count
            unknown.extend(reasons)
        return output, replacements, unknown
    return value, 0, []


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _read_json(path: Path) -> Any | None:
    try:
        return json.loads(path.read_text())
    except (OSError, UnicodeError, json.JSONDecodeError):
        return None


def _trial_classification(trial_dir: Path, result: Any, adapter: Any | None) -> str:
    if adapter is not None and (
        adapter.get("valid") is False or adapter.get("predicate_violations")
    ):
        return "invalid"
    if not isinstance(result, dict):
        return "invalid"
    rewards = (result.get("verifier_result") or {}).get("rewards") or {}
    reward = rewards.get("reward")
    try:
        if float(reward) >= 1.0:
            return "pass"
    except (TypeError, ValueError):
        pass
    return "non-pass"


def _copy_payload(source: Path, output: Path, relative: Path) -> Path:
    target = output / relative
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source, target)
    return target


def _trajectory_source(trial_dir: Path) -> Path | None:
    for candidate in (trial_dir / "trajectory.json", trial_dir / "agent" / "stella" / "trajectory.json"):
        if candidate.is_file():
            return candidate
    return None


def build_archive(job: Path, output: Path) -> dict[str, Any]:
    job = job.expanduser().resolve()
    output = output.expanduser().resolve()
    if not job.is_dir():
        raise ValueError(f"job directory does not exist: {job}")
    if output == job or output.is_relative_to(job):
        raise ValueError("output directory must not be inside the source job")
    if output.exists() and any(output.iterdir()):
        raise ValueError(f"output directory is not empty: {output}")
    output.mkdir(parents=True, exist_ok=True)

    payload_paths: list[Path] = []
    for source in sorted(job.rglob("*")):
        if source.is_symlink():
            continue
        if source.is_file() and source.name in {"result.json", "config.json"}:
            payload_paths.append(_copy_payload(source, output, source.relative_to(job)))

    trials: list[dict[str, Any]] = []
    for config_path in sorted(job.rglob("config.json")):
        config = _read_json(config_path)
        if not isinstance(config, dict) or not config.get("trial_name"):
            continue
        trial_dir = config_path.parent
        result_path = trial_dir / "result.json"
        result = _read_json(result_path)
        adapter_path = trial_dir / "agent" / "stella" / "result.json"
        adapter = _read_json(adapter_path) if adapter_path.is_file() else None
        classification = _trial_classification(trial_dir, result, adapter)
        trajectory = _trajectory_source(trial_dir)
        record: dict[str, Any] = {
            "trial": str(trial_dir.relative_to(job)),
            "classification": classification,
            "trajectory": {"status": "omitted", "reason": "pass"}
            if classification == "pass"
            else {"status": "missing", "reason": "trajectory file not found"},
        }

        if classification == "pass" and trajectory is not None:
            record["trajectory"]["source_path"] = trajectory.relative_to(job).as_posix()

        if classification != "pass" and trajectory is not None:
            nonce = ""
            if isinstance(adapter, dict):
                nonce = str(adapter.get("bridge_nonce") or "")
            try:
                raw = json.loads(trajectory.read_text())
            except (OSError, UnicodeError, json.JSONDecodeError):
                record["trajectory"] = {
                    "status": "excluded",
                    "source_path": trajectory.relative_to(job).as_posix(),
                    "source_sha256": _sha256(trajectory),
                    "reason": "trajectory is not valid UTF-8 JSON",
                }
            else:
                redacted, replacements, unknown = _redact_json(raw, nonce)
                if unknown:
                    record["trajectory"] = {
                        "status": "excluded",
                        "source_path": trajectory.relative_to(job).as_posix(),
                        "source_sha256": _sha256(trajectory),
                        "reason": "unclassified credential shape",
                        "locations": sorted(set(unknown)),
                    }
                else:
                    relative = trajectory.relative_to(job)
                    target = output / relative
                    target.parent.mkdir(parents=True, exist_ok=True)
                    target.write_text(
                        json.dumps(redacted, ensure_ascii=False, indent=2) + "\n"
                    )
                    payload_paths.append(target)
                    record["trajectory"] = {
                        "status": "included",
                        "path": relative.as_posix(),
                        "redactions": replacements,
                        "source_sha256": _sha256(trajectory),
                    }
        trials.append(record)

    files = [
        {
            "path": path.relative_to(output).as_posix(),
            "sha256": _sha256(path),
        }
        for path in sorted(set(payload_paths))
    ]
    manifest: dict[str, Any] = {
        "manifest_version": 1,
        "source_job": job.name,
        "redaction_rules_version": REDACTION_RULES_VERSION,
        "redaction_placeholder": REDACTION_PLACEHOLDER,
        "policy": {
            "include": ["result.json", "config.json"],
            "trajectory": "non-pass and invalid trials only",
            "fail_closed": "exclude the whole trajectory on an unclassified credential shape",
        },
        "trials": trials,
        "files": files,
    }
    manifest_path = output / "manifest.json"
    manifest_path.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n")

    checksums = [f"{entry['sha256']}  {entry['path']}" for entry in files]
    checksums.append(f"{_sha256(manifest_path)}  manifest.json")
    (output / "SHA256SUMS").write_text("\n".join(checksums) + "\n")
    return manifest


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("job", type=Path, help="Harbor job directory to archive")
    parser.add_argument("--output", required=True, type=Path, help="new archive directory")
    args = parser.parse_args(argv)
    manifest = build_archive(args.job, args.output)
    included = sum(
        trial["trajectory"]["status"] == "included" for trial in manifest["trials"]
    )
    excluded = sum(
        trial["trajectory"]["status"] == "excluded" for trial in manifest["trials"]
    )
    print(
        f"archived {len(manifest['files'])} payload file(s), "
        f"{included} trajectory/trajectories included, {excluded} excluded"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
