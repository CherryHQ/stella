---
title: Agent tool rules
description: How to add, change, rename, or remove a model-facing tool in Stella.
---

> This is a **rule file** for contributors. When you add, change, rename, or
> remove any tool a model can call, read this page first and follow it.
> [`api-design.md`](./api-design) owns the HTTP contract a tool usually sits on,
> [`go-patterns.md`](./go-patterns) owns concurrency and redaction, and
> [`doc-style.md`](./doc-style) owns the docs. This page owns the tool surface.

A tool is the only thing in Stella a model can invoke directly. Its name, its
schema and its description are a public API for the model: rename one and every
transcript, delegate preset and `tool_override` row that mentions it stops
matching. Treat a tool change with the same care as a breaking HTTP change.

Every section ends with **Verified by**: the test or command that stops a
violation before review does.

## 1. Do you need a tool at all?

Work down this list and stop at the first rung that holds.

1. **The model can already do it with `bash` and an existing CLI.** Write a
   skill, not a tool. The `tap-web` and `xberg` skills are exactly this: prose
   plus a command line, no Go code, no schema, no registration.
2. **The capability needs the user's identity, a database write, an outbound
   side effect, or server-side validation the model must not be able to skip.**
   That is a tool.

One tool does one operation. **A tool with an `action` parameter that switches
between operations is not allowed.** Providers reject `oneOf`/`const` at the top
level of a function schema, so a union tool has to hoist every action's fields
into one flat object: the provider cannot validate the call, and the lenient
decoder drops the fields that do not belong to the chosen action without saying
so. The model then gets a successful-looking result from a call it did not make.

**Verified by:** review. `toolgen` validation rejects an `action` property on a
split tool (`TestValidateRejectsBadDeclarations`), but only a reader catches a
capability that should have been a skill.

## 2. Where the tool is declared

