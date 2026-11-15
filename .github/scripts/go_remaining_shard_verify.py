#!/usr/bin/env python3
"""Verify four exhaustive remaining-package shards and merge atomic coverage."""

import hashlib
import json
import re
import sys
from collections import Counter
from pathlib import Path

SHARD_COUNT = 4
NATIVE_PACKAGE = "github.com/microsoft/waza/internal/controlledcomparison"
MAX_EVIDENCE_BYTES = 64 * 1024 * 1024
MAX_TARGETS = 100_000
SHA_PATTERN = re.compile(r"^[0-9a-f]{40}$")
COVERAGE_PATTERN = re.compile(
    r"^(?P<file>.+):(?P<start_line>[1-9][0-9]*)\.(?P<start_column>[1-9][0-9]*),"
    r"(?P<end_line>[1-9][0-9]*)\.(?P<end_column>[1-9][0-9]*) "
    r"(?P<statements>[1-9][0-9]*) (?P<count>0|[1-9][0-9]*)$"
)


def fail(message):
    raise ValueError(message)


def read_lines(path, label):
    if not path.is_file() or path.stat().st_size > MAX_EVIDENCE_BYTES:
        fail(f"{label} is missing or exceeds the evidence limit: {path.name}")
    text = path.read_text(encoding="utf-8")
    items = text.splitlines()
    if not items or any(not item or item.strip() != item for item in items) or len(items) != len(set(items)):
        fail(f"{label} is empty, malformed, or duplicated: {path.name}")
    return items


def read_coverage(path):
    lines = read_lines(path, "atomic coverage profile")
    if lines[0] != "mode: atomic" or len(lines) < 2:
        fail(f"coverage profile is not a nonempty atomic profile: {path.name}")
    records = {}
    for line in lines[1:]:
        match = COVERAGE_PATTERN.fullmatch(line)
        if not match:
            fail(f"malformed coverage record in {path.name}")
        values = match.groupdict()
        start = (int(values["start_line"]), int(values["start_column"]))
        end = (int(values["end_line"]), int(values["end_column"]))
        if end < start:
            fail(f"invalid coverage block range in {path.name}")
        key = (values["file"], start, end)
        if key in records:
            fail(f"duplicate coverage block in {path.name}")
        records[key] = (int(values["statements"]), int(values["count"]))
    return records


def merge_coverage(profiles):
    merged = {}
    for records in profiles:
        if not records:
            fail("coverage profile contains no blocks")
        for key, (statements, count) in records.items():
            previous = merged.get(key)
            if previous is not None and previous[0] != statements:
                fail("coverage block statement counts disagree")
            merged[key] = (statements, count + (previous[1] if previous else 0))
    lines = ["mode: atomic"]
    for (filename, start, end), (statements, count) in sorted(merged.items()):
        lines.append(
            f"{filename}:{start[0]}.{start[1]},{end[0]}.{end[1]} {statements} {count}"
        )
    return "\n".join(lines) + "\n"


