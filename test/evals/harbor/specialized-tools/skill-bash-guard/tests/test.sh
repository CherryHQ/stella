#!/bin/sh
set -eu
# The host verdict is the authority. The task container gets no fixture secret,
# credential, or expected answer, only a post-turn attestation copied by host.
v=/tmp/stella-host-verdict.json
test -f "$v"
grep -q '"version": 1' "$v"
grep -q '"task_id": "skill-bash-guard"' "$v"
grep -q '"valid": true' "$v"
reward=$(sed -n 's/.*"reward": \([01]\).*/\1/p' "$v")
test -n "$reward"
printf '%s\n' "$reward" > /logs/verifier/reward.txt
