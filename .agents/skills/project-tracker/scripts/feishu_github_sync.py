#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.12"
# dependencies = []
# ///
"""按需同步飞书多维表格(Base) <-> GitHub Issues。

编排已登录的 lark-cli (--as user) 和 gh，不引第三方依赖。手动运行。

配置（CLI 参数优先于环境变量）：
    --base-token / FEISHU_BASE_TOKEN   多维表格 base token（不是 wiki token！见 SKILL.md）
    --table-id   / FEISHU_TABLE_ID     数据表 id（tbl 开头）
    --repo       / GH_REPO             GitHub 仓库 owner/name

示例：
    python3 feishu_github_sync.py --base-token bascn... --table-id tbl... --repo owner/repo
    python3 feishu_github_sync.py --dry-run        # 只打印将做的改动，不写入
    python3 feishu_github_sync.py --pass a         # 只跑飞书 -> GitHub
    python3 feishu_github_sync.py --pass b         # 只跑 GitHub -> 飞书

同步契约（与表设计一致，详见 SKILL.md）：
  - Pass A 飞书->GitHub：需求状态=已接受 且 无 GitHub URL 的记录 -> gh issue create
        -> 回写 GitHub URL / 研发状态=To Do / Sync Status=已同步 / Last Synced At
  - Pass B GitHub->飞书：有 GitHub URL 的记录 -> gh issue view
        -> 回写 Labels / Assignee / Milestone / Closed State / 研发状态(label 驱动)
           / Sync Status=已同步 / Last Synced At
  - 任一步失败 -> 该记录 Sync Status=同步失败 / Last Synced At
  - 评论不同步；优先级/类型/路线图/需求状态 留在飞书，不被覆盖。
"""

from __future__ import annotations

import argparse
import json
import logging
import os
import re
import subprocess
import sys
from datetime import datetime
from pathlib import Path


def _project_name() -> str:
    """日志目录用的项目名：优先 git 顶层目录名，否则当前目录名。"""
    try:
        top = subprocess.run(
            ["git", "rev-parse", "--show-toplevel"],
            capture_output=True, text=True, check=True,
        ).stdout.strip()
        if top:
            return Path(top).name
    except Exception:  # noqa: BLE001
        pass
    return Path.cwd().name


LOG_DIR = Path.home() / ".agents/sessions" / _project_name() / "logs"
LOG_DIR.mkdir(parents=True, exist_ok=True)

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(message)s",
    datefmt="%Y-%m-%d %H:%M:%S",
    handlers=[
        logging.StreamHandler(sys.stderr),
        logging.FileHandler(LOG_DIR / "feishu_github_sync.log"),
    ],
)
logger = logging.getLogger("feishu_github_sync")

LARK_CLI = os.environ.get("LARK_CLI", "lark-cli")
GH_CLI = os.environ.get("GH_CLI", "gh")
IDENTITY = os.environ.get("LARK_IDENTITY", "user")  # Base 是用户资源，默认 user 身份

# 研发状态：GitHub label -> 飞书选项。已关闭的 issue 一律视为 Done。
# 按下面顺序匹配，命中第一个即用；都不命中且未关闭则回退 To Do。
# 想换成别的 label 约定（或接 GitHub Projects 看板），改这里即可。
LABEL_TO_DEV_STATUS = [
    ("status:in-review", "In Review"),
    ("status:in-progress", "In Progress"),
    ("status:todo", "To Do"),
]
DEV_STATUS_CLOSED = "Done"
DEV_STATUS_DEFAULT = "To Do"

# 飞书字段名（与表结构一致）。表里字段叫别的名字时，改这些常量。
F_TITLE = "标题"
F_DESC = "描述"
F_REQ_STATUS = "需求状态"
F_DEV_STATUS = "研发状态"
F_URL = "GitHub URL"
F_LABELS = "GitHub Labels"
F_MILESTONE = "GitHub Milestone"
F_ASSIGNEE = "GitHub Assignee"
F_CLOSED = "GitHub Closed State"
F_SYNC = "Sync Status"
F_SYNCED_AT = "Last Synced At"

REQ_ACCEPTED = "已接受"
SYNC_OK = "已同步"
SYNC_FAIL = "同步失败"


def now_str() -> str:
    return datetime.now().strftime("%Y-%m-%d %H:%M:%S")


# ---- 子进程封装 ------------------------------------------------------------
def run(cmd: list[str], input_str: str | None = None) -> str:
    proc = subprocess.run(cmd, input=input_str, capture_output=True, text=True)
    if proc.returncode != 0:
        raise RuntimeError(
            f"命令失败 ({proc.returncode}): {' '.join(cmd)}\n{proc.stderr or proc.stdout}"
        )
    return proc.stdout


