---
name: weekly-delivery
description: Run the Tuesday delivery review for Stella — collect the week's merged PRs, reconcile them against the Feishu task table, create or refresh tasks, and report what shipped. Use when the user says "跑周报", "weekly delivery", "本周交付", "补飞书任务", or asks what merged in a given week.
---

# Weekly delivery review

The Tuesday ritual: reconcile a week of merged GitHub PRs against the Feishu
planning Base, then report what shipped and where the effort went.

The delivery week runs **Tuesday 00:00 to the next Tuesday 00:00**, matching the
`周次` field in the Base. A run on Tuesday reports the week that just closed.

## Division of labour

Scripts do the mechanical work; you make the judgement calls; V approves before
anything is written.

| Step                                                                  | Owner        | Why                                                             |
| --------------------------------------------------------------------- | ------------ | --------------------------------------------------------------- |
| Window, PR collection, issue extraction, diff against Feishu          | `collect.py` | Deterministic, easy to get subtly wrong by hand                 |
| Task title, 状态, 优先级, 产品线, 里程碑, 验收标准, release milestone | you          | Requires reading the issue, release scope, and roadmap          |
| Approving the draft                                                   | V            | Feishu and GitHub metadata writes need an explicit confirmation |
| Batch write and read-back verification                                | `write.py`   | Deterministic                                                   |

## Procedure

### 1. Collect

```bash
python3 .agents/skills/weekly-delivery/scripts/collect.py            # last closed week
python3 .agents/skills/weekly-delivery/scripts/collect.py --week-start 2026-08-04
```

Writes `/tmp/weekly-draft.json` with three lists: `new` (issues with no Feishu
task), `update` (tasks that already exist and need their PR links, PR count
and 完成日期 refreshed), and `stale` (tasks delivered in an earlier week whose
状态 never followed the issue to closed).

`update` and `stale` both carry 状态 when the linked issue is closed. That is
the only status write the scripts make on their own, and it exists because a
lingering 进行中 clogs the status board.

Read the printed summary before going further. `unlinked_prs` and
`pr_numbers_mistaken_for_issues` are worth a glance but are usually benign —
see Gotchas.

### 2. Fill the judgement fields

For every entry in `new`, fill `任务`, `状态`, `优先级`, `产品线`, `里程碑`,
`验收标准`. `write.py` refuses a draft that still has nulls (except `里程碑`,
which may legitimately stay empty).

**状态** — a merged PR counts as delivered. Closed issue → `已完成`. Issue still
open → `进行中`, because more PRs are coming. On `update` and `stale` entries
`collect.py` already decided this; you only fill it for `new`.

**任务** — a short Chinese phrase describing the outcome, not the diff. Mirror
the issue's intent; do not translate its title literally.

**验收标准** — one sentence, the observable condition that makes it done.

**产品线** — `数字员工` · `数字分身` · `平台核心` · `渠道` · `Web` · `运维`.

**里程碑** — prefer a versioned milestone when the work belongs to one. When it
does not, fall back to the evergreen milestone matching the product line:

| 产品线         | Evergreen milestone |
| -------------- | ------------------- |
| 渠道           | 渠道接入与维护      |
| 运维           | 运维持续维护        |
| 平台核心 / Web | 平台核心持续维护    |

Every milestone name accepted by `write.py` is listed in its `MILESTONES` map.
A new milestone means adding its record id there first.

**Skip entirely**: pure release actions — changelog PRs, release-validation
issues, version bumps. They are recorded by the GitHub release milestone and
only add noise here. Dependabot PRs carry no issue and drop out on their own.

### 3. Show V the draft and wait

Present a compact table: issue, proposed 任务, 状态, 产品线, 里程碑, release
version, PR count.
List `stats.skipped_release_issues` separately so the release-only
classification is visible during approval, and `stale` separately so the
status repairs are visible too.
Call out anything you were unsure about. Do not write before V answers.

### 4. Write

```bash
python3 .agents/skills/weekly-delivery/scripts/write.py --dry-run
python3 .agents/skills/weekly-delivery/scripts/write.py
```

It batch-creates, batch-updates, then reads the table back and fails loudly if
any touched row lacks its PR links.

