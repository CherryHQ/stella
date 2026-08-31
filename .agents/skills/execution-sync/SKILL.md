---
name: execution-sync
description: Sync committed Stella Base tasks into the Feishu "Stella" tasklist as daily execution tasks. Use when the user says "同步飞书任务", "execution sync", "同步执行清单", commits a new Base task and wants it in Feishu Tasks, or asks to reconcile Base with the execution tasklist.
---

# Execution sync

The planning Base is the source of truth; the `Stella` Feishu
tasklist is the daily execution surface. One Base record at `就绪` / `进行中` /
`阻塞` maps to exactly one task in the tasklist, placed in the matching
section. `待评估` candidates and terminal records are never synced. The full
contract lives in `web/content/docs/development/rules/project-tracker.md`.

Manual trigger only. Run it after weekly delivery writes the Base, or whenever
a task's commitment changed.

## Commands

```bash
cd .agents/skills/execution-sync/scripts
python3 sync.py plan                    # read-only report, no writes
python3 sync.py apply                   # execute the plan, then verify
python3 sync.py plan --tasklist <guid>  # target a specific tasklist
```

`apply` re-plans after writing and exits non-zero unless Base and the
tasklist agree. `plan` is always safe to run.

## What apply does

- ensures the tasklist has the `就绪 / 进行中 / 阻塞` sections and the
  `Base Record ID` (text), `优先级` (P0/P1/P2), `产品里程碑` (single select)
  custom fields; milestone options follow the Base 里程碑 table automatically
- creates missing tasks (summary, description = Base 描述 + Issue link,
  `origin` link to the GitHub Issue, priority and milestone projected)
- moves tasks between sections when Base 状态 changes
- reopens a task that is `done` while its Base record is still active (a
  premature tick or a stale projection — never silently accepted)
- writes the task GUID and share URL back to the Base `飞书任务GUID` /
  `飞书任务` columns, then verifies

## Invariants

- Idempotency key is the Base `record_id`, stored in Base `飞书任务GUID` and
  in the task's `Base Record ID` custom field. A mismatch is an error, never
  auto-repaired.
- Deleting a task in Feishu makes the next run error out and point at the
  orphaned Base row; clear `飞书任务GUID` only after a human decides.
- Section placement mirrors Base 状态; sections are not an independent state
  machine.
- Completion: the parent task's native `done` is only ever set by hand after
  Base acceptance; the sync reopens premature dones. Delivery stats stay
  Base-only.

## Known limits

- Assignees are not synced yet: the 6 pilot issues had no GitHub assignee, and
  Base has no owner field by rule. Add one-way GitHub → task assignee
  projection only when the team wants it.
- Feishu Tasks has no search-by-custom-field, so indexing scans the whole
  tasklist (one get per task). Fine at pilot volume; revisit if the list
  grows past a few hundred.
