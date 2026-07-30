# Release integration manual checks

The Tag workflow now builds one immutable candidate, completes all automatic
jobs, uploads an automatic summary, pauses once at the `release-approval`
Environment, records the decisions below, runs the strict final aggregate, and
only then permits Promotion. These checks never rebuild the candidate.

The mentor downloads the candidate archive or image digest identified by the
automatic summary, verifies its commit and checksum, and runs it in a local
Linux/Docker test environment. Use a fresh `STELLA_HOME`, database, and Run ID
for every release.

## GitHub Environment contract

`release-test` owns automatic Live credentials and has no required reviewer.
It is restricted to Release Tags. The tracked non-secret registry is
`test/live/targets.yaml`; print the current resource list with:

```bash
mise run release:live:resources
```

### CherryIN Provider and Embedding resources

X12-S02 and X14-S02 share one secret and use two non-secret variables. The
release owner supplies values; the runner owns these stable names and schemas.

| Name                                | GitHub Environment entry | Meaning                                                                                                 |
| ----------------------------------- | ------------------------ | ------------------------------------------------------------------------------------------------------- |
| `STELLA_LIVE_CHERRYIN_API_KEY`      | Secret                   | Dedicated low-quota CherryIN credential. It must reach every model selected by the two variables below. |
| `STELLA_LIVE_PROVIDER_TARGETS_JSON` | Variable                 | The three Provider protocols, their model IDs, protocol-specific base URLs, and one total X12 timeout.  |
| `STELLA_LIVE_EMBEDDING_TARGET_JSON` | Variable                 | The Embedding endpoint, model ID, optional dimensions, and X14 timeout.                                 |

The following non-secret values are the configuration validated during local
development. The mentor may replace model IDs, but all three Provider `type`
values must remain present:

```json
{
  "timeout_seconds": 420,
  "targets": [
    {
      "id": "anthropic-qwen",
      "type": "anthropic",
      "model": "agent/qwen3.6-plus",
      "base_url": "https://express-ent-admin.cherryin.ai"
    },
    {
      "id": "openai-qwen",
      "type": "openai",
      "model": "agent/qwen3.6-plus",
      "base_url": "https://express-ent-admin.cherryin.ai/v1"
    },
    {
      "id": "responses-gpt",
      "type": "openai-response",
      "model": "openai/gpt-5-mini",
      "base_url": "https://express-ent-admin.cherryin.ai/v1"
    }
  ]
}
```

`id` is a stable result label, `type` selects the Stella Provider
implementation, `model` is the CherryIN model ID, and `base_url` is the root
expected by that Provider SDK. In particular, Anthropic appends `/v1/messages`
itself, while the OpenAI clients append their endpoint below `/v1`.

```json
{
  "base_url": "https://express-ent-admin.cherryin.ai/v1",
  "model": "qwen/qwen3-embedding-0.6b",
  "timeout_seconds": 180
}
```

`dimensions` may be added only when the selected model requires an explicit
dimension. Credentials are never allowed in either JSON variable; the strict
parser rejects unknown credential fields.

`release-approval` is the only Environment with a required reviewer. Configure
the mentor as reviewer and set these non-secret variables for the current
candidate before approving or rerunning the Manual job:

- `STELLA_MANUAL_COMMIT`: exact candidate commit; stale or missing values fail
  closed.
- `STELLA_MANUAL_APPROVER`: reviewer identity recorded in result evidence.
- `STELLA_MANUAL_X06_S02_STATUS` and
  `STELLA_MANUAL_X13_S02_STATUS`: `pass`, `product_failure`,
  `external_blocked`, `not_run`, `manual_pending`, or `waived`.
- Matching `_EVIDENCE`: redacted evidence URL or immutable audit reference.
- Matching `_REASON`: required for every non-Pass outcome.
- Matching `_ORIGINAL_STATUS`: required only for `waived`.
- `STELLA_RELEASE_WAIVERS_JSON`: optional JSON array for eligible automatic
  outcomes. Each item contains `scenario_id`, `original_status`, `reason`, and
  `evidence`.

Example waiver shape:

```json
[
  {
    "scenario_id": "X12-S02",
    "original_status": "external_blocked",
    "reason": "provider status page confirms a release-window outage",
    "evidence": "https://status.example/incidents/example"
  }
]
```

Only `external_blocked`, `not_run`, `flaky`, and `manual_pending` are waivable.
`product_failure` and `missing_result` cannot be waived. Every waiver is bound
to the current Commit and Scenario by the runner. Clear or update per-release
variables after the run so a later candidate cannot inherit stale decisions.

For every Manual check, record the release Tag and commit, candidate checksum or
image digest, tester, time, environment, result, and redacted evidence link.
Promotion requires every Manual Scenario to have a Pass or an allowed waiver.

## X06-S02: Weixin live message

Prerequisites:

- The exact tagged-release Stella artifact, or future release candidate, running
  in the mentor's local Linux/Docker test environment.
- A compliant test identity and a clean Weixin channel registration.
- Access to Stella server logs with secrets redacted.

Procedure:

1. Register the Weixin channel through the QR-code flow and wait for the channel
   to become ready.
2. Send a unique Run-ID message from Weixin to Stella.
3. Confirm Stella receives the message once, starts the expected agent run, and
   streams the complete reply back to the same Weixin conversation.
4. Disconnect the channel and confirm a later message does not start a run.
5. Save redacted screenshots and Run-ID-correlated logs as release evidence.

Pass criteria:

- Registration, inbound delivery, streamed reply, and disconnect all succeed.
- No duplicate message, cross-account delivery, leaked credential, or
  unredacted private content appears.

## X13-S02: External identity provider live login

Prerequisites:

- The exact tagged-release Stella artifact, or future release candidate, running
  in the mentor's local Linux/Docker test environment.
- A dedicated test tenant for one supported external identity provider.
- A test user that can be deleted or reset after the check.
- A registered callback URL. Use a localhost callback when the identity provider
  permits it; otherwise the mentor must provide an approved public HTTPS test
  domain or temporary tunnel before this check can run.

Procedure:

1. Connect the provider with the tenant's approved client configuration.
2. Complete browser login and confirm Stella creates the expected authenticated
   session for the test user.
3. Refresh the session or provider token and confirm access remains valid.
4. Disconnect the provider, then confirm the old session or credential no longer
   grants provider-backed access.
5. Save redacted screenshots and Run-ID-correlated logs as release evidence.

Pass criteria:

- Connection, callback, login, refresh, and disconnect all succeed.
- Invalid state, stale credentials, or a disconnected account cannot be reused.
- No client secret, token, authorization code, or private user data is exposed.
