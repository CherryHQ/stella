# Release integration manual checks

Use this runbook only for release scenarios that currently lack a compliant,
stable automation target. Record the release commit, tester, time, environment,
result, and evidence link for every check. A skipped or failed check must have an
explicit release waiver; absence of a result is not a pass.

## X06-S02: Weixin live message

Prerequisites:

- A release-candidate Stella deployment reachable by the approved Weixin client.
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

- A release-candidate Stella deployment with its public callback URL configured.
- A dedicated test tenant for one supported external identity provider.
- A test user that can be deleted or reset after the check.

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
