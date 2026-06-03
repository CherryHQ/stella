# lark-cli Bitable cheatsheet

Hard-won gotchas for driving `lark-cli base` against a Feishu 多维表格. None of
these are guessable from `--help`; each one cost a real debugging cycle.

## Identity & scopes

- **Run `--as user`, not `--as bot`.** Bot tokens frequently lack base
  read/write scopes; user tokens carry the operator's own grants.
- Error code **`99991672`** = token expired or missing scope (e.g.
  `base:table:read`, `base:field:update`). Fix: have the user re-auth with
  `lark-cli auth login --domain all`, then retry the same call `--as user`.
- The script reads `LARK_IDENTITY` (default `user`) for this.

## Resolving a wiki link to a base token

A user-facing link looks like:

```
https://<tenant>.feishu.cn/wiki/<node_token>?table=<table_id>&view=<view_id>
```

- `table=` query param → `table_id` (use directly).
- The base **app_token** is NOT in the URL. Resolve the wiki node to get it
  (the wiki node wraps a bitable app). Use the wiki/space resolution subcommand
  to turn `<node_token>` into the underlying `app_token`, then pass that as
  `--base-token`.

## Field operations

- **`+field-update` requires `--yes`** to apply (it's a confirm-guarded write).
  Without it the call no-ops.
- `+record-upsert` does **NOT** accept `--yes` or `--format` — passing either
  errors with `unknown flag`. Upsert emits JSON by default.
- When creating a single-select field, define its options up front; reading a
  single-select cell back returns a **list** like `["已接受"]`, not a bare string.

## Reading records (`+record-list`)

- **Pass `--format json`.** The default output is a markdown table that
  `json.loads` cannot parse.
- The JSON is **columnar**, not row-objects:
  - `data.fields` — ordered list of field names (the columns).
  - `data.data` — list of rows; each row is a list of cell values aligned to `fields`.
  - `data.record_id_list` — a **parallel array** to `data.data`; row _i_'s
    record id is `record_id_list[i]`. The rows themselves do **not** embed the id.
  - Reconstruct each record as `dict(zip(fields, row))` and pair with the id.
- Paginate via `--offset` / `--limit` and the `has_more` flag.

## Cell value read/write shapes

Cells are polymorphic. The script's `norm_text()` normalizes on read:

- text / number → scalar (stringify it).
- single-select → `["value"]` — take element 0.
- user field → `[{"id":…, "name":…}]` — take `name` of element 0.
- empty → `None` or `[]` → treat as `""`.

On **write** (`+record-upsert --json`), pass plain scalars: a single-select is
written as its string value, text as a string, datetime as `"YYYY-MM-DD HH:MM:SS"`.

## URL fields auto-linkify

Writing a bare URL into a **text** field comes back on read as Markdown:
`[https://…](https://…)`. Strip it with a `^\[(.*)\]\((.*)\)$` match (the
script's `bare_url()` does this) before parsing the issue number.

## Deletion

`gh issue delete` typically fails with "Viewer not authorized to delete" on
shared repos. **Close** the issue instead and leave an explanatory comment.
