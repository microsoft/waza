### `diff` - Workspace File Comparison

Compares post-execution workspace files against expected snapshots and/or line fragments. Use this grader to verify that agent edits produce the correct file output — either matching an exact snapshot or containing (or excluding) specific content.

```yaml
- type: diff
  name: config_update
  config:
    expected_files:
      - path: "src/config.json"
        snapshot: "expected/config.json"
      - path: "README.md"
        contains:
          - "+## Installation"
          - "+npm install"
          - "-pip install"
```

**Options:**

| Option | Type | Description |
|--------|------|-------------|
| `expected_files` | list[object] | **Required.** List of file expectations (see below) |
| `context_dir` | string | Base directory for resolving snapshot paths. Defaults to the `--context-dir` CLI flag value |

Each entry in `expected_files` defines one file to check:

| Option | Type | Description |
|--------|------|-------------|
| `path` | string | **Required.** Workspace-relative path to the file to check |
| `snapshot` | string | Path to an expected file (relative to `context_dir`). Content must match exactly |
| `contains` | list[str] | Line fragments that must appear (`+` prefix) or must not appear (`-` prefix) |

At least one of `snapshot` or `contains` must be specified per entry.

**Contains Prefix Rules:**

| Prefix | Meaning | Example |
|--------|---------|---------|
| `+` | Fragment must be present | `"+import os"` |
| `-` | Fragment must be absent | `"-import sys"` |
| *(none)* | Fragment must be present (same as `+`) | `"import os"` |

**Scoring:** `passed_checks / total_checks`

Each expected file contributes:
- 1 implicit check for file existence
- 1 check for snapshot match (if `snapshot` is set)
- 1 check per `contains` entry

**Example: Exact Snapshot Matching**

```yaml
- type: diff
  name: exact_output
  config:
    expected_files:
      - path: "output/result.txt"
        snapshot: "snapshots/expected_result.txt"
```

**Example: Fragment Checking Only**

```yaml
- type: diff
  name: code_edits
  config:
    expected_files:
      - path: "src/main.py"
        contains:
          - "+def new_function():"
          - "+    return 42"
          - "-def old_function():"
      - path: "requirements.txt"
        contains:
          - "requests>=2.28"
```

**Example: Combined Snapshot + Contains**

```yaml
- type: diff
  name: thorough_check
  config:
    expected_files:
      - path: "config.yaml"
        snapshot: "expected/config.yaml"
        contains:
          - "+api_key: ${API_KEY}"
          - "-debug: true"
```

**Workspace Context:**

The diff grader operates on the post-execution workspace directory. Each task gets a fresh workspace with fixtures copied in (see [Fixture Isolation](../../README.md#fixture-isolation)). The `snapshot` path is resolved relative to `--context-dir`.
