---
name: feishu-github-sync
description: >-
  Build and operate a Feishu (Lark) Bitable that mirrors GitHub Issues for
  lightweight project management — a product/requirements table in Feishu that
  spawns GitHub issues on demand and pulls engineering status back. Use this
  skill whenever the user wants to sync a 飞书多维表格 / Feishu Bitable with GitHub
  issues, set up a requirements-tracking or project-management table backed by
  GitHub, or asks to "同步 github issue", "把需求同步到 GitHub", or "建一个项目管理表格".
  Covers both the one-time table setup (建表向导) and the recurring on-demand sync.
---

# Feishu ↔ GitHub Issue Sync

Mirror a Feishu Bitable of product requirements onto GitHub Issues. Feishu owns
intent (what to build, priority, type, roadmap); GitHub owns execution (labels,
assignee, milestone, open/closed). The sync is **on-demand** — a Python script
the user runs when they want, not a daemon.

This skill orchestrates two already-installed CLIs and adds no third-party deps:

- `lark-cli` — Feishu Bitable read/write (run `--as user`; see cheatsheet for why)
- `gh` — GitHub issue create/view

## Known tables

Already-mapped tables — use these coordinates directly, do not re-resolve or guess:

| Wiki link                                                                                                             | Repo              | base-token                    | table-id           |
| --------------------------------------------------------------------------------------------------------------------- | ----------------- | ----------------------------- | ------------------ |
| [Stella 项目](https://mcnnox2fhjfq.feishu.cn/wiki/LUdiwZfufikjBuk8Kqucpk6bnnd?table=tblqsbIpSO6Px3qj&view=vewOuD1702) | `CherryHQ/stella` | `J77YbQKKWaUCe4sPL2CcG24EnJe` | `tblqsbIpSO6Px3qj` |

⚠️ The same base contains a decoy table `内部需求整理` (`tbljZ3glEozfhoOG`) with a
different schema (Stella处理状态/结果/时长) — **not** the sync table. Always confirm
the `table=` param in the URL; the sync contract table is `数据表` (`tblqsbIpSO6Px3qj`).

## When you arrive

Figure out where the user is:

- **Table is in "Known tables" above** → use its coordinates, skip resolution.
- **No table yet, or unsure** → run the setup guide below first.
- **Table exists and is mapped** → skip to "Running the sync".

Don't assume. Ask which repo and which Feishu table/wiki link before touching
anything.

## The sync contract

These eight rules define the source of truth. They are the spec — preserve them
through any change:

1. Once a GitHub issue exists, **研发状态 (dev status) follows GitHub**, never Feishu.
2. Feishu can hold a requirement **without** immediately creating a GitHub issue.
3. A GitHub issue is created **only when 需求状态 = 已接受** (accepted) and there is
   no GitHub URL yet.
4. **Priority / Type / Roadmap / 需求状态 stay in Feishu** — the sync never overwrites them.
5. **Labels / Assignee / Milestone / Closed State follow GitHub** — pulled back into Feishu.
6. Comments are **not** synced, either direction.
7. Every record either has a GitHub URL or is plainly in an un-synced state.
8. Every automated sync writes **Last Synced At** and **Sync Status**.

### Two passes

- **Pass A (Feishu → GitHub):** records with 需求状态=已接受 and no URL → `gh issue create`
  → write back GitHub URL, 研发状态=To Do, Sync Status=已同步, Last Synced At.
- **Pass B (GitHub → Feishu):** records with a URL → `gh issue view` → write back
  Labels / Assignee / Milestone / Closed State, recompute 研发状态, Sync Status, Last Synced At.

Either pass failing on a record sets that record's Sync Status=同步失败 + Last Synced At.

### 研发状态 (dev status) mapping

Computed in Pass B from GitHub state, never set by hand:

- issue **closed** → `Done`
- label `status:in-review` → `In Review`
- label `status:in-progress` → `In Progress`
- label `status:todo` → `To Do`
- otherwise → `To Do`

First match wins, in the order above. Change `LABEL_TO_DEV_STATUS` in the script
to adjust.

## Setup guide (建表向导)

Run this once per table. Goal: a Bitable whose fields map cleanly onto the
contract above.

1. **Resolve the table.** The user gives a wiki link like
   `https://…/wiki/<node>?table=<table_id>`. The `table=` query param is the
   `table_id`. The base token (`app_token`) comes from resolving the wiki node —
   see the cheatsheet for the resolution call. You need both `base-token` and
   `table-id`.
2. **Verify identity & scopes.** Confirm `lark-cli` is authed `--as user` with
   base read+write. If a call returns code `99991672` the token is expired or
   missing scope → have the user run `lark-cli auth login --domain all`.
3. **Check existing fields** (`+field-list`). Map to the required set below.
   Only create what's missing — never blow away a field that's close.

### Required fields

| Field (Feishu name) | Type          | Owner  | Notes                                                 |
| ------------------- | ------------- | ------ | ----------------------------------------------------- |
| 标题                | text          | Feishu | issue title                                           |
| 描述                | text          | Feishu | issue body                                            |
| 优先级              | single-select | Feishu | e.g. P0–P3                                            |
| 类型                | single-select | Feishu | Bug/Feature/Task/…                                    |
| 路线图              | single-select | Feishu | e.g. Q1–Q4/Backlog (optional)                         |
| 需求状态            | single-select | Feishu | **待评估 / 已接受 / 已拒绝** — 已接受 triggers Pass A |
| 研发状态            | single-select | GitHub | To Do / In Progress / In Review / Done                |
| GitHub URL          | text          | sync   | written by Pass A                                     |
| GitHub Labels       | text          | GitHub | comma-joined                                          |
| GitHub Milestone    | text          | GitHub |                                                       |
| GitHub Assignee     | text          | GitHub | comma-joined logins                                   |
| GitHub Closed State | single-select | GitHub | Open / Closed                                         |
| Sync Status         | single-select | sync   | 未同步 / 已同步 / 同步失败                            |
| Last Synced At      | datetime      | sync   |                                                       |

Field names are the script's keys — if you rename a field, update the `F_*`
constants at the top of `scripts/feishu_github_sync.py` to match.

The lark-cli quirks for every step here (wiki resolution, write scopes, the
`--yes` flag on field updates, cell value read/write shapes, the columnar
record-list format, URL auto-linkify) are documented in
`references/lark-base-cheatsheet.md`. Read it before doing any field/record
operation — these gotchas are not guessable.

## Running the sync

`scripts/feishu_github_sync.py` takes config via flags or env vars and errors
loudly if base-token/table-id/repo are missing.

```bash
# dry run first — prints intended changes, writes nothing
python3 scripts/feishu_github_sync.py \
  --base-token <app_token> --table-id <table_id> --repo owner/name --dry-run

# real run, both passes
python3 scripts/feishu_github_sync.py \
  --base-token <app_token> --table-id <table_id> --repo owner/name

# only one direction
python3 scripts/feishu_github_sync.py … --pass a   # Feishu → GitHub
python3 scripts/feishu_github_sync.py … --pass b   # GitHub → Feishu
```

Env equivalents: `FEISHU_BASE_TOKEN`, `FEISHU_TABLE_ID`, `GH_REPO` (plus
`LARK_CLI`, `GH_CLI`, `LARK_IDENTITY` to override binaries/identity).

**Always dry-run first** on a table you haven't synced before — it shows exactly
which records would create issues and which would be pulled, with no writes.

## Guardrails

- Never commit the script into a target repo unless the user asks — it's an
  operator tool, not project code.
- Never create a GitHub issue for a record whose 需求状态 ≠ 已接受 (rule 3).
- Never overwrite a Feishu-owned field (优先级/类型/路线图/需求状态) from GitHub (rule 4).
- If issue deletion is needed, prefer **closing** — `gh issue delete` usually
  lacks permission.
