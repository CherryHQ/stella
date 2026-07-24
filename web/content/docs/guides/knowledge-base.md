---
title: Knowledge Base
---

## What the Knowledge Base Is For

The Knowledge Base lets Stella answer from files that you or an administrator deliberately manage. Stella searches the relevant files when a question is likely to depend on company, project, process, or role-specific information, and cites the filename plus an available page or heading.

Regular chat attachments do not enter the Knowledge Base. Upload a file from a Knowledge Base page when you want it to remain searchable.

## Choose Where a File Applies

Stella combines every scope that applies to the current user and agent:

| Where you manage it                             | Who can use it                                       |
| ----------------------------------------------- | ---------------------------------------------------- |
| Agent → Knowledge Base                          | You, while using this agent                          |
| Settings → Knowledge Base                       | You, while using any agent                           |
| Settings → Knowledge Base → System · All agents | Every signed-in user, while using any agent          |
| Settings → Knowledge Base → System · This agent | Every signed-in user, while using the selected agent |

The two system scopes are available only to administrators. These scopes are additive; their order does not imply priority or retrieval weight.

For an employee's digital twin, put information that colleagues should be able to ask that twin about in **System · This agent**. Keep information that only one person should use in that person's own scopes.

## Upload Files

1. Open the current agent's **Knowledge Base** tab, or open **Settings → Knowledge Base**.
2. Administrators can select a system scope and, when needed, an agent.
3. Select or drag one or more files into the upload dialog.
4. Wait for the batch to finish. Each file succeeds or fails independently.

Supported file types are PDF, DOCX, Markdown, and plain text. Each file can be up to 25 MiB.

## Understand File Status

- **Processing** — the original file is stored and Stella is parsing it.
- **Ready** — the file can be returned by Knowledge Base search.
- **Failed** — parsing did not produce a usable searchable document. The row shows a safe error message.

Stella does not poll for status changes. Refresh the browser page to see the latest status.

Files are immutable in this version. To update content, upload the new file, wait for it to become ready, and then delete the old file. Deleting a file removes it and its searchable content immediately.

## Storage Limits

| Quota pool                                                | Files | Original file bytes |
| --------------------------------------------------------- | ----: | ------------------: |
| One user's personal knowledge, across all personal scopes | 2,000 |              10 GiB |
| System · All agents                                       | 4,000 |              20 GiB |
| System · This agent, per agent                            | 1,000 |               5 GiB |

Processing, ready, and failed files all count toward the limits. Deleting a file releases its quota.

## Current Limits

This version does not provide file preview, download, replacement, manual parse retry, optical character recognition, or synchronization from Feishu and other external sources. Knowledge Base file retrieval is available in private signed-in user-and-agent conversations, not group or background-task sessions.
