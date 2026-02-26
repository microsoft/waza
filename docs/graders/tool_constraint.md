### `tool_constraint` - Tool Usage Constraint Grader

Validates which tools an agent used (or avoided).

**Tool spec format** — match tool name and optionally arguments:

```yaml
- type: tool_constraint
  name: guardrails
  config:
    expect_tools:
      - tool: "bash"
        command_pattern: "azd\\s+up"   # optional regex on the command argument
      - tool: "skill"
        skill_pattern: "my-skill"      # optional regex on the skill argument
      - tool: "edit"                   # match tool name only (any args)
        path_pattern: "\\.go$"         # optional regex on the path argument
    reject_tools:
      - tool: "bash"
        command_pattern: "rm\\s+-rf"
```

**Options:**
| Option | Type | Description |
|--------|------|-------------|
| `expect_tools` | list | Tool specs that MUST have been called |
| `reject_tools` | list | Tool specs that MUST NOT have been called |

**Tool spec entry fields (structured format):**

| Field             | Type | Required | Description                                                           |
|-------------------|------|----------|-----------------------------------------------------------------------|
| `tool`            | str  | yes      | Regex matched against the tool name (case-insensitive).               |
| `command_pattern` | str  | no       | Regex matched against the `command` argument (e.g. bash/powershell).  |
| `skill_pattern`   | str  | no       | Regex matched against the `skill` argument (skill invocations).       |
| `path_pattern`    | str  | no       | Regex matched against the `path` argument (file-based tools).         |

At least one option must be configured.

**Scoring:** `passed_checks / total_checks`

Each `expect_tools` and `reject_tools` entry counts as one check.
