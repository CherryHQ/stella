---
title: Project tracker
description: GitHub Projects and Issues workflow for Stella requirements and status.
---

Manage requirements and development status entirely in GitHub. **GitHub Projects**
owns the product layer (status, priority, roadmap views). **GitHub Issues** are
the execution artifacts (description, labels, assignee, milestone, linked PRs).

Only tool needed: `gh` CLI.

## Known projects

Use these coordinates directly — do not re-resolve or guess:

| Project                                                        | Repo              | Number | Node ID                |
| -------------------------------------------------------------- | ----------------- | ------ | ---------------------- |
| [Stella](https://github.com/orgs/CherryHQ/projects/14/views/1) | `CherryHQ/stella` | 14     | `PVT_kwDOCzFCf84BXEej` |

### Project fields (Stella #14)

| Field      | ID                               | Type          | Options                                                                                    |
| ---------- | -------------------------------- | ------------- | ------------------------------------------------------------------------------------------ |
| Title      | `PVTF_lADOCzFCf84BXEejzhST8rM`   | text          | —                                                                                          |
| Assignees  | `PVTF_lADOCzFCf84BXEejzhST8rQ`   | text          | —                                                                                          |
| **Status** | `PVTSSF_lADOCzFCf84BXEejzhST8rU` | single-select | Backlog (`d98918fd`), Todo (`f75ad846`), In Progress (`47fc9ee4`), Done (`98236657`)       |
| **Week**   | `PVTIF_lADOCzFCf84BXEejzhUocBE`  | iteration     | 7-day cycles starting Monday. Query current iteration IDs before setting `--iteration-id`. |
| Labels     | `PVTF_lADOCzFCf84BXEejzhST8rY`   | text          | —                                                                                          |
| Linked PRs | `PVTF_lADOCzFCf84BXEejzhST8rc`   | text          | —                                                                                          |
| Milestone  | `PVTF_lADOCzFCf84BXEejzhST8rg`   | text          | —                                                                                          |
| Repository | `PVTF_lADOCzFCf84BXEejzhST8rk`   | text          | —                                                                                          |

## Status workflow

```
Backlog → Todo → In Progress → Done
```

- **Backlog** — acknowledged but not yet committed to.
- **Todo** — accepted and scheduled; ready to pick up.
- **In Progress** — actively being worked on.
- **Done** — issue closed (automatically moves here if project auto-close is enabled).

## Common operations

All operations use the `gh` CLI. Owner is `CherryHQ`, project number is `14`.

### View the board

```bash
# all items
gh project item-list 14 --owner CherryHQ --format json

# filter by status (use jq)
gh project item-list 14 --owner CherryHQ --format json \
  | jq '[.items[] | select(.status == "In Progress")]'

# quick summary
gh project item-list 14 --owner CherryHQ
```

### Create an issue and add to project

```bash
# 1. create the issue. Use a heredoc for the body — a "\n" inside a
#    double-quoted --body is written literally, not as a newline.
gh issue create --repo CherryHQ/stella \
  --title "feat: ..." \
  --milestone "v0.42.0" \
  --body "$(cat <<'EOF'
## What
...
## Why
...
## How
...
## Refs
...
EOF
)"

# 2. add it to the project. The "Auto-add to project" workflow already adds new
#    issues automatically, so this is usually redundant (it's idempotent — adding
#    an already-present issue just returns the existing item). Run it to capture
#    the item ID for the next step:
ITEM_ID=$(gh project item-add 14 --owner CherryHQ --url <issue-url> --format json --jq '.id')

# 3. set initial status
gh project item-edit \
  --project-id PVT_kwDOCzFCf84BXEej \
  --id "$ITEM_ID" \
  --field-id PVTSSF_lADOCzFCf84BXEejzhST8rU \
  --single-select-option-id f75ad846   # Todo
```

### Move an item to a different status

```bash
gh project item-edit \
  --project-id PVT_kwDOCzFCf84BXEej \
  --id <item-id> \
  --field-id PVTSSF_lADOCzFCf84BXEejzhST8rU \
  --single-select-option-id <option-id>
```

Status option IDs:

- Backlog: `d98918fd`
- Todo: `f75ad846`
- In Progress: `47fc9ee4`
- Done: `98236657`

### Set week on an item

```bash
# query current iteration IDs
gh api graphql -f query='{
  node(id: "PVT_kwDOCzFCf84BXEej") {
    ... on ProjectV2 {
      field(name: "Week") {
        ... on ProjectV2IterationField {
          configuration {
            iterations { id title startDate }
          }
        }
      }
    }
  }
}'

# set an item to a specific week
gh project item-edit \
  --project-id PVT_kwDOCzFCf84BXEej \
  --id <item-id> \
  --field-id PVTIF_lADOCzFCf84BXEejzhUocBE \
  --iteration-id <iteration-id>
```

### View a specific issue

```bash
gh issue view <number> --repo CherryHQ/stella
gh issue view <number> --repo CherryHQ/stella --json state,labels,assignees,milestone,projectItems
```

### List open issues

```bash
gh issue list --repo CherryHQ/stella
gh issue list --repo CherryHQ/stella --label "bug" --assignee "@me"
```

### Update an issue

```bash
gh issue edit <number> --repo CherryHQ/stella --add-label "priority:p0"
gh issue edit <number> --repo CherryHQ/stella --add-assignee vaayne
gh issue close <number> --repo CherryHQ/stella
```

## Issue conventions

Follow the repo's issue/PR template:

- **What** — the change in one or two sentences.
- **Why** — the motivation or problem it solves.
- **How** — the approach, plan, and design details.
- **Refs** — related issues, PRs, docs, or discussions.

Keep issue and PR descriptions current as the plan evolves.

### Labels

Use labels for categorization. Common patterns:

- `bug`, `feature`, `enhancement`, `docs`
- `priority:p0` through `priority:p3`
- `status:in-review` — PR is open and awaiting review

### Milestones & releases

Every release maps to a GitHub Milestone (e.g., `v0.42.0`). Use milestones to
scope what ships in a release:

```
Plan:    gh issue edit #123 --milestone v0.42.0
Track:   gh issue list --repo CherryHQ/stella --milestone v0.42.0
Release: /release → tag + changelog → close milestone
```

Release mechanics live in [`release.md`](./release.md).

## Creating issues — interaction flow

When creating an issue, **always ask the user** for:

1. **Title and description** — draft from context if available, confirm before creating.
2. **Milestone** — list open milestones (`gh api repos/CherryHQ/stella/milestones --jq '.[].title'`)
   and ask which one to assign. If the list is empty or none fit, ask whether to
   create one (`gh api repos/CherryHQ/stella/milestones -f title=vX.Y.Z`) or skip
   (allow "none" for un-scoped work).
3. **Status** — default Backlog; ask if it should be Todo or In Progress.
4. **Week** — ask if it should be tagged to the current week.

Then execute: create issue → add to project → set Status / Week / Milestone.

## Guardrails

- Prefer **closing** over deleting issues — `gh issue delete` usually lacks permission.
- Don't bulk-create issues without confirmation — ask the user first.
- When creating issues from a list of requirements, create them one at a time and
  confirm each title/body before proceeding.
- Always add new issues to the project board after creation.
- Always ask for milestone assignment when creating issues.