def lark_json(cfg: "Config", args: list[str]) -> dict:
    out = run([LARK_CLI, "base", *args, "--as", IDENTITY])
    data = json.loads(out)
    if not data.get("ok", False):
        raise RuntimeError(
            f"lark-cli 返回错误: {json.dumps(data.get('error'), ensure_ascii=False)}"
        )
    return data["data"]


# ---- 单元格归一化（读取） --------------------------------------------------
_MD_LINK = re.compile(r"^\[(?P<text>.*)\]\((?P<url>.*)\)$")
_ISSUE_NO = re.compile(r"/issues/(\d+)")


def norm_text(v) -> str:
    """文本/单选 单元格 -> 字符串。单选回读是 ['值']，user 是 [{id,name}]。"""
    if v is None:
        return ""
    if isinstance(v, list):
        if not v:
            return ""
        first = v[0]
        if isinstance(first, dict):
            return first.get("name", "")
        return str(first)
    return str(v)


def bare_url(v) -> str:
    """GitHub URL 单元格 -> 裸 URL（飞书会把 URL 存成 [url](url) markdown）。"""
    s = norm_text(v)
    m = _MD_LINK.match(s)
    return m.group("url") if m else s


# ---- 读取 / 写入记录 -------------------------------------------------------
def fetch_records(cfg: "Config") -> list[dict]:
    """返回 [{'record_id':..., 'fields': {字段名: 原始值}}]，自动翻页。

    record-list --format json 是列式：data.data(行) 与 record_id_list 并列，
    列按 data.fields 顺序。
    """
    records: list[dict] = []
    offset = 0
    while True:
        data = lark_json(cfg, [
            "+record-list", "--base-token", cfg.base_token, "--table-id", cfg.table_id,
            "--offset", str(offset), "--limit", "200", "--format", "json",
        ])
        names, ids, rows = data["fields"], data["record_id_list"], data["data"]
        for rid, row in zip(ids, rows):
            records.append({"record_id": rid, "fields": dict(zip(names, row))})
        if not data.get("has_more"):
            break
        offset += len(rows)
    return records


def update_record(cfg: "Config", record_id: str, patch: dict, dry_run: bool) -> None:
    if dry_run:
        logger.info("[dry-run] 更新 %s: %s", record_id, json.dumps(patch, ensure_ascii=False))
        return
    lark_json(cfg, [
        "+record-upsert", "--base-token", cfg.base_token, "--table-id", cfg.table_id,
        "--record-id", record_id, "--json", json.dumps(patch, ensure_ascii=False),
    ])


def mark_failed(cfg: "Config", record_id: str, dry_run: bool) -> None:
    try:
        update_record(cfg, record_id, {F_SYNC: SYNC_FAIL, F_SYNCED_AT: now_str()}, dry_run)
    except Exception as e:  # noqa: BLE001
        logger.error("标记同步失败也失败了: %s", e)


# ---- Pass A: 飞书 -> GitHub -----------------------------------------------
def pass_a(cfg: "Config", records: list[dict], dry_run: bool) -> None:
    logger.info("== Pass A 飞书 -> GitHub ==")
    todo = [
        r for r in records
        if norm_text(r["fields"].get(F_REQ_STATUS)) == REQ_ACCEPTED
        and not bare_url(r["fields"].get(F_URL))
    ]
    if not todo:
        logger.info("无待创建记录（需求状态=已接受 且 无 GitHub URL）。")
        return
    for r in todo:
        rid = r["record_id"]
        title = norm_text(r["fields"].get(F_TITLE)).strip()
        body = norm_text(r["fields"].get(F_DESC))
        if not title:
            logger.warning("跳过 %s: 标题为空，无法建 issue。", rid)
            mark_failed(cfg, rid, dry_run)
            continue
        logger.info("建 issue: %r", title)
        if dry_run:
            logger.info("[dry-run] gh issue create --repo %s --title %r", cfg.repo, title)
            continue
        try:
            url = run([GH_CLI, "issue", "create", "--repo", cfg.repo,
                       "--title", title, "--body", body]).strip().splitlines()[-1]
            update_record(cfg, rid, {
                F_URL: url,
                F_DEV_STATUS: DEV_STATUS_DEFAULT,
                F_SYNC: SYNC_OK,
                F_SYNCED_AT: now_str(),
            }, dry_run)
            logger.info("-> %s", url)
        except Exception as e:  # noqa: BLE001
            logger.error("失败: %s", e)
            mark_failed(cfg, rid, dry_run)


