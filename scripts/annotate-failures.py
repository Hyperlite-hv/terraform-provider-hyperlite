"""Turn the failures of `go test -json` into GitHub annotations: the one part of a job's output readable through the
API alone (check-run annotations), which is how a failure is read where the job logs cannot be fetched."""

import json
import sys

out = {}
failed = []
passed = []
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
    elif ev.get("Action") == "pass":
        passed.append(f"{test} ({ev.get('Elapsed', 0):.0f}s)")

for test in failed[:20]:
    text = "".join(out.get(test, []))[-6000:]
    text = text.replace("%", "%25").replace("\r", "").replace("\n", "%0A")
    print(f"::error title={test}::{text}")
# What passed, readable the same way: a green job that ran no test would otherwise look like one that passed them.
print("::notice title=passed::" + "%0A".join(passed or ["no test passed"]))
sys.exit(1 if failed else 0)
