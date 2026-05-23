### `skill_invocation` - Skill Invocation Sequence Validation

Validates that dependent skills were invoked in the correct sequence during orchestration skill execution. Useful for verifying that orchestration workflows call the right skills in the right order, or that a specific skill was not invoked.

```yaml
- type: skill_invocation
  name: verify_orchestration
  config:
    required_skills:
      - azure-prepare
      - azure-deploy
    mode: in_order
    allow_extra: true
```

**Options:**
| Option | Type | Description |
|--------|------|-------------|
| `mode` | string | How to match required skill sequences (required when `required_skills` is set; ignored for forbidden-only configs) |
| `required_skills` | list[str] | Skill names that must be invoked in sequence |
| `forbidden_skills` | list[str] | Skill names that must not be invoked |
| `allow_extra` | bool | Whether to allow extra skill invocations beyond `required_skills` (default: true; ignored for forbidden-only configs) |

At least one of `required_skills` or `forbidden_skills` must be non-empty.

**Matching Modes:**

1. **`exact_match`** - Perfect match required
   - Actual sequence must match required sequence exactly
   - Same length, same order, same skills
   - Example: Required `["azure-prepare", "azure-deploy"]` only matches actual `["azure-prepare", "azure-deploy"]`

2. **`in_order`** - Skills must appear in order
   - All required skills must appear in actual sequence
   - Can have extra skills between required ones (if `allow_extra: true`)
   - Order must be preserved
   - Example: Required `["azure-prepare", "azure-deploy"]` matches actual `["azure-prepare", "azure-validate", "azure-deploy"]`

3. **`any_order`** - All skills present regardless of order
   - All required skills must appear in actual sequence
   - Order doesn't matter
   - Frequency must match (if required has 2x "skill-a", actual must have at least 2x "skill-a")
   - Example: Required `["azure-deploy", "azure-prepare"]` matches actual `["azure-prepare", "azure-validate", "azure-deploy"]`

**Scoring:**

The grader calculates three metrics:
- **Precision**: `true_positives / len(actual_skills)` - What fraction of actual invocations were required?
- **Recall**: `true_positives / len(required_skills)` - What fraction of required skills were invoked?
- **F1 Score**: `2 * precision * recall / (precision + recall)` - Harmonic mean (base score)

When `allow_extra: false`, the score is penalized for extra skill invocations beyond the required set.

For forbidden-only configs, the score is `1.0` when no forbidden skill appears and `0.0` when any forbidden skill appears. Unrelated skill invocations are allowed. For mixed required and forbidden configs, the grader uses the required-skill score unless a forbidden skill appears, which fails the grader with a score of `0.0`.

The `passed` field is based on the matching mode constraint, while the `score` field uses F1 (with optional penalty).

**Example Use Cases:**

```yaml
# Ensure exact orchestration workflow for reproducible deployments
- type: skill_invocation
  name: deployment_sequence
  config:
    mode: exact_match
    required_skills: ["azure-prepare", "azure-deploy", "azure-monitor"]
    allow_extra: false

# Verify key skills happen in order (allows flexibility)
- type: skill_invocation
  name: orchestration_flow
  config:
    mode: in_order
    required_skills: ["azure-prepare", "azure-deploy"]
    allow_extra: true

# Check that required skills were invoked (any order)
- type: skill_invocation
  name: required_skills
  config:
    mode: any_order
    required_skills: ["azure-prepare", "azure-deploy", "azure-validate"]
    allow_extra: true

# Ensure a skill was not invoked; unrelated skills are fine
- type: skill_invocation
  name: no_prod_deploy
  config:
    forbidden_skills: ["azure-prod-deploy"]
    allow_extra: true

# Require one skill while forbidding another
- type: skill_invocation
  name: safe_validation
  config:
    mode: any_order
    required_skills: ["azure-validate"]
    forbidden_skills: ["azure-prod-deploy"]
    allow_extra: true
```

**Data Source:**

This grader uses `SkillInvocations` data collected during execution via the Copilot SDK's `SkillInvoked` events. The skill names are extracted from the `Name` field of each `SkillInvocation` struct.
