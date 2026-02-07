import json
import sys
from typing import Any, TypedDict
import re

## NOTE: if you're editing this file do NOT print to stdout - we parse that in the caller to determine
## what's failed.

class Event(TypedDict):
    role: str
    content: Any
    type: str

class Data(TypedDict):
    output: str
    assertions: list[str]
    outcome: dict[str, Any]
    transcript: list[dict[str, Event]]
    duration_ms: int

input_data = sys.stdin.read()
data: Data = json.loads(input_data)

# stderr isn't captured by the caller, so you can use this to do some print debugging.
# print(f"Received data: {input_data}", file=sys.stderr)

def print_stderr(*args, **kwargs):
    print(*args, **kwargs, file=sys.stderr)

eval_context = {
    "output": data['output'] or "",
    "outcome": data['outcome'],
    "chat_events": data['transcript'],
    "tool_calls": [t for t in data['transcript'] if t['role'] == "tool" or t["type"] == "tool_call"],
    "errors": [t for t in data['transcript'] if t.get("type") == "error" or "error" in str(t.get("content", ""))],
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

results = []

for assertion in data['assertions']:
    result = eval(assertion, {"__builtins__": {}}, eval_context)
    results.append(not not result)

print(json.dumps({
    "results": results,
}))
