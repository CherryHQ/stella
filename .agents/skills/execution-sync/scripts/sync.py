#!/usr/bin/env python3
"""Sync committed Base tasks into the Stella tasklist.

Model: the planning Base is the source of truth for what is committed and its
lifecycle; Feishu Tasks is the daily execution surface. Every Base record at
就绪 / 进行中 / 阻塞 gets exactly one task in the Stella tasklist,
placed in the matching section. Candidate (待评估) and terminal Base records
are never synced. The idempotency key is the Base record_id, stored twice: in
the Base 飞书任务GUID column and in the task's immutable custom field, so any
drift (deleted task, edited field) is detectable on the next run.

Usage:
    sync.py plan             # read-only report of what apply would change
    sync.py apply            # execute the plan, then re-plan and verify
    sync.py bootstrap        # alias of plan, for the first backfill
    sync.py bootstrap --apply
"""

import argparse
import sys

import tasks_v2 as fv
from tasks_v2 import BASE_GUID_FIELD, FIELD_BASE_RECORD, FIELD_MILESTONE, FIELD_PRIORITY

ACTIVE = {"就绪", "进行中", "阻塞"}
GITHUB_BASE = "https://github.com/CherryHQ/stella/issues/"
LINK_RE = None  # compiled lazily
BASE_TO_SECTION = {"就绪": "就绪", "进行中": "进行中", "阻塞": "阻塞"}

# --- Base side -----------------------------------------------------------

import re as _re
_MD_LINK = _re.compile(r"\[?(https?://[^\]\s)]+)\]?(\([^)]*\))?")


def bare_url(cell):
    """Base text cells store markdown links; tasks API wants a bare URL."""
    m = _MD_LINK.search(cell or "")
    return m.group(1) if m else (cell or "").strip()


def active_base_tasks():
    """Base records that should exist as execution tasks, with projections."""
    names = fv.milestones()               # milestone name -> record_id
    by_id = {v: k for k, v in names.items()}
    rows = fv.base_records(fv.TASKS)
    items = []
    for rid, row in rows.items():
        status = (row.get("状态") or [""])[0]
        if status not in ACTIVE:
            continue
        title = (row.get("任务") or "").strip()
        links = row.get("里程碑") or []
        items.append({
            "record_id": rid,
            "title": title,
            "status": status,
            "priority": (row.get("优先级") or [None])[0],
            "milestone": by_id.get(links[0]["id"]) if links else None,
            "description": row.get("描述"),
            "issue_url": bare_url(row.get("GitHub Issue")),
            "guid": (row.get(BASE_GUID_FIELD) or "").strip(),
        })
    return items


def validate(items):
    """Refuse to run on records this script has no business guessing about."""
    errors = []
    for item in items:
        where = f"{item['record_id']} {item['title'] or '(无标题)'}"
        if not item["title"]:
            errors.append(f"{where}: 任务标题为空")
        if not item["issue_url"]:
            errors.append(f"{where}: 状态 {item['status']} 但没有 GitHub Issue URL")
        elif not item["issue_url"].startswith(GITHUB_BASE):
            errors.append(f"{where}: GitHub Issue URL 不是 CherryHQ/stella 的 issue")
    return errors


# --- Feishu side ---------------------------------------------------------

def indexed_tasks(tasklist, field_guids):
    """Existing tasks keyed by their task guid.

    tasklist_tasks returns summaries; we keep full tasks because the plan needs
    the custom fields. The Base Record ID is checked separately in plan:
    a task whose rid field does not match its Base pointer is a drift error.
    """
    return {t["guid"]: t for t in
            (fv.get_task(s["guid"]) for s in fv.tasklist_tasks(tasklist))}


def section_of(task, tasklist):
    for tl in task.get("tasklists") or []:
        if tl.get("tasklist_guid") == tasklist:
            return tl.get("section_guid")
    return None


# --- plan ----------------------------------------------------------------

def build_plan(tasklist, field_guids, sections, base_items, indexed, ms_options, pr_options):
    """Compute the change set. Returns (actions, errors)."""
    actions, errors = [], []
    seen_guids = {}
    for item in base_items:
        rid = item["record_id"]
        existing = indexed.get(item["guid"]) if item["guid"] else None
        if existing is not None:
            stored = fv.custom_value(existing, field_guids[FIELD_BASE_RECORD],
                                     "text_value")
            if stored != rid:
                errors.append(f"{rid}: 飞书任务 {item['guid']} 的 Base Record ID 是 "
                              f"{stored!r}，与 Base 指针不一致；人工检查")
                continue
        if item["guid"] and existing is None:
            errors.append(f"{rid}: Base 指向的飞书任务 {item['guid']} 不在清单里，"
                          f"疑似被删；人工确认后清掉 Base 的 {BASE_GUID_FIELD} 再跑")
            continue
        if existing is None:
            actions.append({
                "op": "create", "record_id": rid, "title": item["title"],
                "section": BASE_TO_SECTION[item["status"]],
            })
            continue
        guid = existing["guid"]
        if guid in seen_guids:
            errors.append(f"{rid}: 飞书任务 {guid} 被多条 Base 记录引用")
            continue
        seen_guids[guid] = rid

        want_summary = item["title"]
        want_desc = fv.description_of(item["description"], item["issue_url"])
        want_ms = ms_options.get(item["milestone"]) if item["milestone"] else None
        want_pr = pr_options.get(item["priority"]) if item["priority"] else None
        want_section = sections[BASE_TO_SECTION[item["status"]]]

        changes = {}
        if existing.get("summary") != want_summary:
            changes["summary"] = want_summary
        if existing.get("description") != want_desc:
            changes["description"] = want_desc
        if fv.custom_value(existing, field_guids[FIELD_MILESTONE],
                           "single_select_value") != want_ms:
            changes["milestone"] = want_ms or ""
        if fv.custom_value(existing, field_guids[FIELD_PRIORITY],
                           "single_select_value") != want_pr:
            changes["priority"] = want_pr or ""
        if section_of(existing, tasklist) != want_section:
            changes["section"] = want_section
        # Active Base records are always open execution work; a done parent is
        # either a premature tick or a stale projection, so reopen it.
        if existing.get("status") == "done":
            changes["reopen"] = True
        if changes:
            actions.append({"op": "update", "record_id": rid, "guid": guid,
                            "title": want_summary,
                            "section_name": BASE_TO_SECTION[item["status"]],
                            "changes": changes})
    return actions, errors