def verify(directory):
    root = Path(directory)
    if not root.is_dir():
        fail("remaining-shard artifact directory is missing")

    package_inventories = []
    package_selections = []
    test_inventories = []
    test_selections = []
    profiles = []
    identities = []
    for shard in range(SHARD_COUNT):
        prefix = f"coverage-remaining-{shard}.txt"
        packages = read_lines(root / f"{prefix}.packages.all", "package inventory")
        selected_packages = read_lines(root / f"{prefix}.packages.selected", "package selection")
        tests = read_lines(root / f"{prefix}.all", "test inventory")
        selected_tests = read_lines(root / f"{prefix}.selected", "test selection")
        evidence_path = root / f"{prefix}.evidence.json"
        if not evidence_path.is_file() or evidence_path.stat().st_size > MAX_EVIDENCE_BYTES:
            fail(f"shard {shard} evidence receipt is missing or oversized")
        try:
            report = json.loads(evidence_path.read_text(encoding="utf-8"))
        except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
            fail(f"shard {shard} evidence receipt is missing or invalid: {error}")
        if not isinstance(report, dict) or report.get("kind") != "go" or report.get("schema") != 1:
            fail(f"shard {shard} evidence receipt has an unsupported schema")
        if report.get("complete") is not True or report.get("diagnostics") != []:
            fail(f"shard {shard} evidence is incomplete: {report.get('diagnostics')}")
        if any(report.get(key) != 0 for key in
               ("discovery_exit", "discovery_collector_exit", "execution_exit", "collector_exit")):
            fail(f"shard {shard} discovery, execution, or collector failed")
        if report.get("inventory_scope") != "qualified":
            fail(f"shard {shard} evidence does not use qualified test names")
        if report.get("policy") != {
            "retries": 0, "count": 1, "race": True, "covermode": "atomic", "timeout": "30m"
        }:
            fail(f"shard {shard} changed the race, atomic, retry, count, or timeout policy")
        identity = report.get("identity")
        if (not isinstance(identity, dict) or not SHA_PATTERN.fullmatch(str(identity.get("source_sha", "")))
                or identity.get("workflow_sha") != identity.get("source_sha")
                or identity.get("repository") != "microsoft/waza"
                or identity.get("workflow") != "Go Build and Test"
                or identity.get("job") != "remaining"
                or identity.get("os") != "Linux"
                or not identity.get("run_id") or not identity.get("attempt")):
            fail(f"shard {shard} source/run identity is invalid")
        identities.append((identity.get("source_sha"), str(identity.get("run_id")),
                           str(identity.get("attempt")), identity.get("event")))

        if packages != sorted(packages) or selected_packages != sorted(selected_packages):
            fail(f"shard {shard} package lists are not canonical")
        if not set(selected_packages).issubset(packages) or not selected_packages:
            fail(f"shard {shard} selected packages are outside discovery or empty")
        discovered = report.get("discovered")
        if (not isinstance(discovered, list) or len(discovered) > MAX_TARGETS
                or any(not isinstance(item, str) for item in discovered)
                or len(discovered) != len(tests) or set(discovered) != set(tests)):
            fail(f"shard {shard} evidence does not match the discovered test inventory")
        observed_selection = report.get("selected")
        if (not isinstance(observed_selection, list) or len(observed_selection) > MAX_TARGETS
                or any(not isinstance(item, str) for item in observed_selection)
                or len(observed_selection) != len(selected_tests)
                or set(observed_selection) != set(selected_tests)):
            fail(f"shard {shard} evidence does not match the selected test inventory")
        if not set(selected_tests).issubset(tests) or not selected_tests:
            fail(f"shard {shard} selected tests are outside discovery or empty")
        for target in selected_tests:
            package, separator, name = target.partition("::")
            if not separator or not name or package not in selected_packages:
                fail(f"shard {shard} selected a test outside its assigned packages")

        packages_seen = report.get("packages")
        if not isinstance(packages_seen, list) or not packages_seen:
            fail(f"shard {shard} package execution receipt is empty")
        observed_packages = set()
        for item in packages_seen:
            if (not isinstance(item, dict) or not item.get("executed") or item.get("status") != "pass"
                    or item.get("package") in observed_packages):
                fail(f"shard {shard} has a failed or duplicate package execution")
            observed_packages.add(item["package"])
        if observed_packages != set(selected_packages):
            fail(f"shard {shard} executed packages do not match its assignment")

        targets = report.get("targets")
        if not isinstance(targets, list):
            fail(f"shard {shard} test execution receipt is invalid")
        observed_tests = set()
        for item in targets:
            if not isinstance(item, dict) or item.get("top_level") is not True:
                continue
            target = f"{item.get('package')}::{item.get('name')}"
            if (not item.get("executed") or item.get("status") not in ("pass", "skip")
                    or target in observed_tests):
                fail(f"shard {shard} has a failed, unfinished, or duplicate test execution")
            observed_tests.add(target)
        if observed_tests != set(selected_tests):
            fail(f"shard {shard} execution does not cover its selected tests")

        package_inventories.append(packages)
        package_selections.append(selected_packages)
        test_inventories.append(tests)
        test_selections.append(selected_tests)
        profiles.append(read_coverage(root / prefix))

    if any(identity != identities[0] for identity in identities[1:]):
        fail("remaining-shard receipts do not share one source, run, attempt, and event")
    if any(items != package_inventories[0] for items in package_inventories[1:]):
        fail("remaining shards discovered different package inventories")
    if any(set(items) != set(test_inventories[0]) for items in test_inventories[1:]):
        fail("remaining shards discovered different test inventories")
    if NATIVE_PACKAGE in package_inventories[0]:
        fail("native controlled-comparison package was included in remaining shards")
    if Counter(item for group in package_selections for item in group) != Counter(
            {item: 1 for item in package_inventories[0]}):
        fail("remaining package assignments are not exhaustive and exactly once")
    if Counter(item for group in test_selections for item in group) != Counter(
            {item: 1 for item in test_inventories[0]}):
        fail("remaining test assignments are not exhaustive and exactly once")

    merged_profile = merge_coverage(profiles)
    return {
        "kind": "waza.go-remaining-shards",
        "schema": 1,
        "source_sha": identities[0][0],
        "run_id": identities[0][1],
        "attempt": identities[0][2],
        "shard_count": SHARD_COUNT,
        "package_count": len(package_inventories[0]),
        "test_count": len(test_inventories[0]),
        "coverage_block_count": len(merged_profile.splitlines()) - 1,
        "coverage_sha256": hashlib.sha256(merged_profile.encode("utf-8")).hexdigest(),
    }, merged_profile


def main(argv):
    if len(argv) != 4:
        raise SystemExit(
            "usage: go_remaining_shard_verify.py <artifact-directory> "
            "<merged-profile> <verification-report>"
        )
    try:
        report, profile = verify(argv[1])
        Path(argv[2]).write_text(profile, encoding="utf-8", newline="\n")
        Path(argv[3]).write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8", newline="\n")
    except (OSError, ValueError) as error:
        raise SystemExit(str(error)) from error
    print(
        f"Verified {report['shard_count']} remaining shards: "
        f"{report['package_count']} packages and {report['test_count']} tests; "
        f"merged {report['coverage_block_count']} atomic coverage blocks."
    )


if __name__ == "__main__":
    main(sys.argv)
