#!/usr/bin/env python3
"""Apply an approved weekly draft to the Feishu task table.

Reads the draft produced by collect.py after the agent has filled the judgement
fields, refuses to run on an incomplete draft, writes in two batches, and reads
the result back. Nothing here decides anything.

Usage:
    write.py [--draft draft.json] [--dry-run]
"""

import argparse
import json
import sys

import feishu
from feishu import TASKS

REQUIRED = ["任务", "状态", "优先级", "里程碑", "描述"]


def milestone_cell(name, known):
    if not name:
        return None
    feishu.require_known("milestone", name, known)
    return [{"id": known[name]}]


def common(entry):
    """Fields every touched task carries, whether new or refreshed."""
    fields = {
        "PR": entry["pr_field"],
        "GitHub Issue": entry["issue_url"],
    }
    # 完成日期 is the task's end date, not just a delivery date: a cancelled task
    # gets one too, so 周次 can retire it from the board. An open issue keeps
    # delivering across weeks, so it stays empty.
    if entry["issue_state"] == "closed":
        fields["完成日期"] = entry["last_merged"] + " 00:00:00"
    return fields


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--draft", default="/tmp/weekly-draft.json")
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()

    draft = json.load(open(args.draft))

    # The Base owns these, not this script: fetch them so adding a milestone or
    # a status option in Feishu needs no edit here.
    known_milestones = feishu.milestones()
    known = {
        "状态": set(feishu.select_options(TASKS, "状态")),
        "优先级": set(feishu.select_options(TASKS, "优先级")),
    }
    for entry in draft["new"] + draft["update"] + draft.get("stale", []):
        for field, allowed in known.items():
            feishu.require_known(field, entry.get(field), allowed)

    incomplete = [
        f"#{e['issue']} missing {[k for k in REQUIRED if k != '里程碑' and not e.get(k)]}"
        for e in draft["new"]
        if any(not e.get(k) for k in REQUIRED if k != "里程碑")
    ]
    if incomplete:
        sys.exit("draft is not ready:\n  " + "\n  ".join(incomplete))

    creates = []
    for e in draft["new"]:
        row = dict(common(e))
        row.update({
            "任务": e["任务"],
            "状态": e["状态"],
            "优先级": e["优先级"],
            "描述": e["描述"],
        })
        ms = milestone_cell(e.get("里程碑"), known_milestones)
        if ms:
            row["里程碑"] = ms
        creates.append(row)

    updates = {}
    for e in draft["update"]:
        row = dict(common(e))
        # Updates normally refresh only delivery references. These explicit
        # fields let the reviewed draft repair a task whose lifecycle drifted.
        for field in ("状态", "完成日期"):
            if field in e:
                row[field] = e[field]
        ms = milestone_cell(e.get("里程碑"), known_milestones)
        if ms:
            row["里程碑"] = ms
        updates[e["record_id"]] = row

    # Status-only repairs for tasks delivered in an earlier week. They carry no
    # PR fields, so they must not join the PR-link verification below.
    repairs = {e["record_id"]: {"状态": e["状态"]} for e in draft.get("stale", [])}

    print(f"create {len(creates)} / update {len(updates)} / repair {len(repairs)}")
    if args.dry_run:
        print(json.dumps({"create": creates, "update": updates, "repair": repairs},
                         ensure_ascii=False, indent=2))
        return

    created = []
    if creates:
        created = feishu.lark("+record-batch-create", TASKS,
                              {"create_records": creates})["record_id_list"]
    if updates or repairs:
        feishu.lark("+record-batch-update", TASKS,
                    {"update_records": {**repairs, **updates}})

    # The write APIs do not echo stored rows, so confirm against the table.
    rows = feishu.records(TASKS)
    touched = set(created) | set(updates)
    missing_pr = [rid for rid in touched if not rows.get(rid, {}).get("PR")]
    print(f"verified {len(touched - set(missing_pr))}/{len(touched)} rows carry PR links")
    if missing_pr:
        sys.exit(f"these records did not take the write: {missing_pr}")


if __name__ == "__main__":
    main()
