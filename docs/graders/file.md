### `file` - File Existence & Content Grader

Validates file existence, absence, and content patterns in the post-execution workspace.

```yaml
- type: file
  name: output_files
  config:
    must_exist:
      - "src/main.py"
      - "README.md"
    must_not_exist:
      - "temp/debug.log"
    content_patterns:
      - path: "src/main.py"
        regex_match:
          - "def main\\("
          - "import os"
        regex_not_match:
          - "TODO"
          - "HACK"
```

**Options:**
| Option | Type | Description |
|--------|------|-------------|
| `must_exist` | list[str] | File paths (relative to workspace) that must be present |
| `must_not_exist` | list[str] | File paths (relative to workspace) that must be absent |
| `content_patterns` | list[object] | Regex patterns to match against file contents (see below) |

At least one of `must_exist`, `must_not_exist`, or `content_patterns` must be specified.

**Content Pattern Object:**
| Field | Type | Description |
|-------|------|-------------|
| `path` | string | File path (relative to workspace) |
| `must_match` | list[str] | Regex patterns that must match the file content |
| `must_not_match` | list[str] | Regex patterns that must not match the file content |

**Scoring:** `passed_checks / total_checks`

Each check counts separately:
- 1 check per `must_exist` entry
- 1 check per `must_not_exist` entry
- 1 implicit file existence check per `content_patterns` entry, plus 1 per regex pattern

**Path Safety:** All paths must be relative to the workspace. Absolute paths and paths that escape the workspace (e.g., via `..` traversal) are rejected.

**Example: Verify generated project structure**

```yaml
- type: file
  name: project_structure
  config:
    must_exist:
      - "package.json"
      - "src/index.ts"
      - "tsconfig.json"
    must_not_exist:
      - "node_modules"
    content_patterns:
      - path: "package.json"
        regex_match:
          - '"name"'
          - '"scripts"'
```
