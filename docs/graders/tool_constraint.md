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
        args:
          path: { regex: "^src/" }     # structured matcher on arbitrary args
    reject_tools:
      - tool: "bash"
        command_pattern: "rm\\s+-rf"
    allow_only:
      - tool: "bash"                   # exact name, NOT a regex
      - tool: "edit"
      - tool: "view"
```

**Options:**

| Option         | Type | Description                                                                              |
|----------------|------|------------------------------------------------------------------------------------------|
| `expect_tools` | list | Tool specs that MUST have been called (lower bound).                                     |
| `reject_tools` | list | Tool specs that MUST NOT have been called.                                               |
| `allow_only`   | list | Allow-list of tool specs (upper bound). Any observed tool call not matching an entry is a violation. Declared entries do NOT need to be exercised. An empty list denies all tool use. |

At least one option must be configured.

**Tool spec entry fields (structured format):**

| Field             | Type | Required | Description                                                                                                                              |
|-------------------|------|----------|------------------------------------------------------------------------------------------------------------------------------------------|
| `tool`            | str  | yes      | For `expect_tools`/`reject_tools`: regex matched against the tool name (case-insensitive). For `allow_only`: exact tool name (case-insensitive) — an entry `bash` will NOT match `bashful`. |
| `command_pattern` | str  | no       | Regex matched against the `command` argument (e.g. bash/powershell).                                                                     |
| `skill_pattern`   | str  | no       | Regex matched against the `skill` argument (skill invocations).                                                                          |
| `path_pattern`    | str  | no       | Regex matched against the `path` argument (file-based tools).                                                                            |
| `args`            | map  | no       | Structured argument matchers keyed by argument name.                                                                                     |

`args` supports the same structured matchers used by the `tool_calls` grader:

| Matcher | Meaning |
|---------|---------|
| `equals` | Deep equality with the supplied value |
| `regex` | RE2 match against the stringified argument |
| `contains` | Substring match against the stringified argument |
| `range` | Numeric bounds with `gte`, `lte`, `gt`, or `lt` |
| `json_schema` | JSON Schema validation for the argument value |

**Scoring:** `passed_checks / total_checks`

Each `expect_tools` and `reject_tools` entry counts as one check. For `allow_only`, each observed tool call counts as one check and each undeclared call counts as one failure; a session that made no tool calls at all counts as one vacuous pass.

**`allow_only` vs `expect_tools`**

- `expect_tools` is a *lower bound*: each listed tool must appear at least once.
- `allow_only` is an *upper bound*: each observed tool call must match at least one listed spec, but listed specs don't have to be used.
- The two compose. Use `expect_tools` for "the agent must have called X", `allow_only` for "the agent may only have called things in this set".

`allow_only` is also what `.agent.md` frontmatter `tools:` declarations desugar to — see the [custom agents guide](/waza/guides/custom-agents/).

