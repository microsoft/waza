### `program` - External Program Grader

Runs an external program or script to grade agent output. The agent output is passed via stdin, the workspace directory is available as the `WAZA_WORKSPACE_DIR` environment variable, and `WAZA_GRADING_CONTEXT` names a JSON file with the run's grading context.

```yaml
- type: program
  name: custom_validator
  config:
    command: python3
    args: ["graders/validate.py"]
    timeout: 60
```

**Options:**
| Option | Type | Description |
|--------|------|-------------|
| `command` | string | **Required.** The program to execute |
| `args` | list[str] | Arguments to pass to the program |
| `timeout` | int | Maximum execution time in seconds (default: 30) |

**Scoring:** Binary — exit code `0` means pass (`1.0`), non-zero means fail (`0.0`).

**Environment:**
- **stdin**: The agent's output text
- **`WAZA_WORKSPACE_DIR`**: Path to the post-execution workspace directory
- **`WAZA_GRADING_CONTEXT`**: Path to a JSON file containing the run's grading context (see below)

**stdout** from the program is captured and used as the grader feedback message on success.

**Grading context file:**

Waza writes the same run evidence that inline [`code`](code.md) graders receive on stdin to `context.json` in a new private temporary directory and passes the file's path in `WAZA_GRADING_CONTEXT`. Stdin is unchanged, so existing program graders keep working. After the program exits, Waza deletes the directory and everything in it, once it has verified that the directory and `context.json` are still the entries it created.

- **Privacy:** the directory has mode `0700` and the file `0600` on Linux and macOS. On Windows, both get a protected DACL that grants access only to the current user.
- **Ownership:** if the directory or `context.json` was moved away, deleted or replaced, Waza does not delete the replacement and grading fails with an error naming the path, because transcript data may remain elsewhere. Programs should treat the directory and file as read-only.
- **Failures:** if the file cannot be written or the directory cannot be removed, grading fails with an error naming the path, so transcript data is never left behind silently.
- **Trust model:** the grader program and other processes running as the same user are trusted; the program can read and copy the context anyway. The private directory protects against other local users. A replacement made by a concurrently running same-user process between Waza's check and its deletion is out of scope.

| Field | Type | Description |
|-------|------|-------------|
| `version` | int | Context format version, currently `1`. Additive fields keep the version. |
| `output` | string | Final agent output (same text as stdin) |
| `outcome` | object | Outcome data (`{}` when none) |
| `transcript` | list | Transcript events (`[]` when none) |
| `tool_calls` | list | Tool calls derived from the transcript (`[]` when none) |
| `tool_events` | list or `null` | Normalized per-call tool records |
| `command_invocations` | list or `null` | Sanitized calls handled by `command_mocks`: `command`, `response_index`, and optional `args` and `exit_code` (absent when the attempt did not complete) |
| `duration_ms` | int | Run duration in milliseconds |

`null` for `tool_events` or `command_invocations` means no evidence was recorded or preserved for this run (for example, no `command_mocks` were configured, or a results file omits the field). It is not proof that zero calls happened. An empty list means the evidence was present and contained no calls. `waza grade --results` builds the file offline from the saved run.

**Example: Python grader checking mocked CLI calls**

```yaml
- type: program
  name: account_lookup
  config:
    command: python3
    args: ["graders/check_commands.py"]
```

```python
#!/usr/bin/env python3
import json, os, sys

with open(os.environ["WAZA_GRADING_CONTEXT"], encoding="utf-8") as f:
    ctx = json.load(f)

calls = ctx["command_invocations"]
if calls is None:
    print("no command invocation evidence recorded", file=sys.stderr)
    sys.exit(1)

if not any(c["command"] == "az" and c.get("args", [])[:2] == ["account", "show"] for c in calls):
    print("expected 'az account show'", file=sys.stderr)
    sys.exit(1)

print(f"{len(calls)} mocked command call(s) checked")
```

**Example: Shell script grader**

```yaml
- type: program
  name: lint_check
  config:
    command: bash
    args: ["-c", "cd $WAZA_WORKSPACE_DIR && npm run lint"]
    timeout: 120
```

**Example: Python grader reading workspace files**

```yaml
- type: program
  name: file_validator
  config:
    command: python3
    args: ["graders/check_output.py"]
```

```python
#!/usr/bin/env python3
import os, sys

output = sys.stdin.read()
workspace = os.environ.get("WAZA_WORKSPACE_DIR", "")

# Check agent output
if "error" in output.lower():
    print("Output contains errors", file=sys.stderr)
    sys.exit(1)

# Check workspace files
readme = os.path.join(workspace, "README.md")
if not os.path.exists(readme):
    print("README.md not found", file=sys.stderr)
    sys.exit(1)

print("All checks passed")
```
