# `trigger` Grader

Heuristic grader for validating whether a prompt should activate a skill.

## Use Cases

- Validate that relevant prompts trigger a skill (`mode: positive`)
- Validate that unrelated prompts do not trigger a skill (`mode: negative`)
- Add a lightweight trigger check without running full trigger test suites

## Configuration

```yaml
- type: trigger
  name: deploy_trigger
  config:
    skill_path: skills/azure-deploy/SKILL.md
    mode: positive
    threshold: 0.6
```

### Fields

- `skill_path` (required): Path to the skill `SKILL.md` file.
- `mode` (required): `positive` or `negative`.
  - `positive`: passes when score is `>= threshold`.
  - `negative`: passes when score is `< threshold`.
- `threshold` (optional): Score threshold between `0.0` and `1.0`. Default: `0.6`.

## How Scoring Works

The grader extracts heuristic keywords from:

- skill name
- frontmatter description
- skill body headings/content
- parsed `USE FOR:` phrases from description frontmatter

It then scores prompt relevance by combining:

1. Token overlap between prompt tokens and extracted skill keywords
2. Best phrase-match score against `USE FOR:` trigger phrases

Final score is the higher of these two signals (range `0.0` to `1.0`).

## Output Details

`details` includes:

- `mode`
- `threshold`
- `skill_path`
- `matched_keywords`
- `matched_count`
- `keyword_count`
- `phrase_score`
