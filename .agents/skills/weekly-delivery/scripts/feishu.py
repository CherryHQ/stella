#!/usr/bin/env python3
"""Read the shape of the planning Base instead of hardcoding it.

Milestones, statuses and priorities all live in Feishu and change there. Every
copy of them in a script is a copy that goes stale silently, so this module
fetches them at run time. Only the coordinates below are constants: they are
the one thing that genuinely never changes.
"""

import json
import subprocess
import sys

BASE = "BEEbbI9jtad6PmsYSXpcmBy2nUd"
TASKS = "tbl4pUhlngTJdg2Z"
MILESTONES_TABLE = "tblCRcuKDjmnKCJr"

# Feishu paginates at 500; 200 keeps a page comfortably inside the CLI's buffer.
PAGE = 200


def lark(cmd, table, payload=None, extra=None):
    argv = ["lark-cli", "base", cmd, "--base-token", BASE, "--table-id", table,
            "--as", "user"]
    if payload is not None:
        argv += ["--json", json.dumps(payload, ensure_ascii=False)]
    argv += extra or []
    out = subprocess.run(argv, capture_output=True, text=True)
    try:
        body = json.loads(out.stdout or "{}")
    except json.JSONDecodeError:
        sys.exit(f"{cmd} returned no JSON: {out.stdout[:200]!r} {out.stderr[:200]!r}")
    if not body.get("ok"):
        sys.exit(f"{cmd} failed: {json.dumps(body.get('error'), ensure_ascii=False)}")
    return body.get("data") or {}


def records(table):
    """Every row of a table as {record_id: {field: value}}."""
    rows, offset = {}, 0
    while True:
        data = lark("+record-list", table, extra=[
            "--limit", str(PAGE), "--offset", str(offset), "--json",
        ])
        page = {
            rid: dict(zip(data["fields"], row))
            for rid, row in zip(data["record_id_list"], data["data"])
        }
        rows.update(page)
        if not data.get("has_more"):
            return rows
        if not page:
            sys.exit("record-list returned has_more with an empty page")
        offset += len(page)


def milestones():
    """Live milestone name -> record id, straight from the 里程碑 table.

    The name is the primary field, so it is what a human types and what the
    draft carries. Adding a milestone in Feishu is enough; nothing here needs
    editing.
    """
    out = {}
    for rid, row in records(MILESTONES_TABLE).items():
        name = row.get("里程碑")
        if name:
            out[name] = rid
    if not out:
        sys.exit("the 里程碑 table came back empty; refusing to guess")
    return out


def select_options(table, field):
    """The option names of a single-select field, in the order Feishu stores them."""
    for f in lark("+field-list", table).get("fields", []):
        if f.get("name") == field:
            return [o["name"] for o in f.get("options") or []]
    sys.exit(f"field {field!r} not found on table {table}")


def require_known(kind, value, allowed):
    if value is not None and value not in allowed:
        sys.exit(f"unknown {kind} {value!r}; the Base currently offers: {sorted(allowed)}")
