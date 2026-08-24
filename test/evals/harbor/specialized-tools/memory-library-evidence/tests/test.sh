#!/bin/sh
set -eu
d=/root/.stella-harbor-verdict
openssl pkeyutl -verify -pubin -inkey "$d/public.pem" -rawin -in "$d/payload.json" -sigfile "$d/signature.bin"
grep -q '"task_id":"memory-library-evidence"' "$d/payload.json"
grep -q '"valid":true' "$d/payload.json"
reward=$(sed -n 's/.*"reward":\([01]\).*/\1/p' "$d/payload.json")
test -n "$reward"
printf '%s\n' "$reward" > /logs/verifier/reward.txt
