#!/usr/bin/env python3
"""Feishu Tasks v2 helpers for execution-sync.

Every call shells out to lark-cli with --as user. The planning Base helpers
come straight from the weekly-delivery skill so the two skills never drift on
coordinates or pagination; only the task-v2 surface lives here.
"""

import json
import subprocess
import sys
import time

import os

sys.path.insert(0, os.path.join(os.path.dirname(__file__),
                                "..", "..", "weekly-delivery", "scripts"))
import feishu as base_feishu  # noqa: E402  (weekly-delivery Base helpers)

# Re-exported for sync.py: the Base reads and its write wrapper.
BASE = base_feishu.BASE
MILESTONES_TABLE = base_feishu.MILESTONES_TABLE
TASKS = base_feishu.TASKS
base_lark = base_feishu.lark
base_records = base_feishu.records
milestones = base_feishu.milestones
base_select_options = base_feishu.select_options

# Runtime resolution, never hardcoded: the tasklist/sections/custom fields are
# found or created by name at run time.
TASKLIST_NAME = "Stella"
SECTIONS = ["就绪", "进行中", "阻塞"]
FIELD_BASE_RECORD = "Base Record ID"
FIELD_PRIORITY = "优先级"
FIELD_MILESTONE = "产品里程碑"
# Base columns this skill owns the write of (backfill only; nothing else).
BASE_GUID_FIELD = "飞书任务GUID"
BASE_URL_FIELD = "飞书任务"

DESC_MAX = 3000
SLEEP = 0.3


def task_lark(argv, data=None, params=None):
    """Run a `lark-cli task` raw resource command and return .data."""
    cmd = ["lark-cli", "task", *argv, "--as", "user", "--json"]
    if params:
        for key, value in params.items():
            cmd += [f"--{key.replace('_', '-')}", str(value)]
    if data is not None:
        cmd += ["--data", json.dumps(data, ensure_ascii=False)]
    out = subprocess.run(cmd, capture_output=True, text=True)
    raw = out.stdout or out.stderr or "{}"
    try:
        body = json.loads(raw)
    except json.JSONDecodeError:
        sys.exit(f"task {argv} returned no JSON: {raw[:200]!r}")
    if not body.get("ok"):
        err = body.get("error") or {}
        detail = {k: err.get(k) for k in ("type", "subtype", "code", "message", "field", "error_field") if err.get(k)}
        sys.exit(f"task {argv} failed: {json.dumps(detail, ensure_ascii=False) or json.dumps(err, ensure_ascii=False)}")
    return body.get("data") or {}


# --- tasklist / sections / custom fields -------------------------------

def find_tasklist():
    """The Stella tasklist by exact name, or None."""
    out = subprocess.run(
        ["lark-cli", "task", "+tasklist-search", "--query", TASKLIST_NAME,
         "--page-all", "--as", "user", "--json"],
        capture_output=True, text=True)
    body = json.loads(out.stdout or "{}")
    if not body.get("ok"):
        sys.exit(f"tasklist-search failed: {json.dumps(body.get('error'), ensure_ascii=False)}")
    items = (body.get("data") or {}).get("items") or []
    exact = [t for t in items if t.get("name") == TASKLIST_NAME]
    return exact[0]["guid"] if exact else None


def create_tasklist():
    data = task_lark(["tasklists", "create"],
                     data={"name": TASKLIST_NAME})
    return data["tasklist"]["guid"]


def tasklist_sections(tasklist):
    data = task_lark(["sections", "list"],
                     params={"resource_type": "tasklist", "resource_id": tasklist,
                             "page_size": 100})
    return {s["name"]: s["guid"] for s in data.get("items") or []}


def create_section(tasklist, name):
    data = task_lark(["sections", "create"],
                     data={"name": name, "resource_type": "tasklist",
                           "resource_id": tasklist})
    return data["section"]["guid"]


def ensure_sections(tasklist):
    """Section name -> guid, creating missing ones in canonical order."""
    have = tasklist_sections(tasklist)
    for name in SECTIONS:
        if name not in have:
            have[name] = create_section(tasklist, name)
            time.sleep(SLEEP)
    return have


def custom_fields(tasklist):
    data = task_lark(["custom_fields", "list"],
                     params={"resource_type": "tasklist", "resource_id": tasklist,
                             "page_size": 100})
    return {f["name"]: f for f in data.get("items") or []}


def create_custom_field(tasklist, name, ftype, options=None):
    body = {"name": name, "type": ftype, "resource_type": "tasklist",
            "resource_id": tasklist}
    if options:
        body["single_select_setting"] = {"options": [{"name": o} for o in options]}
    data = task_lark(["custom_fields", "create"], data=body)
    time.sleep(SLEEP)
    return data["custom_field"]


PRIORITY_OPTIONS = ["P0", "P1", "P2"]


