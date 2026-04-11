# Lark CLI Mapping

Use this file to map retired built-in Feishu tools to `lark-cli`.

## Old Tool Mapping

| Old tool | `lark-cli` service | Typical starting point |
| --- | --- | --- |
| `feishu_user` | `contact` | `lark-cli contact --help` |
| `feishu_calendar` | `calendar` | `lark-cli calendar --help` |
| `feishu_task` | `task` | `lark-cli task --help` |
| `feishu_bitable` | `base` | `lark-cli base --help` |
| `feishu_chat` | `im` | `lark-cli im --help` |
| `feishu_im` | `im` | `lark-cli im --help` |
| `feishu_doc` | `docs` | `lark-cli docs --help` |
| `feishu_wiki` | `wiki` | `lark-cli wiki --help` |
| `feishu_sheets` | `sheets` | `lark-cli sheets --help` |
| `feishu_drive` | `drive` | `lark-cli drive --help` |
| `feishu_search` | `drive`, `wiki`, or `api` | inspect help first |

## Reliable Fallbacks

If shortcut commands are unclear:

```bash
lark-cli schema
lark-cli schema <method>
lark-cli api GET /open-apis/...
lark-cli api POST /open-apis/... --params '{}' --body '{}'
```

## Useful Examples

Calendar agenda:

```bash
lark-cli calendar +agenda --as user --format json
```

Send a message:

```bash
lark-cli im +messages-send --as bot --chat-id "oc_xxx" --text "Hello" --format json
```

Create a doc:

```bash
lark-cli docs +create --title "Weekly Report" --markdown "# Progress" --format json
```

Inspect an API schema:

```bash
lark-cli schema im.messages.delete
```
