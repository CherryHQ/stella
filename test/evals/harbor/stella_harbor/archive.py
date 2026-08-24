"""Create a reviewable, fail-closed Harbor evidence archive.

Usage:
    python -m stella_harbor.archive <job> --output <directory>

The source job is never changed. Only result/config files and, on request,
redacted agent transcripts are copied.
"""

from __future__ import annotations

import argparse
import base64
import binascii
import hashlib
import json
import re
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
_SECRET_FIELDS = re.compile(
    r"(?:password|passwd|secret|api_key|access_token|refresh_token)", re.IGNORECASE
)


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


def _is_obvious_fixture(value: str) -> bool:
    """Ignore regex templates and the ordered synthetic token used by the benchmark."""
    lowered = value.lower()
    return (
        any(marker in value for marker in ("[", "]", "{", "}", "\\"))
        or "abcdefghijklmnopqrstuvwxyz" in lowered
        or "0123456789" in lowered
    )


def _is_valid_authorization(match: re.Match[str]) -> bool:
    token = match.group(4)
    if not re.fullmatch(r"[A-Za-z0-9\-._~+/]+={0,}", token):
        return False
    return match.group(2).lower() != "basic" or _looks_like_basic_credential(token)


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
        if not _is_valid_authorization(match):
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
        if _is_valid_authorization(match):
            continue
        if token.lower() not in _PLACEHOLDER_VALUES:
            unknown.append("authorization-header")

    return unknown


def _scan_credential_shapes(text: str, field: str = "") -> list[str]:
    """Find actionable credential shapes without using the broad long-token rule."""
    findings = list(_unknown_credential_shapes(text))
    field_name = field.lower().replace("-", "_")
    if field_name == "bridge_nonce":
        return findings

    if _SECRET_FIELDS.fullmatch(field_name) and text and text.lower() not in _PLACEHOLDER_VALUES:
        if not _is_obvious_fixture(text):
            findings.append("credential-field")

    for pattern, label in (
        (_ASSIGNMENT, "credential-assignment"),
        (_URL_USERINFO, "url-userinfo"),
        (_JWT, "jwt"),
        (_PRIVATE_KEY, "private-key"),
    ):
        for match in pattern.finditer(text):
            if not _is_obvious_fixture(match.group(0)):
                findings.append(label)

    for match in _AUTHORIZATION.finditer(text):
        if _is_valid_authorization(match) and not _is_obvious_fixture(match.group(0)):
            findings.append("authorization-header")

    for match in _TOKEN_PREFIX.finditer(text):
        if not _is_obvious_fixture(match.group(0)):
            findings.append("token-prefix")

    return findings


def _scan_json(value: Any, path: str = "$", field: str = "") -> list[str]:
    if isinstance(value, str):
        return [f"{path}:{reason}" for reason in _scan_credential_shapes(value, field)]
    if isinstance(value, list):
        findings: list[str] = []
        for index, item in enumerate(value):
            findings.extend(_scan_json(item, f"{path}[{index}]"))
        return findings
    if isinstance(value, dict):
        findings = []
        for key, item in value.items():
            findings.extend(_scan_json(item, f"{path}.{key}", str(key)))
        return findings
    return []


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
    value: Any, nonce: str, path: str = "$", field: str = "", *, drop_unknown: bool = False
) -> tuple[Any, int, list[str]]:
    """Redact in place; report every string that carries an unclassified secret.

    With ``drop_unknown`` the offending string is replaced whole rather than
    trimmed, because an unclassified shape is exactly the case where we cannot
    say where the secret ends. Dropping one command loses one line of evidence;
    keeping it can publish a credential, so the trade is not close. Callers that
    cannot drop treat the same list as a reason to exclude the file.
    """
    if isinstance(value, str):
        sanitized, replacements, unknown = _redact_value(value, nonce, field)
        located = [f"{path}:{reason}" for reason in unknown]
        if unknown and drop_unknown:
            return REDACTION_PLACEHOLDER, replacements + 1, located
        return sanitized, replacements, located
    if isinstance(value, list):
        output = []
        replacements = 0
        unknown: list[str] = []
        for index, item in enumerate(value):
            sanitized, count, reasons = _redact_json(item, nonce, f"{path}[{index}]", drop_unknown=drop_unknown)
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
                item, nonce, f"{path}.{key}", str(key), drop_unknown=drop_unknown
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