# --- apply ---------------------------------------------------------------

def apply_plan(tasklist, field_guids, sections, base_items, actions):
    item_by_rid = {i["record_id"]: i for i in base_items}
    created = []
    for action in actions:
        item = item_by_rid[action["record_id"]]
        if action["op"] == "create":
            guid = fv.create_task(
                tasklist, action["title"],
                fv.description_of(item["description"], item["issue_url"]),
                action["record_id"], item["issue_url"], field_guids,
                milestone_option=fv.ms_options(tasklist, field_guids)
                                 .get(item["milestone"]),
                priority_option=fv.pr_options(tasklist, field_guids)
                                .get(item["priority"]))
            fv.place_in_section(guid, tasklist, sections[action["section"]])
            created.append((action["record_id"], guid))
            continue
        guid = action["guid"]
        changes = action["changes"]
        task_fields = {}
        if "summary" in changes:
            task_fields["summary"] = changes["summary"]
        if "description" in changes:
            task_fields["description"] = changes["description"]
        for key, field in (("milestone", FIELD_MILESTONE), ("priority", FIELD_PRIORITY)):
            if key in changes:
                task_fields.setdefault("custom_fields", []).append({
                    "guid": field_guids[field], "type": "single_select",
                    "single_select_value": changes[key]})
        if task_fields:
            fv.patch_task(guid, task_fields)
        if "section" in changes:
            fv.place_in_section(guid, tasklist, changes["section"])
        if "reopen" in changes:
            fv.reopen_task(guid)
    if not created:
        return created

    # Write the fresh guids and share URLs back to Base, then verify.
    write_back = {}
    for rid, guid in created:
        task = fv.get_task(guid)
        write_back[rid] = {BASE_GUID_FIELD: guid,
                           "飞书任务": task.get("url") or ""}
    fv.base_lark("+record-batch-update", fv.TASKS,
                 {"update_records": write_back})
    rows = fv.base_records(fv.TASKS)
    missing = [rid for rid, fields in write_back.items()
               if not (rows.get(rid) or {}).get(BASE_GUID_FIELD)]
    if missing:
        sys.exit(f"Base write-back did not stick for: {missing}")
    return created


# --- entry ---------------------------------------------------------------

def run(tasklist, field_guids, base_items, indexed, ms_options, pr_options,
        sections, label):
    actions, errors = build_plan(tasklist, field_guids, sections, base_items,
                                 indexed, ms_options, pr_options)
    creates = [a for a in actions if a["op"] == "create"]
    updates = [a for a in actions if a["op"] == "update"]
    if errors:
        print(f"ERRORS ({label}; nothing will be written):")
        for e in errors:
            print("  !", e)
    print(f"[{label}] base {len(base_items)} · create {len(creates)} · "
          f"update {len(updates)} · errors {len(errors)}")
    for a in creates:
        print(f"  + [{a['section']}] {a['title']}")
    for a in updates:
        print(f"  ~ {a['title']}: {', '.join(a['changes'])}")
    return actions, errors


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("command", choices=["plan", "apply", "bootstrap"])
    ap.add_argument("--apply", action="store_true",
                    help="with bootstrap: execute after printing the plan")
    ap.add_argument("--tasklist", default=None,
                    help="use an existing tasklist guid instead of finding/creating one")
    args = ap.parse_args()
    if args.command == "bootstrap" and not args.apply:
        args.command = "plan"

    tasklist = args.tasklist or fv.find_tasklist() or fv.create_tasklist()
    field_guids = fv.ensure_custom_fields(tasklist)
    sections = fv.ensure_sections(tasklist)
    fv.sync_milestone_options(
        fv.custom_fields(tasklist)[FIELD_MILESTONE],
        sorted(fv.milestones()))
    fv.sync_priority_options(
        fv.custom_fields(tasklist)[FIELD_PRIORITY])
    ms_options = fv.ms_options(tasklist, field_guids)
    pr_options = fv.pr_options(tasklist, field_guids)

    base_items = active_base_tasks()
    errors = validate(base_items)
    indexed = indexed_tasks(tasklist, field_guids)
    actions, plan_errors = run(tasklist, field_guids, base_items, indexed,
                               ms_options, pr_options, sections, "plan")
    errors += plan_errors

    if args.command == "plan":
        if errors:
            sys.exit(1)
        return

    if errors:
        sys.exit("errors above; fix them before applying")
    apply_plan(tasklist, field_guids, sections, base_items, actions)

    indexed = indexed_tasks(tasklist, field_guids)
    actions2, errors2 = run(tasklist, field_guids, base_items, indexed,
                            ms_options, pr_options, sections, "read-back")
    if actions2 or errors2:
        sys.exit("read-back still shows drift; run plan again")
    print("converged: Base and the Stella tasklist agree")


if __name__ == "__main__":
    main()
