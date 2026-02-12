import json
import sys
import re

## NOTE: if you're editing this file do NOT print to stdout - we parse that in the caller to determine
## what's failed.

from typing import Literal

input_data = sys.stdin.read()
data = json.loads(input_data)

# stderr isn't captured by the caller, so you can use this to do some print debugging.
# print(f"Received data: {input_data}", file=sys.stderr)

def print_stderr(*args, **kwargs):
    print(*args, **kwargs, file=sys.stderr)

eval_context = {
    "output": data['output'] or "",
    "outcome": data['outcome'],
    "transcript": data['transcript'],
    "tool_calls": data['tool_calls'],
    "errors": [t for t in data['transcript'] if "error" in t.get("type") or "error" in str(t.get("content", ""))],
    "duration_ms": data['duration_ms'],
    "len": len,
    "any": any,
    "all": all,
    "re": re,
    "str": str,
    "int": int,
    "float": float,
    "bool": bool,
    "list": list,
    "dict": dict,
    "True": True,
    "False": False,
}

# anything but empty string means we failed.
# 'fail' if it's just an assertion failure
# any other string is assumed to be an exception of some kind (ie, bad syntax, using a non-existent field).
results: list[str] = []

for assertion in data['assertions']:
    try:
        result = eval(assertion, {"__builtins__": {}}, eval_context)
        results.append("" if not not result else "fail")
    except Exception as e:
        results.append(str(e))

print(json.dumps({
    "results": results,
}))
