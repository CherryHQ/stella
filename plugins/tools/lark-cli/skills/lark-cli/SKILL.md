---
name: lark-cli
description: |
  Lark/Feishu workspace operations through the `lark-cli` tool — calendar and meeting
  rooms, docs, sheets, Base (多维表格), wiki, drive, mail, IM messages, tasks, OKR,
  approvals, minutes, video conferences, whiteboards, slides, contacts, attendance, and
  real-time events. Use whenever the user mentions 飞书 / Lark / 我的日程 / 约会议 /
  订会议室 / 飞书文档 / 多维表格 / 知识库 / 云空间 / 发消息 / 群聊 / 邮件 / 待办 /
  OKR / 审批 / 妙记 / 会议纪要 / 画板 / 幻灯片, or asks to look up a colleague, or
  hands you a `feishu.cn` / `larksuite.com` URL. Also covers `lark-cli` setup, auth
  login, and permission-denied errors.
tags:
  - lark
  - feishu
  - workspace
---

# Lark CLI

`lark-cli` carries its own agent documentation, compiled into the binary and versioned
with it. Read it from the CLI; do not work from memory and do not look for docs in this
repo.

## Authentication in Stella

Stella installs this plugin's CLI and injects authentication through the sandbox
environment. Use the supplied `LARKSUITE_CLI_USER_ACCESS_TOKEN`,
`LARKSUITE_CLI_APP_ID`, and `LARKSUITE_CLI_BRAND`; never print their values or write
them into a local config or token store. The plugin configuration selects the env
credentials or Stella-managed OAuth source. Stella owns OAuth token refresh.

These rules override standalone setup instructions in `lark-shared`: do not run
`lark-cli config init` or `lark-cli auth login` inside Stella. For missing or expired
OAuth authorization, use Stella's OAuth tools to list providers and connect the one
whose `required_by` includes Lark CLI; pass the returned authorization link to the
user. For missing user scopes, request only the scopes reported by the API, then
retry after authorization. If the application lacks a scope, show its
`console_url` to the administrator instead. For explicitly configured env
credentials, ask the user to update the plugin's credential configuration.

Use `--as user` for personal resources and employee-attributed actions. Never switch
to `--as bot` to bypass a credential or permission error. Channel credentials and
Stella login are separate from this plugin's authentication.

## Routing

```bash
lark-cli --help                      # the 23 domains, one line each
lark-cli <domain> --help             # +shortcuts (prefer these) and raw API resources
lark-cli skills read lark-<domain>   # the domain guide: concepts, workflows, gotchas
lark-cli skills read lark-<domain> references/<file>.md   # deeper references
lark-cli schema <service>.<resource>.<method>             # params, types, scopes
```

Start with `lark-cli --help` to pick the domain, then read that domain's guide before
the first call. `lark-cli api <METHOD> <path>` is the escape hatch when no typed command
exists.

Read `lark-cli skills read lark-shared` first for auth, `--as user` vs `--as bot`, and
permission-denied handling. Identity does not carry across commands: pass `--as`
explicitly on every call in a workflow.

## Rules

Every command's `--help` labels it `read`, `write`, or `high-risk-write`.
**`high-risk-write` requires `--yes`, and only after the user has confirmed.** Use
`--dry-run` to preview a request without sending it.

`--jq <expr>` filters JSON output. Suppress the `_notice` banner that otherwise pollutes
every JSON response:

```bash
LARKSUITE_CLI_NO_UPDATE_NOTIFIER=1 LARKSUITE_CLI_NO_SKILLS_NOTIFIER=1 lark-cli ...
```
