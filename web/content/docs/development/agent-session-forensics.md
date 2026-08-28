---
title: Agent session forensics
---

> This page is for developers investigating an agent turn that was slow, costly,
> incorrect, or needlessly tool-heavy. It explains how to derive a hypothesis from
> one persisted session, not how to benchmark a change.

A Stella session leaves two complementary traces in PostgreSQL:

- `ctx_conversation` and `ctx_message` record the user-visible transcript,
  including persisted tool calls and tool results.
- `agent_llm_call` records provider-reported token usage, latency, model, stop
  reason, and error for each model call.

Together they answer _what the agent did_, _what it showed the model_, and
_where it spent turns_. They do not prove that a proposed change improves agent
behavior. Turn a diagnosis into a deterministic replay or a matched Harbor
comparison before making that claim.

## Safety first

A session transcript can contain user content, URLs, tool output, credentials
that a tool failed to redact, and private business data. Treat it as production
customer data.

- Query only a deployment and user scope you are authorized to inspect.
- Use a read-only database role. Never run `UPDATE`, `DELETE`, migrations, or
  ad-hoc cleanup while investigating.
- Keep raw transcript output on the secured machine. Redact excerpts before
  pasting them into an issue, PR, log, or model prompt.
- Do not enable `OTEL_STELLA_RECORD_TOOL_IO` to recreate a session during a
  Harbor run. Terminal-Bench tasks contain synthetic secrets.

Have your deployment's secret manager or secured terminal environment provide
`STELLA_DATABASE_URL`; do not type a connection string containing a password into
shell history. Then connect with:

```sh
psql "$STELLA_DATABASE_URL"
```

The following examples use a session ID as a SQL parameter. In `psql`, set it
once rather than interpolating it into a query:

```sql
\set session_id '00000000-0000-0000-0000-000000000000'
```

## Establish the session boundary

Start from `ctx_conversation`. A session ID is a text identifier, while the
conversation row's UUID is the foreign key used by `ctx_message`.

```sql
SELECT
  id,
  session_id,
  title,
  channel,
  kind,
  agent_id,
  user_id,
  created_at,
  last_active,
  archived
FROM ctx_conversation
WHERE session_id = :'session_id';
```

Confirm the `agent_id`, `user_id`, channel, and time range before reading
content. A session can be a private chat, group chat, scheduler run, delegate,
or task; the expected tool surface and turn policy differ by kind.

## Reconstruct the tool timeline

Read messages in `seq` order. `tool_call` and `tool_result` rows store their
payload as JSON text in `content`; the call ID in that JSON links the pair.
Start with a redacted, bounded excerpt rather than dumping an entire transcript.

```sql
SELECT
  m.seq,
  m.created_at,
  m.role,
  m.event_type,
  m.token_count,
  left(m.content, 1000) AS redacted_locally_before_sharing
FROM ctx_message AS m
JOIN ctx_conversation AS c ON c.id = m.conversation_id
WHERE c.session_id = :'session_id'
ORDER BY m.seq;
```

Classify the sequence before proposing a fix:

| Evidence in the transcript                                    | Likely problem                                                | First change to consider                                                                       |
| ------------------------------------------------------------- | ------------------------------------------------------------- | ---------------------------------------------------------------------------------------------- |
| A simple request loads several references before acting       | The happy path is hidden behind optional instructions         | Put the short default path in `SKILL.md`; load the reference only for explicit advanced intent |
| A failed command is followed by a near-identical retry        | A missing dependency or unsuitable fallback caused a dead end | Remove the fragile dependency or make the next fallback materially different                   |
| The same article or document appears in multiple tool results | Large intermediate content is returning to the model          | Keep it in a sandbox file and pass a path or reference between tools                           |
| A tool call fails validation, then succeeds after a retry     | Prompt guidance drifted from the generated tool schema        | Add the exact request shape and a contract test that reads the schema                          |
| Metadata is fetched again after a successful content fetch    | The agent discarded usable intermediate data                  | Emit a compact sidecar result and reuse it in the next call                                    |

A transcript is evidence of one execution, not a universal failure mode. Confirm
that the behavior is reachable from the current skill, prompt, tool schema, and
sandbox implementation before changing them.

## Account for model calls and cost

`agent_llm_call` has `session_id`, but does not have a foreign key to an
individual transcript message. Align it with the transcript by `occurred_at`;
do not assume one message or one tool call always maps to one model call.

```sql
SELECT
  occurred_at,
  provider,
  model,
  input_tokens,
  output_tokens,
  cache_read_tokens,
  cache_write_tokens,
  duration_ms,
  time_to_first_token_ms,
  stop_reason,
  error
FROM agent_llm_call
WHERE session_id = :'session_id'
ORDER BY occurred_at;
```

For a compact baseline, aggregate the save or task window you are investigating:

```sql
SELECT
  count(*) AS model_calls,
  sum(input_tokens) AS input_tokens,
  sum(output_tokens) AS output_tokens,
  sum(cache_read_tokens) AS cache_read_tokens,
  sum(duration_ms) AS model_duration_ms,
  count(*) FILTER (WHERE error <> '') AS errored_calls
FROM agent_llm_call
WHERE session_id = :'session_id';
```

Wall-clock duration is useful for locating a stall, but it is not comparable
across hosts, models, cache state, or network conditions. Prefer counts of model
turns, tool calls, tool errors, and provider-reported tokens when forming a
hypothesis.

## Turn evidence into a safe improvement

Use this loop after the forensic pass:

1. **State the observed waste precisely.** For example: “one bare URL save
   loaded two references, ran three fetches, and spent eight model turns.” Keep
   the raw article and identifiers out of the statement.
2. **Name the invariant.** For the example: the saved body must not enter model
   context; a new Recally save must use the `articles` batch; metadata must be
   typed and bounded.
3. **Stop at the smallest layer that can enforce it.** Prefer an existing skill
   instruction, generated tool contract, sandbox file transfer, or unit test.
   Do not add a server-side fetch endpoint when the sandbox already owns web
   extraction and its egress policy.
4. **Add a deterministic guard.** A prompt recipe that is executable in the
   sandbox needs a test that executes it with a stubbed dependency. A documented
   tool request needs a test that checks its fields against the generated input
   schema.
5. **Replay the narrow behavior.** Use a fixture, a stubbed upstream response,
   and a fresh session. Verify the saved data and that bodies or secrets do not
   enter tool arguments, Code source, or persisted model history.
6. **Measure agent behavior at the right layer.** Session forensics explains
   why to change something. Harbor supplies matched before/after evidence only
   when its task and enabled tool surface actually exercise the behavior. The
   current trusted Harbor loop is deliberately bash-only, so it cannot validate
   a Skill/Tap/Recally flow; build a deterministic specialized-tool replay
   first instead of claiming an unrelated Harbor score.

## A review-ready summary

A useful investigation summary separates facts from interpretation:

```text
Observed session window: <UTC start> to <UTC end>
Outcome: <completed / failed / partial>
Evidence: <turns, tool calls, failed calls, provider tokens>
Waste: <specific repeated work or invalid call>
Root cause: <verified prompt, schema, or sandbox path>
Change: <smallest chosen layer and invariant>
Verification: <fixture/unit/system test>
Measurement boundary: <what Harbor or replay covers, and what it does not>
```

This makes a session investigation reproducible without exporting private
transcripts, and prevents a persuasive anecdote from becoming an unsupported
performance claim.
