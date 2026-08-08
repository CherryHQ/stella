---
title: Library
---

Use the Library to give Stella durable reference material that Agents can search when it is relevant to a conversation. Files enter the Library only when you upload them from a Library page; ordinary chat attachments are not added automatically.

## Choose who can use a file

Each file belongs to one of four additive ranges. When you talk to an Agent, Stella can search every range that applies to both you and that Agent.

| Range               | Who can manage it | When it is available                      |
| ------------------- | ----------------- | ----------------------------------------- |
| Mine · All Agents   | You               | Whenever you use any Agent                |
| Mine · One Agent    | You               | Only when you use the selected Agent      |
| System · All Agents | Administrators    | Whenever any user uses any Agent          |
| System · One Agent  | Administrators    | Whenever any user uses the selected Agent |

Manage **Mine · All Agents** from **Settings → Library**. Administrators can also select either System range there. To manage **Mine · One Agent**, open the Agent's profile and select **Library**.

These ranges are combined for retrieval; their order does not imply priority. A file in a personal range remains private to its owner, while a file in a System range can inform answers for other users.

## Upload files

1. Open the Library page for the range you want.
2. For **System · One Agent**, select the target Agent.
3. Select **Upload files** and choose one or more files.
4. Review the result for each file, then refresh the page later to see processing updates.

Library V1 accepts plain-text (`.txt`) and Markdown (`.md`, `.markdown`) files up to 25 MiB each. Each file is uploaded independently, so one failed file does not cancel the rest of a batch.

## Understand processing states

- **Processing** — the file is queued or being parsed and is not searchable yet.
- **Ready** — the file can be searched by Agents in the applicable ranges.
- **Failed** — parsing did not complete, so the file is not searchable. Review the displayed error, delete the file, and upload a corrected copy.

Stella checks the Library automatically when an Agent decides the files may help answer the current request. You do not need to select a specific document before chatting.

## Quotas

| Range or quota pool                                                | File limit | Original-file storage |
| ------------------------------------------------------------------ | ---------: | --------------------: |
| Mine · All Agents and all of your Mine · One Agent files, combined |      2,000 |                10 GiB |
| System · All Agents                                                |      4,000 |                20 GiB |
| Each System · One Agent range                                      |      1,000 |                 5 GiB |

The usage shown on the page reflects the whole applicable quota pool. Deleting a file releases its logical quota immediately.

## Find and delete files

Search filters the current range by file name. Long lists are returned in pages; select **Load more** to view the next page.

Deleting a file immediately removes it from retrieval and cannot be undone. If you need a newer version, upload the replacement and delete the old file after the replacement becomes **Ready**.

For first-party API clients, list requests use `page_size`. Pass the previous response's opaque `next_page_token` back as `page_token` to request the next page. Stop when `next_page_token` is `null`; do not inspect or modify token contents.