def ensure_custom_fields(tasklist):
    """The three projection fields. Returns name -> field guid."""
    have = custom_fields(tasklist)
    wanted = [(FIELD_BASE_RECORD, "text", None),
              (FIELD_PRIORITY, "single_select", PRIORITY_OPTIONS),
              (FIELD_MILESTONE, "single_select", None)]
    guids = {}
    for name, ftype, options in wanted:
        if name in have:
            guids[name] = have[name]["guid"]
        else:
            guids[name] = create_custom_field(tasklist, name, ftype, options)["guid"]
    return guids


def sync_milestone_options(field, names):
    """Create missing milestone options; leave unknown ones alone."""
    existing = {o["name"]: o for o in (field.get("single_select_setting") or {}).get("options") or []}
    for name in names:
        if name not in existing:
            task_lark(["custom_field_options", "create"],
                      params={"custom_field_guid": field["guid"]},
                      data={"name": name})
            time.sleep(SLEEP)


# --- tasks --------------------------------------------------------------

def tasklist_tasks(tasklist):
    """Every task in the list (both states), paginated."""
    out, token = [], ""
    while True:
        params = {"tasklist_guid": tasklist, "page_size": 100}
        if token:
            params["page_token"] = token
        data = task_lark(["tasklists", "tasks"], params=params)
        out += data.get("items") or []
        token = data.get("page_token") or ""
        if not data.get("has_more") or not token:
            return out


def get_task(guid):
    return (task_lark(["tasks", "get"], params={"task_guid": guid}).get("task")) or {}


def create_task(tasklist, summary, description, base_record_id, issue_url,
                field_guids, milestone_option=None, priority_option=None):
    """Create the task inside the tasklist and return its guid.

    The Base Record ID custom field is immutable from here on; origin links the
    task detail header back to the GitHub Issue.
    """
    custom = [{"guid": field_guids[FIELD_BASE_RECORD], "type": "text",
               "text_value": base_record_id}]
    if milestone_option:
        custom.append({"guid": field_guids[FIELD_MILESTONE], "type": "single_select",
                       "single_select_value": milestone_option})
    if priority_option:
        custom.append({"guid": field_guids[FIELD_PRIORITY], "type": "single_select",
                       "single_select_value": priority_option})
    body = {
        "summary": summary,
        "description": description,
        "tasklists": [{"tasklist_guid": tasklist}],
        "custom_fields": custom,
        "origin": {"href": {"url": issue_url, "title": "GitHub Issue"}},
    }
    data = task_lark(["tasks", "create"], data=body)
    return data["task"]["guid"]


def patch_task(guid, fields):
    """Patch selected task attributes; keys must be task object field names."""
    task_lark(["tasks", "patch"], params={"task_guid": guid},
              data={"task": fields, "update_fields": sorted(fields)})
    time.sleep(SLEEP)


def place_in_section(task, tasklist, section_guid):
    """(Re)place a task into the given section of the tasklist."""
    subprocess_ok(["+tasklist-task-add", "--tasklist-id", tasklist,
                   "--task-id", task, "--section-guid", section_guid])


def complete_task(guid):
    subprocess_ok(["+complete", "--task-id", guid])


def reopen_task(guid):
    subprocess_ok(["+reopen", "--task-id", guid])


def comment_task(guid, content):
    subprocess_ok(["+comment", "--task-id", guid, "--content", content])


def subprocess_ok(short):
    cmd = ["lark-cli", "task", *short, "--as", "user", "--json"]
    out = subprocess.run(cmd, capture_output=True, text=True)
    body = json.loads(out.stdout or "{}")
    if not body.get("ok"):
        sys.exit(f"task {short[0]} failed: {json.dumps(body.get('error'), ensure_ascii=False)}")
    time.sleep(SLEEP)


def sync_priority_options(field):
    """The three priority options, created only if missing."""
    sync_milestone_options(field, PRIORITY_OPTIONS)


def ms_options(tasklist, field_guids):
    return {o["name"]: o["guid"] for o in
            (custom_fields(tasklist)[FIELD_MILESTONE]
             .get("single_select_setting") or {}).get("options") or []}


def pr_options(tasklist, field_guids):
    return {o["name"]: o["guid"] for o in
            (custom_fields(tasklist)[FIELD_PRIORITY]
             .get("single_select_setting") or {}).get("options") or []}


def description_of(base_desc, issue_url):
    """Composed task description: Base acceptance summary plus the issue link."""
    text = (base_desc or "").strip() or "（Base 描述为空，验收标准见 GitHub Issue）"
    link = f"\n\nGitHub Issue: {issue_url}" if issue_url else ""
    budget = DESC_MAX - len(link)
    if len(text) > budget:
        text = text[:budget - 20] + "…（截断，完整见 GitHub Issue）"
    return text + link


def custom_value(task, field_guid, key):
    for cf in task.get("custom_fields") or []:
        if cf.get("guid") == field_guid:
            return cf.get(key)
    return None