### 5. Link delivery PRs and release milestones

After the Feishu write succeeds, generate a share link for every touched task
and append a `## Feishu Task` section to every delivery PR that references it.
A PR that delivers two issues carries two task links. Do not manufacture a task
for a small `No issue:` PR or a release-bookkeeping PR.

Set the linked GitHub Issue's **GitHub release milestone** only when its complete
work shipped in that release. The release tag and release PR are the evidence,
not merge time alone: release branches can cherry-pick a PR, and an open Issue
with partial PRs must remain unmilestoned. A closed release milestone can be
assigned through the GitHub Issues API even though `gh issue edit` only lists
open milestones.

This is a GitHub-side delivery record, not a synchronization of the Feishu
product milestone. Never copy names or links between those two milestone types.

### 6. Report

Pull the numbers from the Base rather than recomputing them locally:

```bash
lark-cli base +data-query --base-token BEEbbI9jtad6PmsYSXpcmBy2nUd --as user --format table \
  --dsl '{"datasource":{"type":"table","table":{"tableId":"tbl4pUhlngTJdg2Z"}},
    "dimensions":[{"field_name":"周次","alias":"week"},{"field_name":"里程碑","alias":"ms"}],
    "measures":[{"field_name":"任务","aggregation":"count","alias":"任务数"},
                {"field_name":"PR 数","aggregation":"sum","alias":"PR数"}],
    "filters":{"type":1,"conjunction":"and","conditions":[{"field_name":"完成日期","operator":"isNotEmpty","value":[]}]},
    "sort":[{"field_name":"PR数","order":"desc"}],"shaper":{"format":"flat"}}'
```

Report: tasks delivered, distinct PR count, distribution across milestones, the
two or three tasks that ate the most PRs, and anything that looks off — a
milestone with no movement, a spike in 运维 work, an issue open for weeks.

State the distinct PR count, not only the `PR 数` sum. They differ.

## Where things live

| Thing                          | Id                                                           |
| ------------------------------ | ------------------------------------------------------------ |
| Base                           | `BEEbbI9jtad6PmsYSXpcmBy2nUd`                                |
| 任务 / 里程碑 / Roadmap tables | `tbl4pUhlngTJdg2Z` / `tblCRcuKDjmnKCJr` / `tblp9iIcKyO9NN00` |
| Views                          | `上周交付` `vewMkXaorC` · `本周看板` `vewUOoitcq`            |
| Dashboard                      | `交付总览` `blkmR2p324cxcGva`                                |

Field ownership: `完成日期` is the task's end date — the merge date of the last
PR that delivered it, or the day it was cancelled. It is the only date the
scripts write; per-task start and due dates are not tracked, scheduling lives on
the 里程碑 record. `周次` is a read-only formula over `完成日期` that compares
against `TODAY()` and yields 本周 / 上周 / 更早 / empty, so the views and
dashboard roll over on their own and need no weekly edit. To pin a specific past
week, filter on a `完成日期` range instead.

A cancelled task carries a `完成日期` too, so it retires from the board with its
week. That means any delivery count must filter on `状态 = 已完成`; a bare row
count over `完成日期` includes cancellations.

The full tracker contract lives in
`web/content/docs/development/rules/project-tracker.md`.

## Gotchas

**A PR number is not an issue number.** Stella's stacked PRs reference sibling
PRs under `Refs`. `collect.py` resolves every number through the API and reports
the ones it dropped. They are intentional cross-references, not broken links —
do not go editing merged PR bodies.

**One issue, many PRs.** In the 2026-08-04 week, 65 PRs collapsed to 37 tasks;
one issue alone took 8 PRs. PR count is not task count, and neither is a
throughput measure on its own.

**`PR 数` double counts.** A single PR can close several issues, so summing the
column overstates. For a true count, dedupe `/pull/N` out of the `PR` field.

**Open issues carry partial counts.** `PR 数` on a 进行中 task only covers the
PRs seen so far. When the issue finally closes, the count needs recomputing over
its whole history.

**`isNotEmpty` needs `"value":[]`** in `+data-query` filters, unlike everywhere
else in `lark-cli`.

**Feishu writes do not echo rows.** Always read back; `write.py` does.
