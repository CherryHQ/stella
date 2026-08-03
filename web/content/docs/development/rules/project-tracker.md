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

- Roadmap: `战线`, `一句话方向`, `状态` (`候选`, `进行中`, `暂停`, `已完成`),
  `DRI`, `里程碑`.
- Milestones: `里程碑`, `状态` (`候选`, `已计划`, `进行中`, `暂停`,
  `已完成`, `已取消`), `目标与验收`, `DRI`, `目标日期`, `预计人周`,
  `所属战线`, `任务`.
- Tasks: `任务`, `状态` (`待评估`, `就绪`, `进行中`, `阻塞`, `已完成`,
  `已取消`), `优先级`, `DRI`, `里程碑`, `GitHub Issue`, `验收标准`,
  dates, estimate, `父任务`, `依赖任务`, `依赖说明`, `触发条件`, product
  line, and references.

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

| Event                 | Feishu Task                | GitHub                                       |
| --------------------- | -------------------------- | -------------------------------------------- |
| Candidate             | `待评估`                   | No issue required                            |
| Committed and ready   | `就绪`                     | Open + `status:ready`                        |
| Work starts           | `进行中`                   | Remove `status:ready`; assign or link a PR   |
| Blocked               | `阻塞`                     | Add `status:blocked` and explain the blocker |
| Implementation closes | unchanged until acceptance | Close through the PR                         |
| Acceptance passes     | `已完成`                   | Closed                                       |
| Cancelled             | `已取消`                   | Close with the reason                        |

Closing an issue does not automatically complete the Feishu task; the DRI
marks it `已完成` after acceptance.

## Maintainer-planned work

Create internal ideas as `待评估` Tasks only when they are concrete enough to
estimate or compare. Do not create speculative GitHub issues. When the team
commits a task:

1. Search GitHub for an existing community issue.
2. Link it when one exists; otherwise create a new issue.
3. Put the full Issue URL and final acceptance criteria in the Feishu task, then
   move it to `就绪`.
4. Add `status:ready` and, when known, a version milestone to the issue.

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

Then create the issue. For committed work, create or update the Feishu task
with the returned URL, move it to `就绪`, and add `status:ready`; finally return
the Issue URL. Do not bulk-create issues without confirmation, and prefer
closing over deleting.

## Automation boundary

The workflow is manual until repeated evidence justifies automation. The only
safe initial automation candidates are:

1. create or link an issue when a task enters `就绪` and write back its URL;
2. notify the Feishu task when the linked issue is closed or reopened.

Do not bidirectionally synchronize descriptions, comments, assignees,
priorities, dates, milestones, or deletion. Each field already has one owner.
