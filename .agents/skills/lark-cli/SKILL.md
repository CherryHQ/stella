---
name: lark-cli
description: >
  Use the Lark CLI instead of the removed built-in Feishu workspace tools.
  Covers Lark/Feishu calendar, tasks, contacts, chats, messages, docs, wiki,
  drive, sheets, base/bitable, mail, and search workflows through `lark-cli`.
  Trigger when the user asks to use Lark or Feishu workspace data, mentions
  old tool names like `feishu_calendar`, `feishu_task`, `feishu_im`,
  `feishu_doc`, `feishu_wiki`, `feishu_drive`, `feishu_sheets`,
  `feishu_bitable`, `feishu_search`, or wants to set up or use `lark-cli`.
compatibility:
  requires:
    - lark-cli
---

# Lark CLI

Anna no longer ships built-in `feishu_*` workspace tools. Use `lark-cli` through the shell instead.

## Workflow

1. Check availability first:
   ```bash
   command -v lark-cli
   ```
2. If missing, tell the user `lark-cli` is required. If they want setup help, use:
   ```bash
   npm install -g @larksuite/cli
   ```
3. Verify auth before real work:
   ```bash
   lark-cli auth status
   ```
4. If auth is missing, guide setup:
   ```bash
   lark-cli config init --new
   lark-cli auth login --recommend
   ```
5. Prefer structured output for agent work:
   ```bash
   lark-cli <service> <command> --format json
   ```
6. For mutating commands, prefer `--dry-run` first when the command supports it unless the user clearly wants immediate execution.
7. If you do not know the exact subcommand, inspect before guessing:
   ```bash
   lark-cli <service> --help
   lark-cli schema
   lark-cli schema <method>
   ```
8. If the shortcut or API command is unclear, fall back to raw API calls:
   ```bash
   lark-cli api METHOD /open-apis/...
   ```

## Identity

- Use `--as user` for user-scoped operations such as personal calendar, tasks, mail, or docs.
- Use `--as bot` only when the operation is explicitly bot-scoped, typically messaging.

Examples:

```bash
lark-cli calendar +agenda --as user --format json
lark-cli im +messages-send --as bot --chat-id "oc_xxx" --text "Hello"
```

## Command Discovery

Read [references/mapping.md](references/mapping.md) for the old `feishu_*` to `lark-cli` mapping.

When exact flags are unknown, follow this order:

1. `lark-cli <service> --help`
2. `lark-cli schema <method>`
3. `lark-cli api METHOD /open-apis/...`

Do not invent flags or subcommands without checking the local CLI help first.
