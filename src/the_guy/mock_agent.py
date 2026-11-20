"""Deterministic mock coding agent for tests and `guy demo`.

Behavior is driven entirely by the GUY_MOCK_SPEC environment variable (JSON):

    {"write": [{"path": "x.txt", "content": "hi"}],
     "stdout": "optional text to print",
     "sleep": 1.5,
     "exit": 0,
     "leak_secret": true,
     "spawn_child": {"seconds": 60}}

It always writes the received prompt to mock-prompt-received.txt in cwd so
tests can verify prompt transport (spaces, unicode, quotes).
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import time
from pathlib import Path


def main(argv=None) -> int:
    argv = list(sys.argv[1:] if argv is None else argv)
    if "--help" in argv:
        print("the-guy mock agent: deterministic, no model calls")
        return 0
    prompt = ""
    if "--prompt" in argv:
        idx = argv.index("--prompt")
        prompt = " ".join(argv[idx + 1:])

    spec = {}
    raw_spec = os.environ.get("GUY_MOCK_SPEC")
    if raw_spec:
        try:
            spec = json.loads(raw_spec)
        except ValueError:
            print("[mock] invalid GUY_MOCK_SPEC; ignoring", file=sys.stderr)

    cwd = Path.cwd()
    (cwd / "mock-prompt-received.txt").write_text(prompt, encoding="utf-8")

    for entry in spec.get("write", []) or []:
        target = cwd / entry["path"]
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(entry.get("content", ""), encoding="utf-8")
        print(f"[mock] wrote {entry['path']}")

    if spec.get("stdout"):
        print(spec["stdout"])

    if spec.get("leak_secret"):
        print("api_key=AKIAIOSFODNN7EXAMPLE")
        print("Authorization: Bearer abcdefghijklmnop.qrstuvwxyz")
        print("connection string: postgres://admin:SuperSecret123@db.example.com/prod")

    child_spec = spec.get("spawn_child")
    if isinstance(child_spec, dict):
        seconds = int(child_spec.get("seconds", 60))
        child = subprocess.Popen(
            [sys.executable, "-c", f"import time; time.sleep({seconds})"]
        )
        (cwd / "mock-child.pid").write_text(str(child.pid), encoding="utf-8")
        print(f"[mock] spawned child pid {child.pid}")

    if spec.get("sleep"):
        time.sleep(float(spec["sleep"]))

    return int(spec.get("exit", 0))


if __name__ == "__main__":
    sys.exit(main())
