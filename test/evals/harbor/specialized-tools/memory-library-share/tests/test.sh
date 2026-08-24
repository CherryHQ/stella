#!/bin/sh
set -eu
v=/tmp/stella-host-verdict.json
test -f "$v"
grep -q '"version": 1' "$v"
grep -q '"task_id": "memory-library-share"' "$v"
grep -q '"valid": true' "$v"
reward=$(sed -n 's/.*"reward": \([01]\).*/\1/p' "$v")
test -n "$reward"
printf '%s\n' "$reward" > /logs/verifier/reward.txt
