"""Fail-closed checks for every required hosted test repetition; no runtime access."""

from collections import Counter
from pathlib import Path
import re
import sys


def verify(text, repetitions, expected):
    if repetitions < 1 or not expected or len(set(expected)) != len(expected):
        raise ValueError("Positive repetitions and unique expected test names required")
    active = set()
    passed = Counter()
    for line in text.splitlines():
        if re.search(r"^\s*--- (?:FAIL|SKIP):|^FAIL(?:\s|$)|testing: warning: no tests to run", line):
            raise ValueError("Failed, skipped or absent tests cannot pass acceptance")
        started = re.fullmatch(r"=== RUN\s+(\S+)", line)
        finished = re.fullmatch(r"\s*--- PASS: (\S+) \([0-9.]+s\)", line)
        if started and started[1] in expected:
            name = started[1]
            if name in active or passed[name] >= repetitions:
                raise ValueError(f"Unexpected additional test start: {name}")
            active.add(name)
        if finished and finished[1] in expected:
            name = finished[1]
            if name not in active:
                raise ValueError(f"PASS without matching RUN: {name}")
            active.remove(name)
            passed[name] += 1
    if active or any(passed[name] != repetitions for name in expected):
        raise ValueError(f"Every required test must pass {repetitions} times: {dict(passed)}")
    if text.splitlines().count("PASS") != 1:
        raise ValueError("Exactly one successful Go test-binary summary is required")


if __name__ == "__main__":
    if len(sys.argv) < 4:
        raise SystemExit("usage: verify_containerd_test_log.py LOG REPETITIONS TEST...")
    verify(Path(sys.argv[1]).read_text(), int(sys.argv[2]), sys.argv[3:])
