"""Turn the failures of `go test -json` into GitHub annotations: the one part of a job's output readable through the
API alone (check-run annotations), which is how a failure is read where the job logs cannot be fetched."""

import json
import sys

out = {}
failed = []
for line in open(sys.argv[1], encoding="utf-8", errors="replace"):
    try:
        ev = json.loads(line)
    except ValueError:
        continue
    test = ev.get("Test")
    if not test:
        continue
    if ev.get("Action") == "output":
        out.setdefault(test, []).append(ev.get("Output", ""))
    elif ev.get("Action") == "fail":
        failed.append(test)

for test in failed[:20]:
    text = "".join(out.get(test, []))[-6000:]
    text = text.replace("%", "%25").replace("\r", "").replace("\n", "%0A")
    print(f"::error title={test}::{text}")
sys.exit(1 if failed else 0)
