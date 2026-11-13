#!/usr/bin/env python3
"""Minimal Waza optimizer command adapter (contract 1.0), standard library only.

Waza writes one JSON request to stdin and reads one JSON response from stdout.
This adapter turns failed text graders from optimization-split feedback into
checklist lines appended to every proposed file. It makes no network calls.
"""
import json
import re
import sys

KIND = "waza.optimizer-adapter-response"


def describe():
    return {
        "kind": KIND,
        "contract_version": "1.0",
        "adapter": {
            "name": "checklist-example",
            "version": "0.1.0",
            "contract_versions": ["1.0"],
            "required_capabilities": ["propose.files", "feedback.optimization-split", "feedback.task-results"],
            "optional_capabilities": ["target.context-overlay", "target.skill-directory"],
            "paid_calls": False,
        },
    }


def propose(request):
    parent = request["parent"]
    latest = next((h for h in reversed(request["history"]) if h["candidate_id"] == parent["id"]), None)
    missing = []
    for task in (latest or {}).get("tasks", []):
        for grader in task["failed_graders"]:
            missing += re.findall(r"Missing expected substring: ([^;\n]+)", grader["message"])
    missing = sorted(set(m.strip() for m in missing))
    if not missing:
        return {"kind": KIND, "contract_version": "1.0", "done": True}
    files = []
    for f in parent["files"]:
        lines = "".join(f"- Mention {m}.\n" for m in missing)
        files.append({"path": f["path"], "content": f["content"].rstrip("\n") + "\n" + lines})
    return {
        "kind": KIND,
        "contract_version": "1.0",
        "candidate": {"parent_id": parent["id"], "files": files, "rationale": "Add checklist items for missing terms."},
    }


def main():
    request = json.load(sys.stdin)
    response = describe() if request["operation"] == "describe" else propose(request)
    json.dump(response, sys.stdout)


if __name__ == "__main__":
    main()