def _redact_payload(source: Path, output: Path, relative: Path, nonce: str) -> dict[str, Any]:
    """Copy one payload file with the same redaction the transcripts get.

    Terminal-Bench ships tasks whose whole point is a password or a URL with
    credentials in it, and the agent's own commands are recorded in result.json,
    so a real job always carries credential-shaped strings. Refusing to archive
    on that finding makes the tool unusable on the runs it exists for; scrubbing
    them, and saying so per file, keeps both the evidence and the guarantee.
    """
    value = json.loads(source.read_text())
    redacted, replacements, dropped = _redact_json(value, nonce, drop_unknown=True)
    target = output / relative
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(json.dumps(redacted, ensure_ascii=False, indent=2) + "\n")
    return {
        "path": relative.as_posix(),
        "redactions": replacements,
        "dropped": sorted(set(dropped)),
        "source_sha256": _sha256(source),
    }


def _payload_files(job: Path) -> list[Path]:
    return [
        source
        for source in sorted(job.rglob("*"))
        if not source.is_symlink() and source.is_file() and source.name in {"result.json", "config.json"}
    ]


def _payload_nonces(payloads: list[Path]) -> dict[Path, str]:
    """Every trial's bridge nonce, so a payload is redacted with its own."""
    nonces: dict[Path, str] = {}
    for source in payloads:
        if source.name != "result.json" or source.parent.name != "stella":
            continue
        adapter = _read_json(source)
        if isinstance(adapter, dict) and adapter.get("bridge_nonce"):
            nonces[source.parents[2]] = str(adapter["bridge_nonce"])
    return nonces


def _nonce_for(source: Path, nonces: dict[Path, str]) -> str:
    """The nonce of the trial this payload belongs to, wherever it sits in it."""
    for parent in source.parents:
        if parent in nonces:
            return nonces[parent]
    return ""


def _validate_payloads(payloads: list[Path]) -> None:
    """Fail before writing anything if a payload cannot be parsed at all."""
    for source in payloads:
        try:
            json.loads(source.read_text())
        except (OSError, UnicodeError, json.JSONDecodeError) as exc:
            raise ValueError(f"cannot inspect {source}: invalid JSON ({exc})") from exc


# What each agent leaves behind. Stella writes one JSON trajectory; upstream pi
# writes its stream as JSON lines. Anything else contributes no transcript, and
# the manifest says so per trial rather than staying silent about it.
_TRANSCRIPTS: tuple[tuple[str, tuple[str, ...], str], ...] = (
    ("stella", ("trajectory.json", "agent/stella/trajectory.json"), "json"),
    ("pi", ("agent/pi.txt",), "jsonl"),
)


def _transcript_sources(trial_dir: Path) -> list[tuple[str, Path, str]]:
    found: list[tuple[str, Path, str]] = []
    for kind, candidates, encoding in _TRANSCRIPTS:
        for candidate in candidates:
            path = trial_dir / candidate
            if path.is_file():
                found.append((kind, path, encoding))
                break
    return found


def _load_transcript(path: Path, encoding: str) -> tuple[list[Any], str | None]:
    """Return the transcript's JSON documents, or the reason it cannot be read.

    A pi stream that was cut mid-write is not valid UTF-8, and a half-written
    last line is not valid JSON. Both mean the file cannot be redacted with
    confidence, which is a reason to exclude it, never a reason to copy it raw.
    """
    try:
        text = path.read_text()
    except (OSError, UnicodeError):
        return [], "transcript is not valid UTF-8"
    try:
        if encoding == "json":
            return [json.loads(text)], None
        return [json.loads(line) for line in text.splitlines() if line.strip()], None
    except json.JSONDecodeError:
        return [], f"transcript is not valid {encoding.upper()}"


def _dump_transcript(documents: list[Any], encoding: str) -> str:
    if encoding == "json":
        return json.dumps(documents[0], ensure_ascii=False, indent=2) + "\n"
    return "".join(json.dumps(doc, ensure_ascii=False) + "\n" for doc in documents)


