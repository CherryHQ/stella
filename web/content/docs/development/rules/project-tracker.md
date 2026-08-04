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
systems. A missing Feishu task means that a GitHub issue is not committed to a
delivery plan; it does not mean that the issue is invalid.

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
not only the issue number. Draft acceptance in Feishu while evaluating a task.
When promoting it to `就绪`, copy the complete requirement and acceptance
criteria to GitHub; the issue becomes the execution source of truth.

## Community issue triage

Issue forms add `status:needs-triage` to new community reports.

```text
New GitHub issue
  ├── invalid / duplicate / question → explain and close or redirect
  ├── valid, not scheduled           → status:accepted; GitHub only
  └── committed                      → Feishu Task at 就绪 + status:ready
```

During triage:

1. Reproduce or clarify the request.
2. Apply the appropriate type label (`bug`, `enhancement`, `documentation`, or
   `design`).
3. Remove `status:needs-triage`.
4. If valid but unscheduled, add `status:accepted`. Do not create a Feishu task.
5. If committed, search Feishu and GitHub for duplicates, create or link the
   Feishu task, add its GitHub Issue URL, move it to `就绪`, remove
   `status:accepted`, and add `status:ready`.
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

Every PR must link a GitHub issue. Use `Closes #123` when the PR completes it,
or `Refs #123` for partial work. Complete the PR template's What, Why, How,
Test, and Refs sections.

## Creating issues for a user

Before creating an issue on a user's behalf:

1. Draft and confirm its title and body.
2. Choose the type label.
3. Ask whether the work is merely accepted or already committed.
4. For accepted-only work, add `status:accepted` and stop.
5. For committed work, confirm that a Feishu task will link the new issue, and
   ask whether a version milestone is known. `none` is valid.

Then create the issue. For committed work, create or update the Feishu task with
the returned URL, set the state matching real progress (`就绪`, or `进行中` with
no `status:ready` when a PR is already open), add the matching status label, and
return the Issue URL. Do not bulk-create issues without confirmation, and prefer
closing over deleting.

## Automation boundary

The workflow is manual until repeated evidence justifies automation. The only
safe initial automation candidates are:

1. create or link an issue when a task enters `就绪` and write back its URL;
2. notify the Feishu task when the linked issue is closed or reopened.

Do not bidirectionally synchronize descriptions, comments, assignees,
priorities, dates, milestones, or deletion. Each field already has one owner.
