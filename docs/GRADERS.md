# Native Grader Reference

This maintained compatibility entrypoint preserves older repository links.
The [site catalog](../site/src/content/docs/guides/graders.mdx#at-a-glance)
and linked native configuration pages are canonical. There are **13 native
grader kinds**, validated against the enum and schema by the docs regressions.
Regex/keyword checks use `text`; old `llm`/`script` examples use `prompt`/`program`.
`trigger_tests` is suite configuration, not a grader kind.

## code - Assertion-Based Grader

[`code`: inline Python/JavaScript assertions](graders/code.md).

## text - Text Matching Grader

[`text`: substring and `regex_match` predicates](graders/text.md).

## file - File System Validation

[`file`: final workspace file checks](graders/file.md).

## diff - Workspace File Comparison

[`diff`: expected snapshots/fragments](graders/diff.md).

## behavior - Agent Behavior Validation

[`behavior`: observed metrics and tool constraints](graders/behavior.md).

## action_sequence - Tool Call Sequence Validation

[`action_sequence`: observed call order](graders/action_sequence.md).

## skill_invocation - Skill Invocation Sequence Validation

[`skill_invocation`: observed skill calls](graders/skill_invocation.md).

## prompt - LLM-Based Evaluation

[`prompt`: explicit judge instructions](graders/prompt.md). May incur model costs.

## Trigger Tests

Use native [`trigger`](graders/trigger.md) for activation grading; suite
`trigger_tests` is not itself a grader kind.

## JSON Schema

[`json_schema`: structured output validation](graders/json_schema.md).

## Program

[`program`: external command exit status](graders/program.md).

## Tool Constraint

[`tool_constraint`: required/forbidden observations](graders/tool_constraint.md).

## Tool Calls

[`tool_calls`: call counts, names and arguments](graders/tool_calls.md).
