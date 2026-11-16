#!/usr/bin/env python3
"""Assign complete remaining Go packages using measured race/atomic costs."""

import math
import statistics
import sys

SHARD_COUNT = 4
NATIVE_PACKAGE = "github.com/microsoft/waza/internal/controlledcomparison"


def parse_history(history):
    durations = {}
    for line in history:
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        fields = line.split()
        if len(fields) != 2:
            raise ValueError(f"invalid package timing: {line.rstrip()}")
        name, raw_seconds = fields
        seconds = float(raw_seconds)
        if name in durations or not math.isfinite(seconds) or seconds < 0:
            raise ValueError(f"invalid or duplicate timing for {name}")
        durations[name] = seconds
    return durations


def inventory(lines, label):
    items = [line.strip() for line in lines if line.strip()]
    if not items or len(items) != len(set(items)):
        raise ValueError(f"{label} inventory must be nonempty and unique")
    return items


def assignments(packages, history, shard_count=SHARD_COUNT):
    names = inventory(packages, "package")
    if shard_count < 1 or len(names) < shard_count:
        raise ValueError("package inventory must populate every shard")
    durations = parse_history(history)
    if NATIVE_PACKAGE in names:
        raise ValueError("native controlled-comparison package belongs to its own shards")
    measured = [seconds for seconds in durations.values() if seconds > 0]
    fallback = statistics.median(measured) if measured else 1.0
    loads = [0.0] * shard_count
    groups = [[] for _ in loads]
    for name in sorted(names, key=lambda item: (-durations.get(item, fallback), item)):
        target = min(range(shard_count), key=lambda index: (loads[index], len(groups[index]), index))
        groups[target].append(name)
        loads[target] += durations.get(name, fallback)
    return [sorted(group) for group in groups]


def select(packages, history, shard):
    if shard < 0 or shard >= SHARD_COUNT:
        raise ValueError(f"shard must be between 0 and {SHARD_COUNT - 1}")
    return assignments(packages, history)[shard]


def select_tests(test_inventory, package_inventory, selected_packages):
    packages = set(inventory(package_inventory, "package"))
    selected = set(selected_packages)
    if not selected or not selected.issubset(packages):
        raise ValueError("selected packages must be a nonempty subset of discovery")
    tests = inventory(test_inventory, "test")
    selected_tests = []
    for target in tests:
        package, separator, name = target.partition("::")
        if not separator or not package or not name or package not in packages:
            raise ValueError(f"invalid or unassigned test target: {target}")
        if package in selected:
            selected_tests.append(target)
    if not selected_tests:
        raise ValueError("package assignment selected no discovered tests")
    return sorted(selected_tests)


def main(argv):
    if len(argv) not in (3, 5) or (len(argv) == 5 and argv[3] != "--tests"):
        raise SystemExit(
            "usage: go_remaining_shard_plan.py <durations.tsv> <shard 0-3> "
            "[--tests <test-inventory>]"
        )
    try:
        shard = int(argv[2])
        with open(argv[1], encoding="utf-8") as history_file:
            history = history_file.readlines()
        packages = sys.stdin.readlines()
        selected_packages = select(packages, history, shard)
        if len(argv) == 5:
            with open(argv[4], encoding="utf-8") as tests_file:
                selected = select_tests(tests_file, packages, selected_packages)
        else:
            selected = selected_packages
    except (OSError, ValueError) as error:
        raise SystemExit(str(error)) from error
    if selected:
        sys.stdout.buffer.write(("\n".join(selected) + "\n").encode("utf-8"))


if __name__ == "__main__":
    main(sys.argv)