**A tool that has an HTTP operation is declared on that operation.** Add an
`x-agent-tool` annotation in `api/spec/domain/<domain>/paths.yaml`. If the
capability has no endpoint yet, add one first, following
[`api/CLAUDE.md`](https://github.com/CherryHQ/stella/blob/main/api/CLAUDE.md) and
[`api-design.md`](./api-design). One contract, one schema, one place to change it.

**A tool that genuinely has no HTTP operation is declared in
`api/spec/agent-tools/<domain>.yaml`.** Sending a message into another session
or loading a skill into the sandbox are not REST resources, and inventing an
endpoint to hang a tool off would be a lie about the contract. These tools carry
their own schema and still go through `toolgen`, so they get the same name
checks, the same sealed schema and the same generated input types:

```yaml
family: session
package: agent/session/access # output directory under internal/
tools:
  - action: list
    description: List this agent's recent sessions for the current user.
    input:
      type: object
      properties:
        include_archived: { type: boolean, default: false, description: Include archived sessions. }
  - action: send
    description: Continue one of this agent's sessions and wait for its reply.
    input:
      type: object
      properties:
        session_id: { type: string, description: The session to continue. }
        message: { type: string, description: The session's next request. }
    required: [message, session_id]
```

That declaration generates `session_list` and `session_send`. Add
`resource: message` to the second tool and the name becomes
`session_message_send` instead — a resource is a real name segment, not a
comment.

`input` is either an inline schema or a `$ref` into the assembled OpenAPI
components; only `#/components/schemas/...` refs resolve here, at any depth. A
`$ref` brings the whole schema, so `required` may only name properties that
schema actually has — requiring a field the input does not declare makes a
schema no argument object can satisfy, and `validate` rejects it. `package` is
the directory under `internal/` that receives `tool_gen.go`; its last segment is
the Go package name. `batch: <field>` wraps the input in an array property,
exactly as the annotation modifier does.

Declared tools get `<Family><Action>Input` types (`SessionSendInput`), not
`<Action>Input`, because they land in a package that already has hand-written
code: `internal/agent/session/access` has its own `SendInput`, and a bare name
would not compile.

**Hand-written tools are a closed list**: `bash`, `view_image` (core sandbox),
`webfetch` (plugin), `notify` (channel dispatcher), `goal_control` (attempt
protocol), `code` (meta-tool), `library_*`, and `mcp__*`. Adding to it means
claiming the tool has neither an HTTP operation nor a schema that could be
declared. Change the list in `internal/agent/toolmeta` and say why in the PR.

`memory`, `skills` and `session` are hand-written too, but for a different
reason: they are unions whose split has not landed yet. They sit in a separate
`pendingSplit` map, and each one leaves it in the PR that converts it — the map
is meant to reach empty, unlike the list above.

**Verified by:** `TestGeneratedFixtureIsCurrent` and
`TestValidateRejectsUnsatisfiableRequired` (`internal/cmd/toolgen`) — the first
renders `test/toolgenfixture/agent-tools/session.yaml` through the real pipeline
into Go that `go build ./...` compiles next to a colliding hand-written
`SendInput`; `TestEveryBuiltinIsGeneratedOrAnAcceptedException` and
`TestExceptionListsAreExactlyWhatTheRuleDocuments`
(`internal/agent/toolmeta`), which check every fixed builtin against the two
lists above; `mise run generate:api:check`.

## 3. `x-agent-tool` reference

One annotation, or a list when one operation backs several tool actions:

```yaml
x-agent-tool:
  - { tool: "scheduler", resource: "job", action: "update" }
  - { tool: "scheduler", resource: "job", action: "pause", fixed: { enabled: false }, body: false }
```

| Key             | Effect                                                                                         |
| --------------- | ---------------------------------------------------------------------------------------------- |
| `tool`          | The family. Required. Must have an entry in `domainPackages` (`internal/cmd/toolgen/main.go`). |
| `resource`      | The sub-resource this action acts on. Shapes the tool name and the `id` parameter (§4).        |
| `action`        | The dispatch key. Required. Becomes the generated `Handler` method and the `Dispatch` case.    |
| `name_override` | An explicit tool name, for the one grammar exception in §4. Otherwise do not use it.           |
| `description`   | Overrides the operation summary as the declared description.                                   |
| `fixed`         | Service-owned constants. The property is deleted from the model input.                         |
| `restrict`      | Narrows a property's enum without touching the HTTP API. The first value becomes the default.  |
| `require`       | Marks an optional HTTP field required in the tool schema only.                                 |
| `optional`      | Marks a required HTTP field optional in the tool schema only.                                  |
| `add`           | Tool-only properties. On a batch action they belong to each item.                              |
| `omit`          | Drops a property from the tool input, leaving the HTTP contract alone.                         |
| `rename`        | Renames a property (and its `required` entry) in the tool input.                               |
| `body`          | `false` takes path and query parameters only and ignores the request body.                     |
| `batch`         | Wraps the request body in an array property of that name, `minItems 1`, `maxItems 20`.         |

Modifiers apply in a fixed order: build the input (params, plus the body unless
`body: false`) → `add` → `fixed` → `omit` → `rename` → `restrict` → `require` /
`optional`. So `restrict` and `require` name the property as the model sees it,
after any rename.

Traps worth knowing:

- **Identity fields are always stripped.** `agent_id`, `user_id` and their camel
  spellings never reach the model. Identity comes from the request context.
- **`fixed` runs before `restrict`.** Restricting a property you also fixed does
  nothing, because the property is already gone.
- **`restrict`, `require` and `optional` apply to the tool input, not to batch
  items.** On a batch action the tool input is the wrapper; use `add` for
  item-level properties.
- **An unknown or mistyped modifier fails the build**, it is not ignored.
- **Do not describe other actions in a property description.** `"only
entry_update takes this"` is union-era text that a split tool inherits and
  that its own exact schema already answers.

**Verified by:** `internal/cmd/toolgen/main_test.go` (one test per modifier);
`TestParseActionSpecsRejectsUnknownModifier`.

## 4. Naming

**Tool names are `<domain>_<resource>_<action>`**, with the resource dropped when
it repeats the domain: `recally_feed_add`, `scheduler_job_pause`, `goal_create`.
Verbs are small and boring — `create`, `get`, `list`, `update`, `delete`,
`search`, `add`, `remove`, `save`, `send`. Resources are singular and match the
HTTP resource name. No bare nouns, no plurals, no verb-object inversion.

The one exception: when the thing being created is the domain's own resource and
the object is only its source, `<domain>_<action>_<object>` reads better —
`share_create_artifact` creates a _share_ from an artifact, not an artifact. Use
`name_override` for that case and only that case.

**Parameter names**, eight rules:

1. Always `snake_case`. A path parameter naming the tool's own resource
   (`{jobId}` on a job tool) becomes `id`; any other `{xId}` becomes `x_id`.
   This is a rule in `toolFieldName`, not a per-name table — a new domain needs
   no entry.
2. The resource's own key is `id`; a foreign key is `<resource>_id`
   (`goal_id`, `article_id`, `feed_id`, `session_id`). Never let one schema carry
   two identifiers of different things with only one of them called `id`.
3. Pagination is `page_size` + `page_token`, returning `next_page_token`. `limit`
   is only for an unpaginated "return at most N", and its schema `maximum` must
   equal the handler's own cap.
4. Free-text search is `q`. Times are RFC3339 strings named `since` / `before` /
   `at`. Booleans are affirmative (`enabled`, `unread`), never `disabled` or
   `no_*`.
5. The idempotency key is `idempotency_key`, and every tool with an outbound
   side effect should require one.
6. A batch is `<resource>s: [...]`, `minItems 1`, `maxItems 20`.
7. Identity (`agent_id`, `user_id`) never appears in a tool parameter.
8. Descriptions follow §6.

**Verified by:** `TestToolFieldNameMapsIdentifiersByResource`;
`TestValidateRejectsBadDeclarations` (provider-legal names, snake_case
properties).

## 5. Schema rules

A split tool's schema is a contract, not a hint. Providers validate against it
before the call, and `DecodeInputStrict` rejects anything it does not declare.

- **Exact and sealed.** Only this tool's own properties and its own `required`.
  No `action` property; the name carries the action. `toolgen` adds
  `additionalProperties: false` to the tool input and to batch items. A property
  that is genuinely a free-form map declares its own `additionalProperties` and
  is left alone.
- **The top level stays a plain object.** No `oneOf`, `anyOf`, `allOf`, `enum`,
  `const` or `not` at the top level — OpenAI-compatible providers reject them.
- **Every property has a one-sentence description.** Constrained values use
  `restrict`, not prose.
- **Numeric bounds match the handler.** A schema `maximum` the handler then
  clamps teaches the model something false.
- **Large bodies travel as a sandbox path, not as an inline string.** Follow the
  `content_path` precedent in `recally_article_save`: 1 MB per file, 4 MB per
  call, and the tool reports what it actually stored.
- **Output has a stated cap** and says so in the result (`truncated`, `note`)
  when it hits it. Timestamps are RFC3339. Secret values are never returned —
  the vault tools return metadata only.

**Verified by:** `TestBatchAnnotationWrapsRequestBody`,
`TestActionSchemaKeepsDeclaredAdditionalProperties`,
`TestToolSchemaIsPlainObjectWithActionEnum`;
`TestValidateRejectsBadDeclarations`.

## 6. Descriptions

- **60 words or fewer.**
- **First sentence: what the call does.** Second: the side effect or the
  precondition — "never fetches the URL itself", "sends mail; requires
  `idempotency_key`".
- **Name other tools by their real names** when the flow needs several calls
  ("then call `oauth_flow_status`").
- **Do not restate the schema.** Field-level prose belongs on the field.
- **Do not disambiguate against sibling actions.** An exact schema already did.

Operation-backed tools keep their model-facing prose in the hand-written adapter
next to the handler (`actionDescriptions` in `internal/recally/tool.go`), because
an endpoint summary is written for API readers. Declared tools carry it in the
declaration file.

**Verified by:** `TestValidateRejectsBadDeclarations` (a tool with no
description fails the build); word count is on review.

## 7. Go implementation shape

Add the family to `domainPackages` in `internal/cmd/toolgen/main.go`, run
`mise run generate:api`, then write the adapter in `tool.go`:

- `Tool{spec, svc}`, built by `NewTool` — or `NewRuntimeTool` when the tool needs
  the sandbox session.
- `Definition()` returns `spec.Definition(description)`.
- `Execute` is five steps: nil-service guard → `authz.ToolIdentity(ctx, name)` →
  `ToAuthority()` → `Dispatch(ctx, handler, spec.Action, args)` →
  `authz.MapError` + marshal.
- Handler methods stay thin. **Identity comes from the context, never from an
  argument.** Per-action authorization belongs in the `Access` layer, not in the
  handler.
- Validate everything before the first write. Errors are actionable and name
  real tool names. A missing record is an empty list from a `list` tool and a
  not-found from a `get` tool.
- A tool with an outbound side effect is idempotent: deduplicate on
  `idempotency_key` and report the duplicate rather than sending twice.

**Verified by:** `go build ./...`; the domain's own handler tests;
`internal/authz/tool_authz_test.go`.

## 8. Registration and visibility

Tools are registered in `cmd/stellad/commands.go`. A split family registers one
`agent.BuiltinTool` per entry in its generated `ActionTools()`, so adding an
action needs no registration edit.

- **`Available` gates visibility, and its errors are fatal, not silent.** The
  baseline is `agent.BuiltinToolAvailable` (a user and an agent are present).
  When the check itself errors, the error propagates: registry and runner
  construction abort, `GET /api/agents/{id}/tools` returns 5xx, and no partial
  subset is cached. A tool set that quietly lost a tool is worse than a request
  that failed, because the model reasons about the gap as if it were the truth.
- **Core names are reserved.** A builtin or plugin may not take a core tool's
  name, and `mcp__` is reserved for MCP.
- **The Code Mode hot set is small on purpose.** `codeHotToolNames` in
  `pkg/agent/code_strategy.go` lists the tools worth putting in front of the
  model every turn instead of behind `tools.search`. Adding one means editing
  that map, the system prompt, and the docs that quote the set.

**Verified by:** the runtime registry and catalog availability tests added in
PR-1 ([#1175](https://github.com/CherryHQ/stella/pull/1175)); `cmd/stellad` and
`internal/agent` registration tests; `pkg/tools` registry tests (duplicate
names).

## 9. Consumers to update

Tool names are strings in places no compiler checks. When you add, rename or
remove one, walk this list:

- `resources/skills/system/<domain>/SKILL.md` — examples must use real names and
  real fields.
- `resources/skills/system/stella/SKILL.md` — the tool inventory.
- `internal/agent/prompt/template/system_prompt.tmpl`.
- Scheduler built-in job templates, which name tools in their prompts.
- `web/content/docs/development/architecture.md` (EN + ZH) tool tables.
- `resources/builtin_manifest_gen.go` — regenerate.
- The Web UI's tool metadata, where a name drives an icon or a label.
- The release note, whenever a name changes or disappears.

**Verified by:** `resources/recally_skill_test.go` and
`internal/scheduler/builtin_schema_test.go` (skill and template examples are
checked against the live schema); `mise run generate:check`.

## 10. Renaming, splitting, deleting

A rename is a delete plus a create. There is no alias period, so the migration
carries the compatibility instead — expand, then contract:

1. **`tool_override` gets a migration in the same release**, mapping every old
   name to its new names. Fan a union name out to each action name, and map a
   renamed exact name across. Merge with **deny-wins**
   (`enabled = existing AND incoming`): a capability the user switched off must
   never come back on because its row stopped matching. **Keep the old rows.**
2. **The next release deletes the old rows.** A deployment that upgrades with
   downtime may collapse both steps.
3. **The `down` migration folds the action rows back** with `bool_and(enabled)`
   per `(scope, user_id, agent_id)`, ANDed with any surviving old row.
4. **`toolmeta`'s `legacyNames` map carries the old names for exactly one
   deprecation release**, so a delegate preset's `tools:` list or an
   `excluded_tools` entry written against the previous release keeps selecting
   the same capability. Remove the entries when the contract migration ships.
5. **The release note lists every old name against its new name** and gives a
   grep for custom skills and presets:

   ```bash
   grep -rn 'recally_digest\|scheduler_pause' ~/.stella/skills ~/.stella/delegates
   ```

A custom skill that calls a removed name by hand still breaks. That is a
declared breaking change, not something the migration can fix.

**Verified by:** the migration's own test (fan-out, deny-wins, and the `down`
fold); `TestLegacyNamesRedirectSelectors` (`internal/agent/toolmeta`).

## 11. Testing

Every tool needs, at minimum:

- **An authorization case** in `internal/authz/tool_authz_test.go`: one call that
  must be refused, and proof that the refusal leaks nothing about what exists.
- **A handler test** for whatever the handler does beyond dispatch — mutually
  exclusive fields, path expansion, caps, projection.
- **Schema and skill guards.** `internal/scheduler/builtin_schema_test.go` and
  `resources/recally_skill_test.go` check documented examples against the live
  schema; extend them rather than writing a parallel one.
- **`mise run generate:api:check` clean.** It regenerates through the Redocly
  bundler (`vp dlx`), so it needs the node toolchain; it checks untracked files
  too, because a new family's first `tool_gen.go` is untracked, not modified.
- **A catalog assertion** that the tool appears in `GET /api/agents/{id}/tools`
  with an exact schema and no `action` property.

Reach for a system test only when the seam is cross-process — the `goal_control`
attempt protocol is the example. See [`system-test.md`](./system-test).

**Harbor does not cover builtin tools.** No PR may cite a Harbor score as
evidence for a tool change; say so explicitly instead of letting a score imply
coverage.

**Verified by:** `mise run test`; `mise run generate:api:check`.

## 12. Code Mode notes

- **Tool names become JavaScript identifiers verbatim** in the generated
  directory, so a name that is awkward here is awkward there.
- **`tools.search` matches on the family prefix**, which is why the
  `<domain>_<resource>_<action>` grammar matters more in Code Mode than in native
  mode: a consistent prefix is what makes a family discoverable in one search.
- **Do not round-trip large results through `code`.** Use the `content_path`
  pattern so the payload never enters the model's context.
- **Hot versus cold is visibility, not authority.** A tool called from inside
  `code` inherits the same identity and the same authorization as a direct call.

**Verified by:** `pkg/agent` code strategy tests.

## 13. PR checklist

Paste this into the PR's Test section and answer every line.

- [ ] The capability needs a tool, not a skill (§1).
- [ ] Declared on an HTTP operation, or in `api/spec/agent-tools/` with a stated
      reason for having no endpoint (§2).
- [ ] Modifiers used as documented; no reliance on side effects of `fixed` (§3).
- [ ] Name follows `<domain>_<resource>_<action>`; parameters follow the eight
      rules (§4).
- [ ] Schema is exact, sealed, and free of an `action` property; bounds match the
      handler (§5).
- [ ] Description is under 60 words and states the side effect (§6).
- [ ] Handler takes identity from the context; validation precedes any write;
      outbound effects are idempotent (§7).
- [ ] `Available` fails closed (§8).
- [ ] Every consumer in §9 updated, including both language versions of the docs.
- [ ] Renames carry the override migration, the `legacyNames` entry, and the
      release-note table (§10).
- [ ] Authorization case, handler test, and guards added; `generate:api:check`
      clean (§11).
- [ ] No Harbor score cited as evidence for the tool change (§11).
