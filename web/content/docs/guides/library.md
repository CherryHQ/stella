---
title: Library
---

## What the Library is for

The Library gives agents durable reference material for answers that depend on company policies, role-specific procedures, or your own working documents. Stella searches it when the current conversation needs evidence and includes the source file in the answer.

Library files are different from chat attachments. Attaching a file to one conversation does not add it to the Library. A file becomes searchable only when someone explicitly uploads it from a Library page.

The first version accepts UTF-8 plain text and Markdown files up to 25 MiB each. Files from cloud drives and other connected sources are not yet synchronized.

## Where files apply

Every file belongs to one of four ranges. The ranges are added together during a search; they are not priority levels.

| Range               | Who manages it   | When it is available                              |
| ------------------- | ---------------- | ------------------------------------------------- |
| Mine · All Agents   | The current user | Whenever that user works with any agent           |
| Mine · One Agent    | The current user | Only when that user works with the selected agent |
| System · All Agents | An administrator | For every user and agent                          |
| System · One Agent  | An administrator | For every user working with the selected agent    |

Open **Settings → Library** to manage files for Mine · All Agents. Administrators manage the two System ranges from **Admin Console → Deployment resources → Global Library**. Open an agent's **Library** page to manage your Mine · One Agent files for that agent.

## Uploading and processing

After an upload, a file moves through these states:

- **Processing** — Stella is parsing and indexing the file. It is not searchable yet.
- **Ready** — the published file chunks can be searched.
- **Failed** — parsing did not finish; the page shows the failure.

Refresh the browser to see a newer state. Uploading the same content again creates another Library file; it does not silently replace an existing one.

Deleting a file removes it from search immediately. Stella cleans up its stored source and derived chunks in the background.

## How agents use it

You can ask a natural question; you do not need to choose a file or write a search command. The agent decides whether the question needs Library evidence, writes a search phrase from the conversation, and retrieves the best matching passages.

Each search covers exactly the four ranges available to the current user and agent. Stella filters permissions, publication state, and deletion state in the same database query that ranks matches. It returns complete matching passages rather than the original file.

When an answer uses a Library passage, check its file-name citation and page or heading when available. Source documents are treated as reference material, not as instructions that can override the conversation or Stella's safety rules.

Library search is not enabled in group conversations. Signed-in one-to-one agent runs can use the Library from the Web UI, supported private channels, webhooks, scheduled work, tasks, workflows, and delegated work when the run retains a trusted user and agent identity.

## Library and memory

Use the Library for documents you intentionally maintain and want searched as reference material. Memory captures ongoing context about a person and past conversations. A fact appearing in a chat does not automatically become a Library file, and uploading a Library file does not rewrite the agent's memory.
