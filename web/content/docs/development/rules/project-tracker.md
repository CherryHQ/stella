---
title: Project tracker
description: Feishu planning and GitHub execution workflow for Stella.
---

Stella uses two trackers with separate ownership:

- **Feishu Base** owns internal planning: Roadmap, product milestones,
  candidate and committed tasks, priority, target dates, and delivery
  ownership.
- **GitHub** owns the public and engineering record: community intake, issue
  details, technical discussion, assignees, pull requests, and release scope.

Do not create a GitHub Project or duplicate the same editable field in both
systems.

Planning flows one way. A Feishu task creates or links a GitHub issue when the
team commits to it, so a committed task always has an issue. The reverse is not
required: an issue with no Feishu task is normal and valid, whether it came from
the community or from a maintainer who started coding first. The Tuesday
delivery review reconciles those issues back into tasks. Never block work on
creating a task first.

The planning Base is
[Stella Roadmap](https://mcnnox2fhjfq.feishu.cn/base/BEEbbI9jtad6PmsYSXpcmBy2nUd?table=tbl4pUhlngTJdg2Z&view=vewBhCvlG1).

## Ownership

| Concern                                                        | Source of truth          | Notes                                          |
| -------------------------------------------------------------- | ------------------------ | ---------------------------------------------- |
| Roadmap and product direction                                  | Feishu Roadmap           | Internal planning, not copied to GitHub        |
| Product milestone and acceptance                               | Feishu Milestones        | Outcome-oriented; may span releases            |
| Candidate work, commitment, priority, dates, DRI, dependencies | Feishu Tasks             | A task becomes committed when it enters `就绪` |
| Public request and technical discussion                        | GitHub Issue             | Community issues remain here before commitment |
| Implementation and current engineer                            | GitHub Issue and PR      | Use assignees, links, and comments             |
| Release scope                                                  | GitHub release milestone | Version names only, such as `v0.61.0`          |
| Code delivery                                                  | GitHub PR                | A PR must link its issue                       |

The Feishu task's DRI owns delivery; the GitHub assignee is the current
implementer. They may be different people.

## Milestone semantics

Product milestones and release milestones are deliberately different:

- A **Feishu product milestone** describes an outcome, such as “Multimodal
  model support”.
- A **GitHub release milestone** describes a version, such as `v0.61.0`.

Never synchronize or reuse names between them. The linked GitHub issue carries
the target release when one is known. Do not create thematic GitHub milestones
or `release:*` labels.

## Feishu structure

```text
Roadmap
  └── Milestones
        └── Tasks ── GitHub Issue URL
```

Use the canonical fields and lifecycle values below. Do not add synonyms or
one-off states.

### Coordinates

Use these directly — do not re-resolve or guess them from the Base URL.

| Resource              | Id                            |
| --------------------- | ----------------------------- |
| Base token            | `BEEbbI9jtad6PmsYSXpcmBy2nUd` |
| Tasks (`任务`)        | `tbl4pUhlngTJdg2Z`            |
| Milestones (`里程碑`) | `tblCRcuKDjmnKCJr`            |
| Roadmap               | `tblp9iIcKyO9NN00`            |

### Fields

`select` values are the complete allowed set. `user` fields take
`[{"id":"ou_..."}]`; resolve the id with `lark-cli contact +get-user` (omit the
user id for yourself). `link` fields take `[{"id":"recXXXXXXXXXX"}]` referencing a
record in the table named in the Links column — read that table first to get the
id; never invent one.

**Tasks**

| Field                  | Type     | Values / links to                                         |
| ---------------------- | -------- | --------------------------------------------------------- |
| `任务`                 | text     | The task title                                            |
| `状态`                 | select   | `待评估`, `就绪`, `进行中`, `阻塞`, `已完成`, `已取消`    |
| `优先级`               | select   | `P0`, `P1`, `P2`                                          |
| `产品线`               | select   | `数字员工`, `数字分身`, `平台核心`, `渠道`, `Web`, `运维` |
| `DRI`                  | user     | —                                                         |
| `里程碑`               | link     | Milestones                                                |
| `父任务`               | link     | Tasks                                                     |
| `依赖任务`             | link     | Tasks                                                     |
| `GitHub Issue`         | text     | Full URL, not a number                                    |
| `验收标准`             | text     | Acceptance criteria                                       |
| `触发条件`             | text     | What unblocks a `待评估` task                             |
| `依赖说明`             | text     | —                                                         |
| `Refs`                 | text     | —                                                         |
| `开始日期`, `截止日期` | datetime | `YYYY-MM-DD HH:MM:SS`                                     |
| `估算(人/天)`          | number   | —                                                         |

**Milestones**

| Field        | Type     | Values / links to                                      |
| ------------ | -------- | ------------------------------------------------------ |
| `里程碑`     | text     | The milestone name                                     |
| `状态`       | select   | `候选`, `已计划`, `进行中`, `暂停`, `已完成`, `已取消` |
| `目标与验收` | text     | —                                                      |
| `DRI`        | user     | —                                                      |
| `所属战线`   | link     | Roadmap                                                |
| `任务`       | link     | Tasks                                                  |
| `目标日期`   | datetime | —                                                      |
| `预计人周`   | number   | —                                                      |

**Roadmap**

| Field        | Type   | Values / links to                  |
| ------------ | ------ | ---------------------------------- |
| `战线`       | text   | The workstream name                |
| `一句话方向` | text   | —                                  |
| `状态`       | select | `候选`, `进行中`, `暂停`, `已完成` |
| `DRI`        | user   | —                                  |
| `里程碑`     | link   | Milestones                         |

### Writing to the Base

Use `lark-cli base` with `--as user`. Confirm fields before writing, and preview
any write with `--dry-run` first.

```bash
BASE=BEEbbI9jtad6PmsYSXpcmBy2nUd
TASKS=tbl4pUhlngTJdg2Z

# confirm the live field set before constructing a payload
lark-cli base +field-list --base-token $BASE --table-id $TASKS --as user

# create a task (drop --dry-run to execute)
lark-cli base +record-upsert --base-token $BASE --table-id $TASKS --as user --dry-run \
  --json '{"任务":"...","状态":"就绪","优先级":"P2","产品线":"平台核心",
           "GitHub Issue":"https://github.com/CherryHQ/stella/issues/123",
           "DRI":[{"id":"ou_..."}],"验收标准":"..."}'

# write back an Issue URL on an existing task
lark-cli base +record-upsert --base-token $BASE --table-id $TASKS --as user \
  --record-id recXXXXXXXXXX --json '{"GitHub Issue":"https://github.com/CherryHQ/stella/issues/123"}'
```

`+record-upsert` creates without `--record-id` and updates with it; it does not
match on a business key. Its response does not echo the stored row, so read the
record back (`+record-search`) rather than trusting the exit status.

### Lifecycle rules

`待评估` tasks are internal candidates and do not need a GitHub issue. Every
task at `就绪` or later must contain a full GitHub Issue URL. Store the URL,
not only the issue number. This requirement runs task to issue only; an issue
without a task is not a gap to fix on the spot. Draft acceptance in Feishu while evaluating a task.
When promoting it to `就绪`, copy the complete requirement and acceptance
criteria to GitHub; the issue becomes the execution source of truth.

## Community issue triage

Issue forms add `status:needs-triage` to new community reports.

```text
New GitHub issue
  ├── invalid / duplicate / question → explain and close or redirect
  ├── valid, not scheduled           → status:accepted; GitHub only
  └── committed                      → status:ready; task optional, backfilled Tuesday
```

During triage:

1. Reproduce or clarify the request.
2. Apply the appropriate type label (`bug`, `enhancement`, `documentation`, or
   `design`).
3. Remove `status:needs-triage`.
4. If valid but unscheduled, add `status:accepted`. Do not create a Feishu task.
5. If committed, remove `status:accepted` and add `status:ready`. Create a
   Feishu task now only if you are planning the work there; otherwise leave it
   and let the Tuesday review backfill the task. When you do create one, search
   both systems for duplicates first, store the Issue URL, and move it to
   `就绪`.
6. Add a version milestone only when the target release is known.

## Execution lifecycle

| Event                  | Feishu Task                | GitHub                                       |
| ---------------------- | -------------------------- | -------------------------------------------- |
| Candidate              | `待评估`                   | No issue required                            |
| Committed, not started | `就绪`                     | Open + `status:ready`                        |
| A PR is open           | `进行中`                   | Remove `status:ready`; assign or link the PR |
| Blocked                | `阻塞`                     | Add `status:blocked` and explain the blocker |
| Implementation closes  | unchanged until acceptance | Close through the PR                         |
| Acceptance passes      | `已完成`                   | Closed                                       |
| Cancelled              | `已取消`                   | Close with the reason                        |

The Feishu column applies once a task exists. Work tracked only on GitHub is
legitimate and complete on its own; the Tuesday review creates the task
afterwards.

An open PR is the marker for `进行中` because it is the one transition with an
objective timestamp. Set the state that matches reality: work already
implemented when its issue is filed goes straight to `进行中` and never wears
`status:ready`.

Closing an issue does not automatically complete the Feishu task; the DRI
marks it `已完成` after acceptance.

## Maintainer-planned work

Create internal ideas as `待评估` Tasks only when they are concrete enough to
estimate or compare. Do not create speculative GitHub issues. When the team
commits a task:

1. Search GitHub for an existing community issue.
2. Link it when one exists; otherwise create a new issue.
3. Put the full Issue URL and final acceptance criteria in the Feishu task, then
   move it to the state matching real progress — `就绪`, or `进行中` when a PR
   is already open.
4. Add the matching status label and, when known, a version milestone to the
   issue.

Work that starts on GitHub takes the short path: file the issue, label it, and
build. The Tuesday review turns it into a task.

Contributors do not need Feishu access. Maintainers own the Feishu promotion
step when accepting community work.

## GitHub labels

Project-tracking labels are intentionally small:

- Type and resolution: `bug`, `enhancement`, `documentation`, `design`,
  `duplicate`, `invalid`, `question`, `wontfix`.
- Intake and execution: `status:needs-triage`, `status:accepted`,
  `status:ready`, `status:blocked`.
- Contributor help: `good first issue`, `help wanted`.

Automation-owned labels such as `dependencies`, `go`, and `javascript` may
remain on generated pull requests. Do not add priority labels; priority belongs
to Feishu. Do not add release labels; the version milestone is the release
record.

Common operations:

```bash
gh issue list --repo CherryHQ/stella --label status:needs-triage
gh issue edit <number> --repo CherryHQ/stella \
  --remove-label status:needs-triage --add-label status:accepted
gh issue edit <number> --repo CherryHQ/stella \
  --remove-label status:accepted --add-label status:ready
gh issue edit <number> --repo CherryHQ/stella --milestone v0.61.0
```

## Issue and pull request conventions

Use an issue form for community reports. Maintainer-created implementation
issues use **What**, **Why**, **How**, and **Refs**. Keep issue descriptions
current and do not copy issue comments into Feishu.

Most PRs link a GitHub issue. Use `Closes #123` when the PR completes it, or
`Refs #123` for partial work. Complete the PR template's What, Why, How, Test,
and Refs sections.

A small fix does not need an issue. Open the PR directly when the change is
self-contained enough that a reviewer can judge it from the diff: a typo, a
one-line bug fix, a doc correction, a test fix. Write `No issue: <reason>` in
Refs instead of a number. File an issue when the change needs discussion,
alters external behavior, or spans several areas — anything a reviewer would
want the background for.

## Creating issues for a user

Before creating an issue on a user's behalf:

1. Draft and confirm its title and body.
2. Choose the type label.
3. Choose the status label: `status:accepted` when the work is accepted but
   unscheduled, `status:ready` when it is committed and not started, and none
   when a PR is already open.

Then create the issue and return its URL. Do not ask about a Feishu task; the
Tuesday review reconciles it. Add a version milestone only when the target
release is already known. Do not bulk-create issues without confirmation, and
prefer closing over deleting.

## Weekly delivery review

Every Tuesday, reconcile the week that just closed against the Base: collect the
merged PRs, create or refresh the matching tasks, and report what shipped. The
delivery week runs Tuesday to Tuesday. This is where issues that were delivered
without a Feishu task get one, which is why nothing upstream has to wait on task
creation.

The `weekly-delivery` skill in `.agents/skills/weekly-delivery/` carries the
procedure. Its scripts own the mechanical half — window arithmetic, PR
collection, telling issue numbers from PR numbers, diffing against the task
table — and stop for a human on every judgement: task title, status, product
line, milestone, and acceptance criteria.

Tasks record delivery in `完成日期`, the merge date of the last PR that
delivered them. `截止日期` stays a deadline. The `交付周` and `周次` formulas
derive from `完成日期`, so the `上周交付` view and the `交付总览` dashboard roll
over on their own.

After a reviewed delivery write, link each delivery PR back to its Feishu Task.
Set the linked Issue's GitHub release milestone only when the complete issue
shipped in that release. Release tags and release-branch cherry-picks establish
that fact; merge time alone does not. This remains GitHub release metadata, not
a copy of the Feishu product milestone.

## Automation boundary

The workflow is manual until repeated evidence justifies automation. The only
safe initial automation candidates are:

1. create or link an issue when a task enters `就绪` and write back its URL;
2. notify the Feishu task when the linked issue is closed or reopened.

Do not bidirectionally synchronize descriptions, comments, assignees,
priorities, dates, milestones, or deletion. Each field already has one owner.
