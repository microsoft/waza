### `text` - Text Matching Grader

Validates output using substring matching and regex patterns.

```yaml
- type: text
  name: format_checker
  config:
    contains:
      - "deployed to"
      - "Resource group"
    not_contains:
      - "permission denied"
    regex_match:
      - "https?://.+"
    regex_not_match:
      - "(?i)fatal error|exception"
```

**Options:**
| Option | Type | Description |
|--------|------|-------------|
| `contains` | list[str] | Substrings that MUST appear (case-insensitive) |
| `not_contains` | list[str] | Substrings that MUST NOT appear (case-insensitive) |
| `contains_cs` | list[str] | Substrings that MUST appear (case-sensitive) |
| `not_contains_cs` | list[str] | Substrings that MUST NOT appear (case-sensitive) |
| `regex_match` | list[str] | Regex patterns that MUST match |
| `regex_not_match` | list[str] | Regex patterns that MUST NOT match |

**Scoring:** `passed_checks / total_checks`
