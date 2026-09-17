"""PID 1 reaper for the native CRIU continuation in a fresh PID namespace."""
import os
from pathlib import Path

os.setsid()
criu = os.fork()
if criu == 0:
    os.execvp("criu", ["criu", "restore", "-D", "/out/return-images",
                       "--shell-job", "--restore-detached",
                       "--pidfile", "/out/native.pid", "-v4", "-o", "restore.log"])

_, status = os.waitpid(criu, 0)
if not os.WIFEXITED(status) or os.WEXITSTATUS(status) != 0:
    raise SystemExit(1)
native = int(Path("/out/native.pid").read_text())
result = 1
while True:
    try:
        pid, status = os.waitpid(-1, 0)
    except ChildProcessError:
        break
    if pid == native and os.WIFEXITED(status):
        result = os.WEXITSTATUS(status)
raise SystemExit(result)
