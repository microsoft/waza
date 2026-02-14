# Grader Showcase Examples

This directory demonstrates all available grader types in waza through practical, runnable examples.

## Overview

**Graders** evaluate agent execution quality and produce scores. Each grader returns:
- `score`: 0.0 to 1.0
- `passed`: boolean
- `message`: human-readable result
- `details`: additional metadata

## Quick Start

Run all examples:
```bash
waza run examples/grader-showcase/eval.yaml -v
```

Run a specific task:
```bash
waza run examples/grader-showcase/eval.yaml -v --filter="regex-demo"
```

## Available Graders

### 1. Code Grader (`code`)
**File**: `tasks/code-task.yaml`

Evaluates Python expressions against the execution context.

**Available Context Variables**:
- `output` - Final skill output
- `outcome` - Outcome state
- `transcript` - Full execution transcript
- `tool_calls` - Tool calls from transcript
- `errors` - Errors from transcript
- `duration_ms` - Execution duration

**Example**:
```yaml
graders:
  - type: code
    name: validation
    config:
      assertions:
        - "len(output) > 0"
        - "'success' in output.lower()"
        - "len(errors) == 0"
        - "duration_ms < 30000"
```

**Use Cases**:
- Verify output format and length
- Check for specific content patterns
- Validate no errors occurred
- Ensure execution efficiency
- Complex logic requiring Python expressions

---

### 2. Regex Grader (`regex`)
**File**: `tasks/regex-task.yaml`

Matches output against regular expression patterns.

**Example**:
```yaml
graders:
  - type: regex
    name: pattern_check
    config:
      must_match:
        - "(?i)deployed to https?://.+"
        - "Resource group: .+"
      must_not_match:
        - "error|failed|exception"
```

**Use Cases**:
- Validate output format (URLs, email addresses, IDs)
- Check for specific keywords or phrases
- Ensure unwanted patterns don't appear
- Verify structured output

---

### 3. File Grader (`file`)
**File**: `tasks/file-task.yaml`

Validates file existence, non-existence, and content patterns in the workspace.

**Example**:
```yaml
graders:
  - type: file
    name: file_check
    config:
      must_exist:
        - "README.md"
        - "config.json"
      must_not_exist:
        - "temp.txt"
      content_patterns:
        - path: "README.md"
          must_match:
            - "(?i)installation"
          must_not_match:
            - "TODO|FIXME"
```

**Use Cases**:
- Verify required files were created
- Ensure temporary files were cleaned up
- Validate file content matches expected patterns
- Check files weren't accidentally deleted

---

### 4. Behavior Grader (`behavior`)
**File**: `tasks/behavior-task.yaml`

Validates agent behavioral patterns like tool usage and efficiency.

**Example**:
```yaml
graders:
  - type: behavior
    name: efficiency_check
    config:
      max_tool_calls: 20
      max_tokens: 10000
      max_duration_ms: 300000
      required_tools:
        - "view"
        - "bash"
      forbidden_tools:
        - "sudo"
        - "rm -rf"
```

**Configuration Options**:
- `max_tool_calls` - Maximum allowed tool calls
- `max_tokens` - Maximum token usage allowed
- `max_duration_ms` - Maximum execution time in milliseconds
- `required_tools` - Tools that MUST be called (exact string match)
- `forbidden_tools` - Tools that MUST NOT be called (exact string match)

**Use Cases**:
- Enforce efficiency constraints
- Prevent unsafe tool usage
- Ensure required tools are used
- Control costs via token/time limits

---

### 5. Action Sequence Grader (`action_sequence`)
**File**: `tasks/action-sequence-task.yaml`

Validates that agent's tool calls match an expected sequence.

**Matching Modes**:

1. **`exact_match`** - Perfect match required
   - Same length, same order, same tools
   - Example: `["bash", "edit"]` only matches `["bash", "edit"]`

2. **`in_order_match`** - Actions must appear in order
   - Can have extra actions between expected ones
   - Example: `["bash", "edit"]` matches `["bash", "view", "edit", "report_progress"]`

3. **`any_order_match`** - All actions present regardless of order
   - Order doesn't matter, but frequency must match
   - Example: `["edit", "bash"]` matches `["bash", "view", "edit"]`

**Example**:
```yaml
graders:
  - type: action_sequence
    name: workflow_check
    config:
      matching_mode: in_order_match
      expected_actions:
        - "view"
        - "edit"
        - "bash"
```

**Scoring**:
- **Precision**: `true_positives / len(actual_actions)`
- **Recall**: `true_positives / len(expected_actions)`
- **F1 Score**: Harmonic mean (used as final score)

**Use Cases**:
- Enforce specific workflows (e.g., view → edit → test)
- Verify required steps are performed
- Ensure demo scripts follow exact sequences
- Validate best practices (read before write)

---

### 6. Skill Invocation Grader (`skill_invocation`)
**File**: `tasks/skill-invocation-example.yaml`

Validates that dependent skills were invoked in the correct sequence during orchestration.

**Matching Modes**:

1. **`exact_match`** - Perfect match required
   - Same length, same order, same skills
   - Example: `["azure-prepare", "azure-deploy"]` only matches `["azure-prepare", "azure-deploy"]`

2. **`in_order`** - Skills must appear in order
   - Can have extra skills between required ones (if allow_extra: true)
   - Example: `["azure-prepare", "azure-deploy"]` matches `["azure-prepare", "azure-validate", "azure-deploy"]`