# ---- Pass B: GitHub -> 飞书 -----------------------------------------------
def dev_status_from(state: str, labels: list[str]) -> str:
    if state.upper() == "CLOSED":
        return DEV_STATUS_CLOSED
    lset = {x.lower() for x in labels}
    for label, status in LABEL_TO_DEV_STATUS:
        if label.lower() in lset:
            return status
    return DEV_STATUS_DEFAULT


def pass_b(cfg: "Config", records: list[dict], dry_run: bool) -> None:
    logger.info("== Pass B GitHub -> 飞书 ==")
    synced = [r for r in records if bare_url(r["fields"].get(F_URL))]
    if not synced:
        logger.info("无已关联 GitHub URL 的记录。")
        return
    for r in synced:
        rid = r["record_id"]
        url = bare_url(r["fields"].get(F_URL))
        m = _ISSUE_NO.search(url)
        if not m:
            logger.warning("跳过 %s: 无法从 URL 解析 issue 号: %s", rid, url)
            mark_failed(cfg, rid, dry_run)
            continue
        num = m.group(1)
        logger.info("拉取 issue #%s", num)
        try:
            out = run([GH_CLI, "issue", "view", num, "--repo", cfg.repo,
                       "--json", "state,labels,assignees,milestone"])
            issue = json.loads(out)
            labels = [lb["name"] for lb in issue.get("labels") or []]
            assignees = [a["login"] for a in issue.get("assignees") or []]
            milestone = (issue.get("milestone") or {}).get("title", "") or ""
            state = issue.get("state", "")
            patch = {
                F_LABELS: ", ".join(labels),
                F_ASSIGNEE: ", ".join(assignees),
                F_MILESTONE: milestone,
                F_CLOSED: "Closed" if state.upper() == "CLOSED" else "Open",
                F_DEV_STATUS: dev_status_from(state, labels),
                F_SYNC: SYNC_OK,
                F_SYNCED_AT: now_str(),
            }
            update_record(cfg, rid, patch, dry_run)
            logger.info("-> 研发状态=%s closed=%s labels=%s",
                        patch[F_DEV_STATUS], patch[F_CLOSED], labels)
        except Exception as e:  # noqa: BLE001
            logger.error("失败: %s", e)
            mark_failed(cfg, rid, dry_run)


class Config:
    def __init__(self, base_token: str, table_id: str, repo: str):
        self.base_token = base_token
        self.table_id = table_id
        self.repo = repo


def main() -> int:
    ap = argparse.ArgumentParser(description="飞书多维表格 <-> GitHub Issue 按需同步")
    ap.add_argument("--base-token", default=os.environ.get("FEISHU_BASE_TOKEN"),
                    help="多维表格 base token（或 env FEISHU_BASE_TOKEN）")
    ap.add_argument("--table-id", default=os.environ.get("FEISHU_TABLE_ID"),
                    help="数据表 id，tbl 开头（或 env FEISHU_TABLE_ID）")
    ap.add_argument("--repo", default=os.environ.get("GH_REPO"),
                    help="GitHub 仓库 owner/name（或 env GH_REPO）")
    ap.add_argument("--pass", dest="which", choices=["a", "b", "both"], default="both",
                    help="只跑某一趟，默认 both")
    ap.add_argument("--dry-run", action="store_true", help="只打印将做的改动，不写入")
    args = ap.parse_args()

    missing = [n for n, v in [("--base-token", args.base_token),
                              ("--table-id", args.table_id),
                              ("--repo", args.repo)] if not v]
    if missing:
        ap.error(f"缺少必填项: {', '.join(missing)}（用 CLI 参数或对应环境变量）")

    cfg = Config(args.base_token, args.table_id, args.repo)
    logger.info("repo=%s base=%s table=%s identity=%s%s",
                cfg.repo, cfg.base_token, cfg.table_id, IDENTITY,
                " [DRY-RUN]" if args.dry_run else "")
    records = fetch_records(cfg)
    logger.info("读取到 %d 条记录。", len(records))

    if args.which in ("a", "both"):
        pass_a(cfg, records, args.dry_run)
        if args.which == "both" and not args.dry_run:
            records = fetch_records(cfg)  # Pass A 写了新 URL，重新读供 Pass B 用
    if args.which in ("b", "both"):
        pass_b(cfg, records, args.dry_run)
    return 0


if __name__ == "__main__":
    sys.exit(main())
