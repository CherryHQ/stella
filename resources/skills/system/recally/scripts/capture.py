#!/usr/bin/env python3
"""Capture one URL into a sandbox file and print compact metadata as JSON.

Bundled with the skill so the model never has to reproduce fetch, extraction,
and normalization logic by hand. The article body is written to disk and is
never printed: only the small metadata object reaches the model.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import subprocess
import sys
from datetime import datetime, timezone
from email.utils import parsedate_to_datetime

# Below this, extraction is treated as failed rather than saved as an article.
MIN_BODY_CHARS = 100
# Metadata is untrusted page content; cap it so it cannot flood model context.
MAX_FIELD_CHARS = 300
# Head and tail of the body echoed back so the caller can see what was captured
# without the body entering model context. A summary page and a real article
# look nothing alike at the edges.
PREVIEW_EDGE_CHARS = 100

TITLE_PATTERN = re.compile(r"^#\s+(.+)$", re.MULTILINE)


def preview(body: str) -> str:
    """Head and tail of the body, so the caller can judge the extraction."""
    text = " ".join(body.split())
    if len(text) <= PREVIEW_EDGE_CHARS * 2:
        return text
    return f"{text[:PREVIEW_EDGE_CHARS]} […] {text[-PREVIEW_EDGE_CHARS:]}"


class CaptureError(Exception):
    """Fatal capture failure with a message meant for the model."""


def compact(value: object) -> str:
    """Collapse untrusted metadata to one short single-line string."""
    if isinstance(value, dict):
        value = value.get("name", "")
    if isinstance(value, list):
        value = value[0] if value else ""
        if isinstance(value, dict):
            value = value.get("name", "")
    if not isinstance(value, str):
        return ""
    return " ".join(value.split())[:MAX_FIELD_CHARS]


def rfc3339(value: object) -> str:
    """Normalize a publication date, treating a naive one as UTC."""
    text = compact(value)
    if not text:
        return ""
    parsed = None
    try:
        parsed = datetime.fromisoformat(text.replace("Z", "+00:00"))
    except ValueError:
        try:
            parsed = parsedate_to_datetime(text)
        except (TypeError, ValueError):
            return ""
    if parsed is None:
        return ""
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=timezone.utc)
    return parsed.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")


def check_url(url: str) -> None:
    if not url.startswith(("http://", "https://")):
        raise CaptureError(f"unsupported URL scheme: {url!r}")
    if any(character.isspace() or ord(character) < 0x20 for character in url):
        raise CaptureError("URL contains whitespace or control characters")


def run_tap(args: list[str]) -> str:
    """Run tap with argv, never a shell, so the URL cannot be interpreted."""
    try:
        result = subprocess.run(
            ["tap", *args], capture_output=True, text=True, check=False
        )
    except FileNotFoundError as error:
        raise CaptureError("tap is not installed in this sandbox") from error
    if result.returncode != 0:
        detail = compact(result.stderr) or f"exit {result.returncode}"
        raise CaptureError(f"tap {' '.join(args)} failed: {detail}")
    return result.stdout


def structured(url: str) -> tuple[str, dict[str, str]]:
    """Preferred path: one fetch returning both body and fetcher metadata."""
    raw = run_tap(["fetch", "--json", url])
    try:
        meta = json.loads(raw)
    except json.JSONDecodeError as error:
        raise CaptureError(f"tap fetch --json returned invalid JSON: {error}") from error
    if not isinstance(meta, dict):
        raise CaptureError("tap fetch --json did not return an object")
    body = meta.get("markdown") or meta.get("content") or ""
    if not isinstance(body, str) or len(body.strip()) < MIN_BODY_CHARS:
        raise CaptureError("thin extraction")
    return body, {
        "title": compact(meta.get("title")),
        "author": compact(meta.get("author")),
        "published": rfc3339(meta.get("published")),
        "description": compact(meta.get("description")),
    }


def plain(url: str) -> tuple[str, dict[str, str]]:
    """Fallback: a different extractor, with the title read from the Markdown."""
    body = run_tap(["fetch", "--lp", url])
    if len(body.strip()) < MIN_BODY_CHARS:
        raise CaptureError("thin extraction")
    match = TITLE_PATTERN.search(body)
    return body, {
        "title": compact(match.group(1) if match else ""),
        "author": "",
        "published": "",
        "description": "",
    }


def capture(url: str, out_dir: str) -> dict[str, str]:
    check_url(url)
    try:
        body, metadata = structured(url)
    except CaptureError as first:
        try:
            body, metadata = plain(url)
        except CaptureError as second:
            raise CaptureError(f"{first}; fallback also failed: {second}") from second
    digest = hashlib.sha256(url.encode("utf-8")).hexdigest()[:8]
    content_path = os.path.join(out_dir, f"recally-{digest}.md")
    with open(content_path, "w", encoding="utf-8") as destination:
        destination.write(body)
    metadata["content_path"] = content_path
    metadata["body_chars"] = len(body)
    metadata["body_preview"] = preview(body)
    return metadata


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("url")
    parser.add_argument(
        "--out-dir",
        default=os.environ.get("TMPDIR") or "/tmp",
        help="directory for the article file (default: $TMPDIR)",
    )
    arguments = parser.parse_args()
    try:
        metadata = capture(arguments.url, arguments.out_dir)
    except CaptureError as error:
        print(f"capture failed: {error}", file=sys.stderr)
        return 1
    print(json.dumps(metadata, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
