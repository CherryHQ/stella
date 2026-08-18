#!/usr/bin/env python3
"""Collect one delivery week of merged PRs and diff them against the Feishu task table.

Deterministic half of the weekly ritual: everything here is mechanical. The
judgement calls (milestone, priority, product line, acceptance criteria) are
left to the agent, which fills them into the emitted draft.

Usage:
    collect.py [--week-start YYYY-MM-DD] [--out draft.json]

Without --week-start the window is the most recent Tuesday-to-Tuesday period
that has fully elapsed, i.e. the week the run on Tuesday is reporting on.
"""

import argparse
import datetime as dt
import json
import re
import subprocess
import sys

REPO = "CherryHQ/stella"
BASE = "BEEbbI9jtad6PmsYSXpcmBy2nUd"
TASKS = "tbl4pUhlngTJdg2Z"

# Closing keywords plus the two forms Stella actually uses in release PRs.
REF = re.compile(r"(?:[Cc]loses|[Ff]ixes|[Rr]esolves|[Rr]efs|[Pp]art of|[Rr]elated to) #(\d+)")


def sh(cmd):
    out = subprocess.run(cmd, capture_output=True, text=True)
    if out.returncode != 0:
        sys.exit(f"command failed: {' '.join(cmd)}\n{out.stderr}")
    return out.stdout


def week_window(explicit):
    """Tuesday 00:00 (inclusive) to the next Tuesday 00:00 (exclusive)."""
    if explicit:
        start = dt.date.fromisoformat(explicit)
    else:
        today = dt.date.today()
        # Monday=0 .. Tuesday=1. Step back to this week's Tuesday, then one more
        # week: on Tuesday we report the week that just closed.
        start = today - dt.timedelta(days=(today.weekday() - 1) % 7) - dt.timedelta(days=7)
    return start, start + dt.timedelta(days=7)


def merged_prs(start, end):
    raw = sh([
        "gh", "pr", "list", "--repo", REPO, "--state", "merged", "--limit", "200",
        "--search", f"merged:{start}..{end}",
        "--json", "number,title,url,createdAt,mergedAt,body",
    ])
    prs = json.loads(raw)
    # GitHub's range is inclusive on both ends; drop anything that landed on the
    # closing Tuesday, which belongs to the next week.
    return [p for p in prs if p["mergedAt"][:10] < end.isoformat()]


def is_issue(number, cache={}):
    if number not in cache:
        kind = sh([
            "gh", "api", f"repos/{REPO}/issues/{number}",
            "--jq", 'if .pull_request then "PR" else "ISSUE" end',
        ]).strip()
        cache[number] = kind == "ISSUE"
    return cache[number]


def issue_meta(number):
    raw = sh([
        "gh", "api", f"repos/{REPO}/issues/{number}",
        "--jq", "{title:.title,state:.state,labels:[.labels[].name]}",
    ])
    return json.loads(raw)


def is_release_action(meta):
    """Release bookkeeping belongs to the GitHub release milestone, not Feishu tasks."""
    return meta["title"].lower().startswith("release:")


def feishu_tasks():
    tasks, offset = [], 0
    while True:
        raw = sh([
            "lark-cli", "base", "+record-list", "--base-token", BASE,
            "--table-id", TASKS, "--as", "user", "--limit", "200",
            "--offset", str(offset), "--json",
        ])
        data = json.loads(raw)["data"]
        fields = data["fields"]
        batch = [
            dict(zip(fields, row), _id=rid)
            for rid, row in zip(data["record_id_list"], data["data"])
        ]
        tasks.extend(batch)
        if not data.get("has_more"):
            return tasks
        if not batch:
            sys.exit("record-list returned has_more with an empty page")
        offset += len(batch)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--week-start", help="Tuesday that starts the window (YYYY-MM-DD)")
    ap.add_argument("--out", default="/tmp/weekly-draft.json")
    args = ap.parse_args()

    start, end = week_window(args.week_start)
    prs = merged_prs(start, end)

    refs = {}
    for p in prs:
        for n in set(REF.findall(p["body"] or "")):
            refs.setdefault(n, []).append(p)

    issues = {n: v for n, v in refs.items() if is_issue(n)}
    tasks = feishu_tasks()

    by_issue = {}
    for t in tasks:
        m = re.search(r"issues/(\d+)", str(t.get("GitHub Issue") or ""))
        if m:
            by_issue[m.group(1)] = t

    new, update, skipped_release = [], [], []
    for n, plist in sorted(issues.items(), key=lambda kv: int(kv[0])):
        plist = sorted(plist, key=lambda p: p["createdAt"])
        meta = issue_meta(n)
        entry = {
            "issue": n,
            "issue_title": meta["title"],
            "issue_state": meta["state"],
            "issue_url": f"https://github.com/{REPO}/issues/{n}",
            "prs": [p["number"] for p in plist],
            "pr_field": " ".join(
                f"[#{p['number']}](https://github.com/{REPO}/pull/{p['number']})" for p in plist
            ),
            "pr_count": len(plist),
            "first_created": plist[0]["createdAt"][:10],
            "last_merged": plist[-1]["mergedAt"][:10],
        }
        task = by_issue.get(n)
        if is_release_action(meta):
            skipped_release.append({
                "issue": n,
                "issue_title": meta["title"],
                "issue_url": entry["issue_url"],
                "prs": entry["prs"],
            })
            continue
        if task is None:
            # Judgement fields the agent must fill before write.py will accept it.
            entry.update({"任务": None, "状态": None, "优先级": None,
                          "产品线": None, "里程碑": None, "验收标准": None})
            new.append(entry)
        else:
            entry["record_id"] = task["_id"]
            entry["task_title"] = task.get("任务")
            entry["task_status"] = task.get("状态")
            entry["has_done_date"] = bool(task.get("完成日期"))
            update.append(entry)

    unlinked = [p["number"] for p in prs if not REF.search(p["body"] or "")]
    draft = {
        "window": {"start": start.isoformat(), "end": end.isoformat()},
        "stats": {
            "prs": len(prs),
            "referenced": len(refs),
            "real_issues": len(issues),
            "pr_numbers_mistaken_for_issues": sorted(set(refs) - set(issues), key=int),
            "unlinked_prs": unlinked,
            "skipped_release_issues": skipped_release,
        },
        "new": new,
        "update": update,
    }
    with open(args.out, "w") as fh:
        json.dump(draft, fh, ensure_ascii=False, indent=2)

    print(f"window   {start} .. {end} (exclusive)")
    print(f"PRs      {len(prs)}")
    print(f"issues   {len(issues)} real ({len(refs) - len(issues)} refs were PR numbers)")
    print(f"new      {len(new)} tasks to create")
    print(f"update   {len(update)} existing tasks to refresh")
    if skipped_release:
        print(f"release  {[item['issue'] for item in skipped_release]}  <- skipped release bookkeeping")
    if unlinked:
        print(f"unlinked {unlinked}  <- PRs with no issue reference")
    print(f"draft    {args.out}")


if __name__ == "__main__":
    main()
