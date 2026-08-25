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
import subprocess
import sys

BASE = "BEEbbI9jtad6PmsYSXpcmBy2nUd"
TASKS = "tbl4pUhlngTJdg2Z"

# Names must match the 里程碑 table; a rename there needs the key updated here.
MILESTONES = {
    "知识库 v1": "recvqx8529aZBT",
    "自动化测试 v1": "recvqx869rkLuv",
    "记忆 v1": "recvqzQfwmK9cy",
    "云端 Agent 体验 v1": "recvqPcD0E3FLo",
    "企业版集成 v1": "recvqPcD0EFGJu",
    "任务一键上云 v1": "recvqPcD0EIydV",
    "Eval v1": "recvqPHDXBNCm7",
    "平台核心持续维护": "recvrXpAB7GkXa",
    "渠道接入与维护": "recvrXpAB7bvth",
    "运维持续维护": "recvrXpAB7YWvG",
}

REQUIRED = ["任务", "状态", "优先级", "里程碑", "描述"]


def lark(cmd, payload=None):
    argv = ["lark-cli", "base", cmd, "--base-token", BASE, "--table-id", TASKS, "--as", "user"]
    if payload is not None:
        argv += ["--json", json.dumps(payload, ensure_ascii=False)]
    out = subprocess.run(argv, capture_output=True, text=True)
    body = json.loads(out.stdout or "{}")
    if not body.get("ok"):
        sys.exit(f"{cmd} failed: {json.dumps(body.get('error'), ensure_ascii=False)}")
    return body.get("data") or {}


def milestone_cell(name):
    if not name:
        return None
    if name not in MILESTONES:
        sys.exit(f"unknown milestone {name!r}; add its record id to MILESTONES first")
    return [{"id": MILESTONES[name]}]


def all_rows():
    rows, offset = {}, 0
    while True:
        argv = ["lark-cli", "base", "+record-list", "--base-token", BASE, "--table-id", TASKS,
                "--as", "user", "--limit", "200", "--offset", str(offset), "--json"]
        table = json.loads(subprocess.run(argv, capture_output=True, text=True).stdout)["data"]
        rows.update(
            (rid, dict(zip(table["fields"], row)))
            for rid, row in zip(table["record_id_list"], table["data"])
        )
        if not table.get("has_more"):
            return rows
        if not table["record_id_list"]:
            sys.exit("record-list returned has_more with an empty page")
        offset += len(table["record_id_list"])


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
        ms = milestone_cell(e.get("里程碑"))
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
        ms = milestone_cell(e.get("里程碑"))
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
        created = lark("+record-batch-create", {"create_records": creates})["record_id_list"]
    if updates or repairs:
        lark("+record-batch-update", {"update_records": {**repairs, **updates}})

    # The write APIs do not echo stored rows, so confirm against the table.
    rows = all_rows()
    touched = set(created) | set(updates)
    missing_pr = [rid for rid in touched if not rows.get(rid, {}).get("PR")]
    print(f"verified {len(touched - set(missing_pr))}/{len(touched)} rows carry PR links")
    if missing_pr:
        sys.exit(f"these records did not take the write: {missing_pr}")


if __name__ == "__main__":
    main()