def build_archive(
    job: Path, output: Path, *, include_trajectories: bool = False
) -> dict[str, Any]:
    job = job.expanduser().resolve()
    output = output.expanduser().resolve()
    if not job.is_dir():
        raise ValueError(f"job directory does not exist: {job}")
    if output == job or output.is_relative_to(job):
        raise ValueError("output directory must not be inside the source job")
    if output.exists() and any(output.iterdir()):
        raise ValueError(f"output directory is not empty: {output}")

    payload_sources = _payload_files(job)
    _validate_payloads(payload_sources)
    nonces = _payload_nonces(payload_sources)
    output.mkdir(parents=True, exist_ok=True)

    payload_records = [
        _redact_payload(source, output, source.relative_to(job), _nonce_for(source, nonces))
        for source in payload_sources
    ]
    payload_paths = [output / record["path"] for record in payload_records]

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
        nonce = str(adapter.get("bridge_nonce") or "") if isinstance(adapter, dict) else ""
        record: dict[str, Any] = {
            "trial": str(trial_dir.relative_to(job)),
            "classification": classification,
            "transcripts": [],
        }

        for kind, source, encoding in _transcript_sources(trial_dir):
            relative = source.relative_to(job)
            entry: dict[str, Any] = {
                "kind": kind,
                "source_path": relative.as_posix(),
                "source_sha256": _sha256(source),
            }
            if not include_trajectories:
                entry |= {"status": "disabled", "reason": "transcript inclusion not requested"}
                record["transcripts"].append(entry)
                continue
            documents, unreadable = _load_transcript(source, encoding)
            if unreadable:
                record["transcripts"].append(entry | {"status": "excluded", "reason": unreadable})
                continue
            redacted: list[Any] = []
            replacements = 0
            unknown: list[str] = []
            for document in documents:
                clean, count, found = _redact_json(document, nonce, drop_unknown=True)
                redacted.append(clean)
                replacements += count
                unknown.extend(found)
            target = output / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(_dump_transcript(redacted, encoding))
            payload_paths.append(target)
            included = entry | {
                "status": "included",
                "path": relative.as_posix(),
                "redactions": replacements,
            }
            if unknown:
                included["dropped"] = sorted(set(unknown))
            record["transcripts"].append(included)

        trials.append(record)

    redaction_by_path = {record["path"]: record for record in payload_records}
    files = []
    for path in sorted(set(payload_paths)):
        relative = path.relative_to(output).as_posix()
        entry = {"path": relative, "sha256": _sha256(path)}
        record = redaction_by_path.get(relative)
        if record:
            entry |= {"redactions": record["redactions"], "source_sha256": record["source_sha256"]}
            if record["dropped"]:
                entry["dropped"] = record["dropped"]
        files.append(entry)
    manifest: dict[str, Any] = {
        "manifest_version": 2,
        "source_job": job.name,
        "redaction_rules_version": REDACTION_RULES_VERSION,
        "redaction_placeholder": REDACTION_PLACEHOLDER,
        "include_trajectories": include_trajectories,
        "policy": {
            "include": ["result.json", "config.json"],
            # Every trial, not only the failures: a passing run is the evidence
            # for how it passed, and reading one is the usual way a regression
            # gets explained. Redaction is content-based, so the verdict never
            # decided how safe a transcript was.
            "transcripts": (
                "redacted, every trial, every agent that leaves one"
                if include_trajectories
                else "disabled unless --include-trajectories is requested"
            ),
            "fail_closed": "drop any string carrying an unclassified credential shape; "
                           "exclude the whole transcript when it cannot be parsed",
            "payload_redaction": "same rules as transcripts; a string carrying an "
                                 "unclassified credential shape is dropped whole and listed",
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
    parser.add_argument(
        "--include-trajectories",
        action="store_true",
        help="include a redacted transcript for every trial that has one",
    )
    args = parser.parse_args(argv)
    try:
        manifest = build_archive(
            args.job, args.output, include_trajectories=args.include_trajectories
        )
    except ValueError as exc:
        parser.error(str(exc))
    transcripts = [entry for trial in manifest["trials"] for entry in trial["transcripts"]]
    included = sum(entry["status"] == "included" for entry in transcripts)
    excluded = sum(entry["status"] == "excluded" for entry in transcripts)
    dropped = sum(len(entry.get("dropped") or []) for entry in manifest["files"])
    dropped += sum(len(entry.get("dropped") or []) for entry in transcripts)
    print(
        f"archived {len(manifest['files'])} payload file(s), "
        f"{included} transcript(s) included, {excluded} excluded, "
        f"{dropped} unclassified secret-shaped value(s) dropped"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
