# Retained AgentV evidence

All three upstream files are verbatim from
[`EntityProcess/agentv@68df6d2206cea61f721d0445b405efcb5e94aff0`](https://github.com/EntityProcess/agentv/tree/68df6d2206cea61f721d0445b405efcb5e94aff0).
The accompanying MIT license applies.

| Local file | Upstream path | SHA256 |
|---|---|---|
| `suite.yaml` | `examples/features/assert/evals/suite.yaml` | `5abd706017618e14cf80222a922a9ec04746466f1f5535dfd41557af23841b88` |
| `assertions.ts` | `packages/core/src/evaluation/graders/assertions.ts` | `152446e80042b29597eb9d124860a6eb67587be1d642b50c101af3711c9ecb02` |
| `LICENSE` | `LICENSE` | `a310bb09289f002d4d27912f9254926afb9510ce2289b6c6154816ab21fc577f` |

`TestPinnedAgentVFiles` freezes these bytes. `TestInteropProductionCLIUpstreamDecisions`
uses the real `contains-check` assertion from the suite and executes the retained
upstream `runContainsAssertion` implementation with Node type stripping. It
compares positive, negative, case-sensitive, empty and Unicode output decisions
with production Waza CLI import and grade. No providers, setup hooks, benchmark
scripts or inference are executed.

This proves only the selected assertion's binary score/decision semantics.
The source suite includes system messages, regex and LLM rubric assertions;
none of those semantics are converted. The adapter's explicit selection and
diagnostic are not whole-suite compatibility evidence. Node 22.18+ or 24+ is
required to execute the TypeScript oracle; no npm dependency is required.
