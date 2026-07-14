---
title: Go patterns
description: Recurring correctness patterns for Stella's Go backend.
---

## Locks and happens-before

**Invariant.** A done/signal channel closes only after the result fields observers
will read have been assigned. The winner path owns both assignment and close;
losers just return.

**How it breaks.** Moving a slow call outside a lock can break the happens-before
contract other paths rely on. In PR #669, `Close()` used to hold the lock across
a 30s container `Stop`, so `closed == true` implied `closeErr` was already
written. After `Stop` moved off-lock, watcher/loser paths could close `done`
before the winner wrote `closeErr`; a concurrent second `Close()` read stale nil,
and `Done()` observers fired while the container was still stopping.

**Fix.** When changing lock granularity, re-audit every reader of the guarded
state. Keep done-channel close in the winner path, after all result fields are
assigned.

**Source.** PR #669.

## Redaction is never gated

**Invariant.** Hiding more is always safe. Redaction sets and deny-lists must be
read unconditionally, not through capability checks that decide what a caller may
expose.

**How it breaks.** In PR #670, bash-output redaction was read through the
vault-capability resolver. That resolver returns nil for group sessions, so a
scoped token was recorded into the redaction set but dropped on the read side,
leaking plaintext into a multi-user group transcript.

**Fix.** Gate only capability methods. In review, trace the record path and read
path separately; redaction paths must not depend on session capability.

**Source.** PR #670.

## No-replace file install

**Invariant.** Restore-on-miss and cache-fill paths must not replace a local file
that appears after the miss check.

**How it breaks.** `stat miss -> fetch -> write temp -> os.Rename(tmp, target)`
has a TOCTOU hole: POSIX rename replaces the target, so a concurrent local write
can be silently overwritten by older remote content.

**Fix.** Install with `os.Link(tmp, target)` and treat `EEXIST` as "concurrent
local write wins"; keep deferred temp removal. Plain temp-plus-rename is only
correct for owned first-party writes where clobbering is intended, such as
`FSStore.Put`.

**Source.** PR #675.
