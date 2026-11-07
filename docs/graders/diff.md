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

Waza preserves regular workspace files before the agent session disconnects so this grader can observe edits that disconnect may restore. `.git` and `node_modules` trees are excluded by default, while every `expected_files[].path` remains explicitly preserved even inside those directories. Capture is limited to 10,000 files, 20,000 traversal entries, 16 MiB per file, and 64 MiB total. If a target or workspace exceeds those limits, the run reports a workspace-capture error and skips graders and snapshot updates rather than grading an incomplete snapshot.

With `--skip-graders`, grader-only preservation requirements are omitted, including task and checkpoint diff targets. Ordinary workspace capture remains enabled with the same exclusions and limits; unused targets in excluded trees cannot fail execution-only runs.

If capture fails after a follow-up or responder turn, the error result still retains that turn's output, transcript, tool calls, skill invocations, duration, and usage. Successfully executed responder replies remain counted in `responder.followups_sent` even when their capture fails; replies that fail setup or execution, time out, or are canceled are not counted, including when capture also fails. Previously completed checkpoint results remain recorded, but the failed capture cannot supply workspace evidence or run later checkpoints and final graders. Trigger tests record capture failures as execution errors rather than successful trigger classifications.

A capture-failed response returned alongside an execution error still preserves its observations and rejects workspace evidence from earlier turns. The original execution error remains recorded, and the failed reply does not count as successfully executed.

Newly generated workspace-capture diagnostics never include absolute filesystem
paths or the raw underlying filesystem error. I/O failures report a bounded
code (`not_found`, `permission`, `closed`, or `io`), operation, and actionable
message; only contained logical workspace-relative names may appear, quoted
and truncated to 160 bytes. Rejected absolute or escaping targets are not echoed.
Typed filesystem causes remain available internally through `errors.Is` and
`errors.As`, but are excluded from serialized responses. This boundary applies
only to generated capture diagnostics: workspace metadata, authored output,
transcripts, other error producers, and legacy artifacts are not scrubbed.
