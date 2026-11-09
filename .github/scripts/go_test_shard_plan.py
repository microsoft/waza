#!/usr/bin/env python3
"""Balance the discovered inventory; timing history never controls admission."""

import math
import statistics
import sys


def select(inventory, history, shard):
    durations = {}
    for line in history:
        if not line.strip() or line.startswith("#"):
            continue
        name, raw = line.split()
        seconds = float(raw)
        if name in durations or not math.isfinite(seconds) or seconds < 0:
            raise ValueError(f"invalid or duplicate timing for {name}")
        durations[name] = seconds
    names = [line.strip() for line in inventory if line.strip()]
    if not names or len(set(names)) != len(names):
        raise ValueError("inventory must be nonempty and unique")
    default = statistics.median(durations.values()) if durations else 1.0
    loads = [0.0] * 4
    assignments = [[] for _ in loads]
    for name in sorted(names, key=lambda name: (-durations.get(name, default), name)):
        target = min(range(4), key=lambda index: (loads[index], len(assignments[index]), index))
        assignments[target].append(name)
        loads[target] += durations.get(name, default)
    return sorted(assignments[shard])


if __name__ == "__main__":
    if len(sys.argv) != 3 or sys.argv[2] not in ("0", "1", "2", "3"):
        sys.exit("usage: go_test_shard_plan.py <timings.tsv> <shard 0-3>")
    with open(sys.argv[1], encoding="utf-8") as history:
        for name in select(sys.stdin, history, int(sys.argv[2])):
            print(name)
