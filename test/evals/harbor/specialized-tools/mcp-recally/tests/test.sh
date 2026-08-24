#!/bin/sh
set -eu
test -f /tmp/stella-host-verdict.json
python3 - <<'PY'
import json
v=json.load(open('/tmp/stella-host-verdict.json'))
assert v['version'] == 1 and v['task_id'] == 'mcp-recally' and v['valid'] is True and v['reward'] in (0, 1)
raise SystemExit(0 if v['reward'] == 1 else 1)
PY
