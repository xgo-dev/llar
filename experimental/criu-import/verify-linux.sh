#!/usr/bin/env bash
set -euo pipefail

/out/probe </dev/null >>/out/caller.log 2>>/out/sentry.log &
source_pid=$!
status=0
wait "$source_pid" || status=$?
while [[ ! -f /out/restore.complete && ! -f /out/restore.failed && ! -f /out/probe.failed && $status == 0 ]]; do
  sleep 0.1
done
wait
cat /out/caller.log
if (( status != 0 )) || [[ ! -f /out/restore.complete ]]; then
  cat /out/sentry.log >&2
  if [[ -f /out/return-images/restore.log ]]; then tail -70 /out/return-images/restore.log >&2; fi
  exit 1
fi

python3 - <<'CHECK'
import json
import struct
from pathlib import Path
before = json.loads(Path('/out/captured.json').read_text())
after = json.loads(Path('/out/ready.json').read_text())
assert before == after, 'probe initialization ran again'
output = Path('/out/caller.log').read_text()
assert f"pointer={before['pointer']:#x}" in output
assert 'count=42 metadata=built-42' in output
assert 'allocation=8388608 alias=ok kernel=linux epoll=ok' in output
assert Path('/out/native.done').is_file()
log = Path('/out/sentry.log').read_text()
assert f"host_pid={before['pid']}" in log, 'Sentry ran outside the caller process'
with open('/out/gate', 'rb') as f:
    mode, request = struct.unpack('<II', f.read(8))
assert (mode, request) == (5, 1), (mode, request)
CHECK
grep 'exported guest' /out/sentry.log

# Check the original platform constraint separately; this is not the call path.
if /out/importer -images /out/images -state /out/systrap.state -platform systrap 2>/out/systrap.log; then
  echo 'Expected the native snapshot to exceed the systrap address range.' >&2
  exit 1
fi
grep 'cannot import VMA' /out/systrap.log
