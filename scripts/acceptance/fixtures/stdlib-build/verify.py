#!/usr/bin/env python3
"""Verify this trusted synthetic binary and write its unsigned integrity record."""

import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys


EXPECTED = b'{"message":"HELLO, ARCENTRA!","length":8}\n'


def toolchain():
    expected = {"GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "GOWORK": "off",
                "GOENV": "off", "CGO_ENABLED": "0"}
    if any(os.environ.get(key) != value for key, value in expected.items()):
        raise RuntimeError("Agent must explicitly set all offline-module Go environment guards")
    # Go reports an empty effective config filename when GOENV=off, rather
    # than echoing the literal sentinel. Check both the process and Go values.
    expected["GOENV"] = ""
    expected.update({key: str(Path.cwd() / ".cache" / name) for key, name in {
        "GOPATH": "go-path", "GOMODCACHE": "go-mod", "GOCACHE": "go-build", "GOTMPDIR": "go-tmp",
    }.items()})
    actual = json.loads(subprocess.check_output(["go", "env", "-json", *expected], timeout=10))
    if actual != expected:
        raise RuntimeError("Agent effective Go settings differ from the offline-module/private-cache contract")
    version = subprocess.check_output(["go", "version"], text=True, timeout=10).strip()
    if not re.fullmatch(r"go version go1\.25\.6 linux/(amd64|arm64)", version):
        raise RuntimeError("expected the preinstalled Linux Go 1.25.6 toolchain")
    print(version)
    print(json.dumps(actual, sort_keys=True))


def main():
    binary = Path("stdlib-demo")
    version = subprocess.check_output(["go", "env", "GOVERSION"], text=True, timeout=10).strip()
    if version != "go1.25.6":
        raise RuntimeError("unexpected build toolchain")
    commit = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True, timeout=10).strip()
    if not re.fullmatch(r"[a-f0-9]{40}|[a-f0-9]{64}", commit):
        raise RuntimeError("checkout has no full Git commit")
    result = subprocess.run([str(binary.resolve()), "arcentra"], capture_output=True, timeout=5, check=True)
    if result.stdout != EXPECTED or result.stderr:
        raise RuntimeError("compiled program behavior differs from the fixture contract")
    invalid = subprocess.run([str(binary.resolve()), "agent1"], capture_output=True, timeout=5, check=False)
    if invalid.returncode != 2 or invalid.stdout or not invalid.stderr:
        raise RuntimeError("compiled program must reject invalid input")
    data = binary.read_bytes()
    manifest = {
        "schemaVersion": 1,
        "sourceCommit": commit,
        "goVersion": version,
        "binary": {"name": binary.name, "sizeBytes": len(data), "sha256": hashlib.sha256(data).hexdigest()},
        "behavior": {"args": ["arcentra"], "stdout": EXPECTED.decode(),
                     "stdoutSha256": hashlib.sha256(EXPECTED).hexdigest()},
    }
    Path("manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    print("ACTUAL_PROJECT_BEHAVIOR " + EXPECTED.decode(), end="")


if __name__ == "__main__":
    if sys.argv[1:] == ["toolchain"]:
        toolchain()
    elif not sys.argv[1:]:
        main()
    else:
        raise SystemExit("usage: python3 verify.py [toolchain]")