3. **`any_order`** - All skills present regardless of order
   - Order doesn't matter, but frequency must match
   - Example: `["azure-deploy", "azure-prepare"]` matches `["azure-prepare", "azure-validate", "azure-deploy"]`

**Example**:
```yaml
graders:
  - type: skill_invocation
    name: deployment_flow
    config:
      mode: in_order
      required_skills:
        - azure-prepare
        - azure-deploy
        - azure-monitor
      allow_extra: true
```

**Scoring**:
- **Precision**: `true_positives / len(actual_skills)`
- **Recall**: `true_positives / len(required_skills)`
- **F1 Score**: Harmonic mean (with optional penalty for extras when allow_extra: false)

**Use Cases**:
- Verify orchestration workflows invoke correct skills
- Ensure skill dependencies are respected
- Validate multi-skill coordination patterns
- Check that required skills are called in proper order

---

## Directory Structure

```
examples/grader-showcase/
├── eval.yaml                         # Main benchmark spec
├── README.md                         # This file
├── fixtures/                         # Context files for tasks
│   ├── sample.py                     # Sample Python file
│   └── config.json                   # Sample config file
└── tasks/                            # Individual task definitions
    ├── code-task.yaml                # Code grader demo
    ├── regex-task.yaml               # Regex grader demo
    ├── file-task.yaml                # File grader demo
    ├── behavior-task.yaml            # Behavior grader demo
    ├── action-sequence-task.yaml     # Action sequence grader demo
    └── skill-invocation-example.yaml # Skill invocation grader demo
```

## Task Breakdown

### 1. Code Task (`code-task.yaml`)
**Agent Task**: Count files in directory  
**Grader Focus**: Python assertions on output, errors, duration, and tool_calls

### 2. Regex Task (`regex-task.yaml`)
**Agent Task**: List programming languages  
**Grader Focus**: Pattern matching for expected/unexpected content

### 3. File Task (`file-task.yaml`)
**Agent Task**: Create README with specific sections  
**Grader Focus**: File existence and content pattern validation

### 4. Behavior Task (`behavior-task.yaml`)
**Agent Task**: Read and explain a file  
**Grader Focus**: Tool usage constraints (must use view, can't use edit/bash)

### 5. Action Sequence Task (`action-sequence-task.yaml`)
**Agent Task**: Add function and verify  
**Grader Focus**: Tool call sequence (view → edit → view)

### 6. Skill Invocation Task (`skill-invocation-example.yaml`)
**Agent Task**: Create a deployment orchestration workflow  
**Grader Focus**: Skill invocation sequence validation

## Global vs Task-Specific Graders

### Global Graders (in eval.yaml)
Applied to ALL tasks:
```yaml
graders:
  - type: code
    name: has_output
    config:
      assertions:
        - "len(output) > 0"
```

### Task-Specific Graders (in task files)
Applied only to that task:
```yaml
graders:
  - type: regex
    name: task_specific_check
    config:
      must_match:
        - "expected pattern for this task only"
```

## Best Practices

### 1. Choose the Right Grader
- **Simple pattern matching** → Use `regex`
- **Complex logic/calculations** → Use `code`
- **File operations** → Use `file`
- **Tool usage validation** → Use `behavior`
- **Workflow enforcement** → Use `action_sequence`

### 2. Combine Multiple Graders
Tasks can use multiple graders for comprehensive validation:
```yaml
graders:
  - type: regex        # Check output format
  - type: code         # Validate logic
  - type: behavior     # Ensure efficiency
```

### 3. Balance Strictness
- **Strict matching**: Use `exact_match` mode or precise regex
- **Flexible matching**: Use `in_order_match` or loose patterns
- **Overly strict graders** can cause false failures
- **Too loose graders** may miss real issues

### 4. Use Descriptive Names
Good grader names help debug failures:
```yaml
# ❌ Bad
- type: regex
  name: check1

# ✅ Good  
- type: regex
  name: deployment_url_format
```

## Customizing Examples

### Using Real Copilot Agent
Change executor in `eval.yaml`:
```yaml
config:
  executor: copilot-sdk  # Instead of mock
```

### Adding Custom Tasks
1. Create new YAML file in `tasks/`
2. Follow existing task structure
3. Add graders specific to your use case
4. Tasks are auto-discovered by glob pattern

### Modifying Fixtures
Edit files in `fixtures/` or add new ones. Reference them in tasks:
```yaml
inputs:
  files:
    - path: your-new-file.txt
```

## Testing & Validation

### Run with Verbose Output
```bash
waza run examples/grader-showcase/eval.yaml -v
```

### Save Results
```bash
waza run examples/grader-showcase/eval.yaml -o results.json
```

### Filter Specific Tasks
```bash
# Run only regex demo
waza run examples/grader-showcase/eval.yaml --filter="regex"

# Run only behavior and action sequence demos
waza run examples/grader-showcase/eval.yaml --filter="behavior|action"
```

## Related Documentation

- **Full Grader Reference**: See `docs/GRADERS.md`
- **Code Explainer Example**: See `examples/code-explainer/`
- **CI Integration Examples**: See `examples/ci/`

## Notes

- **Mock Executor**: Default config uses `mock` executor for fast testing
  - Mock executor returns dummy responses
  - Change to `copilot-sdk` for real agent testing
  
- **Not Implemented**: `prompt` grader is documented but not yet implemented
  - Will be available in a future release
  - Use `code` grader with LLM validation as interim solution

## Support

For issues or questions:
- Check `docs/GRADERS.md` for detailed grader documentation
- Review other examples in `examples/` directory
- Open an issue on GitHub
