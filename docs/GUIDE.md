# Waza User Guide

Welcome to **Waza**, a Go CLI for evaluating agent workflows, skills and custom
agents. Author repository, CLI and MCP scenarios, run graded trials and inspect
recorded evidence. Existing skill workflows remain supported.

Begin with [Concepts and learning order](../site/src/content/docs/concepts.mdx),
then the [offline Quick Start](../site/src/content/docs/quick-start.mdx).
Mock trials prove harness wiring, not agent quality. Read the
[authentication and usage prerequisites](../site/src/content/docs/getting-started.mdx#authenticate-before-real-execution)
before model-backed runs. Scenario/preflight/evidence features are included in
published `v0.39.0` (October 20, 2026); install that release or run `waza update`.
Explicit opt-in and platform limits still apply; see
[availability](../site/src/content/docs/reference/releases.mdx).

## What is Waza?

Waza helps you:

- **Define agent skills** with comprehensive documentation and behavioral requirements
- **Evaluate custom agents** (`.agent.md` files) with automatic tool constraint validation
- **Create test suites** with realistic test cases and validation rules  
- **Run evaluations** against different AI models to measure skill effectiveness
- **Reuse remote grader presets** with Go-module-style `ref` entries pinned by `waza.lock`
- **Compare results** across models and versions to track improvement
- **Select fixed-design release assessment explicitly** with `compare` or `gate`
  using `--release-policy` and `--collection-dir`; historical results do not
  become fresh paired evidence. Use `compare-plan` with explicit design/
  requirements and `compare-collect` for the offline mock collector, or
  explicitly select `--collector native-copilot` for the bounded,
  potentially paid Copilot SDK profile. See
  [the end-to-end example and limits](CONTROLLED-COMPARISONS.md).
- **Opt in separately to offline assurance** with `compare-assurance-plan`
  and a new exclusive `--output`, then explicitly select `--assurance-contract`
  with `--release-policy` during collection and assessment. Supply current
  per-arm eval/reference inputs and separately selected review-source acceptance.
  Only mock/no-skills/native-text finite authored cases and retained actual
  responses are supported. Both fresh offline1.0 reports, deterministic regrade
  and all base non-assurance conditions must pass; stored report labels are
  never adopted. Unreviewed examples stay nonpassing. Live SDK assurance,
  calibration, paid1.1/billing support and automatic paid validation are not
  supported.
  Frozen base decisions and the API/dashboard still cannot pass required
  assurance; only the explicit outer CLI decision can.
  See [the independent profile](CONTROLLED-COMPARISONS.md#independent-offline-assurance-profile)
  for exact-source-byte versus JSON-v1 bindings and human/JSON output.
- **View metrics** in an interactive dashboard with live results, trends, detailed analysis
  and a separate **Release policies** view of strict collection decisions

Perfect for skill authors, platform teams, and developers building AI-powered applications.

The native source projector, root-free capture reconstruction and attempt
inspection support the explicitly selected native collector; they are not
independent collection modes. Retained local evidence is not independently
witnessed execution, server isolation, authenticated assurance or release
authority. Native durable collection is supported on Linux/macOS only; Windows
supplied-reader checks do not certify writing. See
[the native profile and limits](CONTROLLED-COMPARISONS.md#opt-in-native-copilot-collection).

---

## Dependency recovery scenarios

Scenario evals (`scenario` plus exact `schemaVersion: "2.0"`) support finite
MCP and CLI response sequences. They let eval authors test bounded retries,
honest failure reporting, malformed payload handling and prohibited side effects
without replaying a real CLI or server. Unmatched calls and sequence exhaustion
fail closed. A configured timeout error is not transport cancellation.

Run offline preflight without provider credentials:

```bash
go run ./cmd/waza preflight examples/scenarios/recovery/mcp/eval.yaml
go run ./cmd/waza preflight examples/scenarios/recovery/cli/eval.yaml
```

Execution uses the configured Copilot SDK runtime. `command_mocks` intercept
only declared executable names; other commands may use the host `PATH`. MCP
fixtures do not restrict remote/network access. Result dependency modes describe
configuration, and mocked tests do not establish real component reliability.
See the [dependency recovery guide](../site/src/content/docs/guides/dependency-recovery.mdx)
for fixtures and platform/cancellation boundaries.

## Installation

Choose one of three methods:

### 1. Binary Install (Recommended)

The fastest way to get started on macOS, Linux, or Windows Bash environments such as Git Bash, MSYS2, or Cygwin:

```bash
curl -fsSL https://raw.githubusercontent.com/microsoft/waza/main/install.sh | bash
```

The script auto-detects the environment where Bash is running. If you run this from PowerShell and `bash` resolves to WSL, it installs the Linux binary inside WSL. For native Windows, download `waza-windows-amd64.exe` or `waza-windows-arm64.exe` from the newest stable `vX.Y.Z` CLI release on the [Releases page](https://github.com/microsoft/waza/releases), rename it to `waza.exe`, and place it in a directory on your `PATH`.

This downloads the latest standalone `waza` CLI release, verifies the checksum, and installs the `waza` binary to:
- `/usr/local/bin/waza` (if writable), or
- `~/bin/waza` (if `/usr/local/bin` is not writable)

After installation, verify it works:

```bash
waza --version
```

**Note:** If installed to `~/bin`, add it to your PATH:

```bash
export PATH="$HOME/bin:$PATH"
```

### 2. Install from Source

Requires **Go 1.26+, Git LFS, Make, Node.js and npm**. `make build` installs
locked dashboard dependencies and generates embedded assets before Go compilation:

```bash
git clone https://github.com/microsoft/waza.git
cd waza
git lfs install
git lfs pull
make build
```

Verify installation:

```bash
./waza --version
```

### 3. Azure Developer CLI Extension

If you use Azure Developer CLI (`azd`), install waza as an extension:

```bash
# Add the waza extension registry
azd ext source add -n waza -t url -l https://raw.githubusercontent.com/microsoft/waza/main/registry.json

# Install the extension
azd ext install microsoft.azd.waza

# Verify it works
azd waza --help
```

All waza commands are available under `azd waza`:

```bash
azd waza init my-project
azd waza run eval.yaml
azd waza serve
```

---

## Quick Start

Get a complete evaluation suite running in 5 minutes.

For a repository or CLI workflow without a skill, start directly with:

```bash
waza new eval inventory --scenario --template repository
waza run evals/inventory/eval.yaml
```

This selects the real Copilot SDK executor and requires the normal Copilot
runtime/authentication. `--template cli` uses installed Git; `--template mcp`
uses a harness-only mocked MCP dependency, not production-service evidence.
`config.executor: mock` is an offline harness check, never an agent-quality score. See
[sanitized scenario examples](../examples/scenarios/README.md).
Legacy JSON-schema authoring requires `skill` when `scenario` is absent.
Native loading/model validation used by `waza run` historically accepts legacy
default/`1.0`-`1.4` data with no target and preserves existing discovery.
This compatibility is not the recommended authoring path: use a named legacy
target or a scenario `2.0` workflow. Schema `skill` checks length only;
whitespace passes that check but is not a meaningful target.
JSON-RPC `eval.validate` reports schema violations. Separate preflight checks do not
make native loading equivalent to full schema validation or universally
enforce a target.
Scenario evals require explicit `schemaVersion: "2.0"` and a nonempty string
`scenario`. Tasks/results retain `1.x`. Old executables reject eval `2.0`
before execution. Native YAML/JSON parsing rejects present null/non-string/blank
scenario identities, intentionally replacing earlier null acceptance and scalar
coercion. Remove the field for a legacy suite, or use a nonblank string with
exact `2.0`; truly absent fields retain legacy behavior.
Optional `skill` uses existing skill/custom-agent precedence;
without it or explicit directories, ambient discovery is disabled. An empty
task `skill_directories: []` disables discovery; `--no-skills` overrides all
contexts. Fixtures default to the eval's `fixtures/` directory. `inputs.files`
and `instruction_files` resolve from the fixtures/context root, while
`inputs.context.fixture` resolves relative to the eval directory (the repository
template explicitly sets `fixtures/inventory.txt`). Generated outputs remain
relative to each fresh task workspace.
The scenario task stays under `tasks/` when selected by `files.taskGlob`
(including wildcard-directory globs), otherwise in a matching literal
directory; incompatible globs/suffixes fail before writing. The glob must select
only generated tasks, not evals, fixtures or metadata: use `fixtures/*.yaml`
instead of `fixtures/*`, which also selects `.gitkeep`. Disabling
the last eval skill directory does not re-enable ambient discovery.
Whitespace-only optional `skill` values are absent for scenario discovery and
do not import workspace-default skills. Scenario `required_skills` validation
searches only enabled directories; disabled paths, `["*"]` or `none` cannot
satisfy a required skill. Legacy discovery and validation behavior is unchanged.
Scenario generation rejects symlinked parents in the selected output path;
replace them with physical directories or choose a physical `--output` path.
Relative output starts at the retained working directory, and absolute output
is checked from the filesystem root (with verified literal macOS `/var` and
`/tmp` system aliases). Directory handles are retained for writes, so replacing
a parent path cannot redirect creation. A failed creation or write rolls back
files created by that attempt through the retained parents, including a partial
file. Replaced files are preserved and cleanup failures are reported explicitly;
empty directories may remain. Renaming an admitted directory may move its
outputs. This is not a filesystem sandbox or whole-directory transaction, and
other scaffolding commands are unchanged.

The repository template uses three file graders: report outcome, absence of
`private.txt`, and exact final `inventory.txt` bytes. The third grader fails on
changed, missing or differently terminated source content; it does not track
transient writes. CLI/MCP templates retain their two existing graders.

Repository generation includes `fixtures/.gitattributes` to keep inventory LF
bytes through Git checkout, or a safe root `.gitattributes` when a custom task
glob selects local metadata. If both paths are selected or collide with the eval
or task, generation fails before writing: narrow the task-directory glob or
choose a different eval filename. Regenerate older suites to adopt this policy;
attributes alone do not rewrite an existing working copy.
`waza migrate evals/inventory/suite.yml` validates a supported scenario YAML
without changing it. Custom `.yaml`/`.yml` filenames require a present scenario
header with exact schema `2.0`; legacy YAML keeps its `eval.yaml`/`eval.yml`
filename convention, and JSON retains results-artifact recognition.

### Step 1: Initialize a Project

Create a new directory and initialize a waza project:

```bash
mkdir my-eval-suite
cd my-eval-suite
waza init
```

You'll be prompted to create your first skill. Enter a name like `code-explainer`, and the scaffolding is automatic. This creates:

```
my-eval-suite/
├── skills/                     # Skill definitions
│   └── code-explainer/
│       └── SKILL.md            # Skill metadata and description
├── evals/                      # Evaluation suites
│   └── code-explainer/
│       ├── eval.yaml           # Evaluation configuration
│       ├── tasks/              # Test case definitions
│       │   ├── basic-usage.yaml
│       │   └── edge-cases.yaml
│       └── fixtures/           # Test data and resources
│           └── sample.py
├── .github/workflows/
│   └── eval.yml                # CI/CD pipeline
├── .gitignore
└── README.md
```

**Skip the prompt?** Use `--no-skill`:

```bash
waza init --no-skill
```

### Step 2: Create a New Skill (if needed)

If you didn't create one during `init`, add a new skill:

```bash
waza new skill code-analyzer
```

This scaffolds a new skill with SKILL.md and eval suite. The interactive wizard will collect metadata (name, description, use cases).

### Step 3: Define Your Evaluation

Edit `evals/code-explainer/eval.yaml`:

```yaml
name: code-explainer
description: "Tests the agent's ability to explain code"
model: claude-sonnet-4.6
maxTokens: 4096
tasks:
  - id: basic-usage
    description: "Explain a simple Python function"
    fixture: sample.py
    expectedOutput:
      - contains: "function"
      - contains: "parameter"
      - regex: "returns.*value"
validators:
  - type: contains
    caseSensitive: false
  - type: text
    caseSensitive: false
```

Add test case YAML files in `tasks/`:

```yaml
# tasks/basic-usage.yaml
id: basic-usage
name: Basic usage
description: "Explain a simple Python function"
inputs:
  prompt: "Read sample.py and explain what this function does."
  files:
    - path: sample.py
expectedOutput:
  - type: contains
    value: "function"
  - type: text
    value: "returns.*value"
tags: ["basic", "core"]
```

Add fixtures in `fixtures/`:

```python
# fixtures/sample.py
def greet(name):
    """Return a greeting message."""
    return f"Hello, {name}!"
```

### Step 4: Run the Evaluation

Execute the benchmark:

```bash
waza run evals/code-explainer/eval.yaml --context-dir evals/code-explainer/fixtures -v
```

Inspect setup without starting any agent, interpreter, hook, mock server, or live
service first:

```bash
waza preflight evals/code-explainer/eval.yaml --context-dir evals/code-explainer/fixtures
waza preflight evals/code-explainer/eval.yaml --format json > preflight.json
```

Preflight verifies only local schemas/configuration, task discovery/IDs, paths,
locked cached graders, mock matching, references, and static capability support.
Valid unmatched task globs are optional when other patterns discover tasks.
CSV paths must be lexically within the eval directory and remain within it
after resolving symlinks;
ranged CSV fallback IDs/names retain their original 1-based data row number.
The same CSV loader serves execution, preflight, regrading, and run analysis:
`range: [2, 2]` results can be regraded with `waza grade --task row-2`.
Explicit CSV IDs/names and template variables retain their existing precedence.
Command-side CSV loading also rejects resolved symlink escapes while allowing
contained links. An aliased eval base accepts relative contained sources, but an
alternate physical absolute spelling outside its lexical alias root is rejected.
CSV parsing opens through a retained eval-directory root, so a replacement
symlink cannot redirect the final read outside that root. This does not freeze
concurrent file contents or provide universal physical equivalence.
Live MCP settings use the runtime's pure decoder, so malformed headers or
environment maps fail even though live availability remains unresolved.
Model availability, credentials, conditional checkpoint execution, interpreter
syntax, service readiness, and external state are unresolved. Missing/invalid
configuration exits 1; unresolved/unsupported warns and exits 0, or 1 with the
new opt-in `--strict` policy. Existing commands keep their prior exits.
Preflight guards eager argument schemas before eval/task/checkpoint decoding and
before cached-preset or merged-override materialization. External prerequisites
remain unresolved with an incomplete report; literals and returned mock payloads
are not schemas. Later grader schema-file and MCP input-schema checks also mark
unavailable external dependencies incomplete. Runtime schema-loading defaults
remain unchanged. CSV containment accepts filesystem/volume-root eval
directories. File-backed resource paths containing `..` within a cleaned segment
are rejected by the existing runtime loader and diagnosed by preflight; rename
the file or segment. Workspace-relative resource/instruction and file/diff paths
must not clean to the workspace root (`.` or `./`), including inline resource
destinations. Contained absolute CSV paths retain their actual source in task
plans and diagnostics; relative CSV sources still resolve from the eval directory.

Task requirements are optional **descriptions**, not assertions:

```yaml
requirements:
  - id: artifact-present
    category: outcome
    description: The expected artifact exists in the captured workspace.
    checks:
      - scope: task
        grader: artifact-files
  - id: recovery-visible
    category: recovery
    description: Recovery state is observable after the second turn.
    checks:
      - scope: checkpoint
        after_turn: 2
        grader: recovery-state
```

Each reference selects an existing explicit grader by name in its `eval`, `task`,
or `checkpoint` scope. Only checkpoint references accept `after_turn`. IDs are
unique within a task; several requirements may share a check. Empty/absent checks
are uncovered/unresolved. Unknown or ambiguous references are preflight errors,
without changing runtime grader execution. Equivalent valid tool paths remain
allowed unless the chosen existing grader explicitly constrains a sequence.
Text success alone does not establish external resulting state.

Use a build containing #660 for `preflight`; descriptive metadata remains
v1-compatible and old readers may ignore it. This is not a minimum-version
enforcement mechanism. Preflight reports use `kind: waza.preflight` and their own
`schemaVersion: "1.0"`; they are not `results.json`, and inventory `complete`
does not mean requirements are satisfied. See the
[preflight guide](https://microsoft.github.io/waza/guides/preflight/) for diagnostics
and path-resolution rules.

If your eval uses remote grader presets, resolve them first:

```bash
waza get evals/code-explainer/eval.yaml
waza run evals/code-explainer/eval.yaml --context-dir evals/code-explainer/fixtures -v
```

Remote refs use `<host>/<owner>/<repo>[/path][#export]@<version>` in `graders[].ref`. `waza get` downloads the module into `~/.waza/cache/{host}/{org}/{repo}/{sha}/` and writes `waza.lock`; `waza run` uses the lock and refuses missing or digest-mismatched cache entries.

**Output:**
- `✓ Passed` — Task passed all validators
- `✗ Failed` — Task failed one or more validators
- Summary: `X passed, Y failed`

Save results to a JSON file:

```bash
waza run evals/code-explainer/eval.yaml \
  --context-dir evals/code-explainer/fixtures \
  -o results.json
```

### Step 5: View Results in the Dashboard

Launch the web dashboard:

```bash
waza serve
```

The browser opens automatically to `http://localhost:3000`. You'll see:

- **Dashboard Overview** — Run history, pass rate, model comparison
- **Run Details** — Individual task results, validation output
- **Compare** — Side-by-side model performance
- **Trends** — Pass rate over time

---

## Command Reference

### `waza runner` (source-build preview)

`waza runner capabilities` advertises implemented versions and capabilities.
`waza runner negotiate < examples/runner/negotiation.json` checks requirements
through JSON stdin/stdout without execution.
`waza runner validate < examples/runner/unit.json` validates one captured tuple;
`waza runner run < examples/runner/unit.json` executes that exact mock/text attempt
once, with no discovery, cache or automatic retries. Native execution is not
implemented in this bounded profile. The HTTP API shares admission/execution;
live Lab displays pre-execution selection validation and input-derived logical
identity lineage, with a validation JSON export. CLI stdout can be saved using
`waza runner validate < examples/runner/unit.json > validation.json`.
Identity version `sha256-waza-c14n-v1` names input, not observed execution;
older receipts without it remain unavailable. See the
[identity guide](../site/src/content/docs/guides/runner-identities.mdx) and
[canonical contract and migration guide](../site/src/content/docs/guides/runner-contract.mdx).

### `waza init [directory]`

Initialize a waza project with directory structure.

**Flags:**
- `--no-skill` — Skip the first-skill creation prompt

**What it creates:**
- `skills/` — Skill definitions directory
- `evals/` — Evaluation suites directory
- `.github/workflows/eval.yml` — CI/CD pipeline
- `.gitignore` — With waza-specific entries
- `README.md` — Getting started guide

**Example:**
```bash
waza init my-skills --no-skill
```

---

### `waza new skill [skill-name]`

Create a new skill with its evaluation suite. In interactive mode, the wizard collects spec-aligned metadata: name, description, trigger phrases, and anti-trigger phrases.

**Flags:**
- `--template, -t` — Template pack (coming soon)

**Modes:**

**Project mode** (inside a `skills/` directory):
```bash
cd my-skills-repo
waza new skill code-explainer
```
Creates `skills/code-explainer/SKILL.md` and `evals/code-explainer/`.

**Standalone mode** (no `skills/` directory):
```bash
cd my-project
waza new skill my-skill
```
Creates `my-skill/` with `SKILL.md`, `evals/`, CI/CD pipeline, and README.

**Examples:**
```bash
# Interactive wizard in a terminal
waza new skill code-analyzer

# Non-interactive (CI/CD)
waza new skill code-analyzer << EOF
Code Analyzer
Analyzes code for patterns and issues
code, analysis
EOF
```

---

### `waza new task from-prompt <prompt> <task-path>`

Record a prompt execution and generate a reusable task YAML from the observed run.

The generated task includes inferred validators based on:
- Assistant response content
- Tool usage sequence
- Skill invocation events

**Flags:**
- `--model` — Model used for recording (default: `claude-sonnet-4.5`)
- `--testname` — Test ID/name to write to task YAML (default: `auto-generated-test`)
- `--tags` — Comma-separated tags added to the generated task
- `--timeout` — Prompt execution timeout (default: `5m`)
- `--overwrite` — Replace output file if it already exists
- `--root` — Root directory used for skill discovery

**Examples:**
```bash
# Create a task from one recorded prompt run
waza new task from-prompt "Refactor this function for readability" evals/code-explainer/tasks/refactor-readability.yaml

# Add task metadata and overwrite if needed
waza new task from-prompt "Explain this diff and risks" evals/code-explainer/tasks/diff-analysis.yaml \
  --testname diff-analysis \
  --tags recorded,regression \
  --overwrite
```

### Agent-assisted eval authoring

The [Writing Eval Specs guide](https://microsoft.github.io/waza/guides/eval-yaml/)
can be used as direct instructions for a coding agent. It provides:

- Required files to inspect before editing
- Schema and task invariants
- Separate create and update workflows
- A test-first loop for implementation changes
- Guidance for fixtures, MCP mocks, and grader selection
- A targeted-to-full validation ladder
- A reusable prompt for delegating eval implementation

When reviewing agent-authored evals, require stable task IDs, focused changes,
coverage for changed behavior, and the exact verification commands with their
results.

---

### `waza run [eval.yaml | skill-name]`

Run an evaluation benchmark.

**Arguments:**
- `[eval.yaml]` — Path to evaluation spec file
- `[skill-name]` — Skill name (auto-detects eval.yaml)
- `[agent-name]` — Custom agent name (auto-detects eval.yaml for `.agent.md` files)
- *(none)* — Auto-detect using workspace detection

**Flags:**
- `--context-dir` — Fixtures directory (default: `./fixtures`)
- `--output, -o` — Save results JSON
- `--verbose, -v` — Detailed progress output
- `--parallel` — Run tasks concurrently
- `--workers <n>` — Number of concurrent workers (default: auto)
- `--task <pattern>` — Filter tasks by name (glob pattern, can repeat)
- `--tags <pattern>` — Filter tasks by tags (glob pattern, can repeat)
- `--model <model>` — Override model (can repeat for multi-model runs)
- `--cache` — Enable result caching
- `--no-cache` — Disable caching (default)
- `--cache-dir <path>` — Cache directory (default: `.waza-cache`)
- `--format <format>` — Output format: `default`, `github-comment`
- `--interpret` — Print plain-language interpretation of results
- `--baseline` — Run A/B comparison: with skills vs without

**Examples:**

Run an evaluation from a spec file:
```bash
waza run evals/code-explainer/eval.yaml --context-dir evals/code-explainer/fixtures -v
```

Run a specific skill:
```bash
waza run code-explainer
```

Auto-detect in workspace:
```bash
# Single-skill workspace → runs that skill's eval
# Multi-skill workspace → runs all evals with summary
waza run
```

Run specific tasks:
```bash
waza run evals/code-explainer/eval.yaml --task "basic*" --task "edge*"
```

Run with specific models:
```bash
waza run evals/code-explainer/eval.yaml --model "gpt-4o" --model "claude-sonnet-4.6"
```

Save results:
```bash
waza run evals/code-explainer/eval.yaml -o results.json
```

---

### `waza dev <skill-name | skill-path>`

Iteratively score and improve a skill's frontmatter. A single target can be a
workspace skill name, a directory, or a `SKILL.md` file. Both relative and
absolute paths are supported.

```bash
waza dev code-explainer
waza dev ./skills/code-explainer --target high
waza dev ./skills/code-explainer/SKILL.md
waza dev SKILL.md
waza dev .
```

On Windows, native paths work too:

```powershell
waza dev .\skills\code-explainer\SKILL.md
```

Bare names resolve workspace skills first and fall back to existing local
directories only when no skill matches. Use `./directory` (or `.\directory` on
Windows) to select a directory explicitly when its name collides with a skill.
Files other than `SKILL.md` are rejected.

These target forms also work with `--copilot` for a non-mutating recommendations
report and `--scaffold-triggers` to generate `tests/trigger_tests.yaml`.
Multiple positional arguments remain workspace skill names; use `--all` to
process every skill in the workspace.

---

### `waza check [skill-name | skill-path]`

Validate that a skill is ready for submission.

**Arguments:**
- `[skill-name]` — Skill name (e.g., `code-explainer`)
- `[skill-path]` — Relative or absolute path to a skill directory or `SKILL.md` file
- *(none)* — Auto-detect using workspace detection

Explicit paths resolve evals from the target skill's workspace, independent of
the current directory. The target's `.waza.yaml` controls eval paths and filenames.
Without configuration, discovery searches up to 10 directory levels for
`evals/<skill-name>/eval.yaml`, then checks `<skill-dir>/evals/eval.yaml` and
`<skill-dir>/eval.yaml`, in that order.

**What it checks:**
1. **Compliance scoring** — Validates SKILL.md frontmatter (Low/Medium/Medium-High/High)
2. **Token budget** — Ensures SKILL.md is within limits
3. **Evaluation presence** — Confirms eval.yaml exists

**Output:**
- ✓ Compliance level (e.g., "Medium-High")
- ✓ Token count / limit
- ✓ Evaluation status
- Suggestions for improvement

**Examples:**

Check a specific skill:
```bash
waza check code-explainer
```

Check by path:
```bash
waza check skills/my-skill
```

Auto-detect in workspace:
```bash
# Single skill → checks that skill
# Multiple skills → summary table for all
waza check
```

---

### `waza serve`

Launch the web dashboard to view evaluation results.

JSON-RPC `eval.list` and MCP `waza_eval_list` retain canonical
`eval.yaml`/`eval.yml` discovery and also list native-loadable scenario
`.yaml`/`.yml` files with configured/custom filenames. Classification and native
loading determine eligibility, not full authoring-schema or task validation.
Custom absent or classification/loading-invalid scenario, task and unrelated
YAML stays unlisted; `eval.get`/`eval.validate` provide diagnostics.
The existing caller-root traversal and canonical invalid-file fallback remain
unchanged; this is not a containment guarantee or a dashboard result change.

**Flags:**
- `--port <n>` — HTTP server port (default: `3000`)
- `--no-browser` — Don't auto-open the browser
- `--lab` — Open the experimental real-results dashboard at `/#/lab` (cannot combine with `--tcp`)
- `--results-dir <path>` — Directory to read results from (default: `.`)
- `--tcp <address>` — JSON-RPC TCP server (e.g., `:9000`)
- `--tcp-allow-remote` — Bind to all interfaces (default: loopback only)
- `--http` — Start HTTP dashboard (default)

**Dashboard Pages:**
- **Overview** — Run history, pass rates, model comparison
- **Run Details** — Individual task results, validation logs, timestamps
- **Compare** — Side-by-side model performance
- **Trends** — Pass rate and timing trends over time
- **Live View** — Real-time results during active runs

**Examples:**

Start the dashboard:
```bash
waza serve
```

Custom port:
```bash
waza serve --port 8080
```

Load results from a specific directory:
```bash
waza serve --results-dir ./archived-runs
```

Try the experimental dashboard without replacing the classic dashboard:
```bash
waza serve --lab --results-dir ./archived-runs
```
The classic dashboard remains at `/#/`. The lab connects filters, history,
weighted-score comparisons, recorded task evidence, and JSON/CSV/print reports.
It refreshes local results every 15 seconds; activity is saved-result event replay,
not live worker monitoring or execution control. Missing readiness, coverage,
and git revisions are unavailable. `/#/lab/demo` is an explicitly synthetic
demo, never a fallback for failed API requests.
Successful refreshes update mounted task/comparison evidence after same-ID
regrading. Failed refreshes preserve cached artifacts. Omitted/null weighted
scores remain unavailable, while explicit zero is recorded. Candidate selection
remains available without a comparable baseline. Permanently closed activity
streams require **Reconnect stream** rather than claiming automatic retry.
The configured local storage adapter refreshes too. Incomplete rewrites of known
artifacts fail refresh without discarding cached evidence; successful scans still
reflect intentional deletion and ignore unrelated malformed new files.
Missing directories remain empty while no artifacts have been admitted. New
unrelated inaccessible paths are skipped; root and admitted-path failures remain
explicit. Nonempty remote archives refresh on demand rather than every 15
seconds. Evidence tabs support arrow/Home/End navigation, and nullable task or
grader lists normalize to empty evidence in inspectors and exports.

Don't auto-open browser:
```bash
waza serve --no-browser
```

JSON-RPC TCP server (for IDE integration):
```bash
waza serve --tcp :9000
```

---

## Advanced Usage

### Multi-turn conversations

Use `inputs.follow_up_prompts` for a fixed sequence of user messages, or
`inputs.responder` for a simulated user that answers the agent's questions.
Do not configure both in one task. Scripted follow-ups reuse the actor session;
an adaptive responder has a separate persistent classification session.

The [complete two-question example](../site/src/content/docs/guides/eval-yaml.mdx#complete-multi-turn-example)
includes runnable eval/task YAML, preflight, a scripted offline control, and
authenticated real-execution commands. Adaptive runs can incur usage for both
models. A responder's `stopped` outcome does not imply the graders passed;
`abstained` and `cap_exhausted` describe different incomplete conversations.

If responder execution fails without a decision, the run's error and responder
reason retain the available execution error. See the example's error guidance
to distinguish execution failures from successful output without a decision.

### Controlled Dependency Failures

MCP mock `error` responses and command mock `stderr`/`exit_code` responses can model permanent failures or permission denial without contacting the dependency. Legacy responses return the first matching fixture on every call; scenario evals can instead opt into finite recovery sequences. Configured unmatched calls fail closed, and MCP unmatched diagnostics omit argument values. The MCP mock subprocess accepts per-request cancellation without resetting consumed steps or terminating the server. Finite MCP sequence progress spans every turn of one task attempt and resets for each task, trial, and retry. See the [dependency recovery guide](../site/src/content/docs/guides/dependency-recovery.mdx) for a multi-turn example, cleanup limitations, dispatch, interruption, and evidence boundaries.

Use existing graders and checkpoints to check honest failure reporting and prohibited effects. A mocked interaction or responder message is not proof that an external operation occurred. Undeclared commands remain available through the host `PATH`, and command shim interception does not isolate the filesystem or network. See the [eval guide](../site/src/content/docs/guides/eval-yaml.mdx#choosing-a-test-boundary) for choosing mocked versus live dependencies.

### Baseline Grader Challenges

Contributors can run the fixed synthetic challenge corpus without an agent,
workflow service, model or credentials:

```bash
NO_COLOR=1 go test ./internal/graders -run '^TestBaselineChallenge' -count=1
```

The corpus contains 12 scenarios across MCP, CLI and repository state, each
with good, alternative-valid and two bad candidates. The same existing graders
accept different valid tool paths and inspect actual temporary workspace state;
identical final success prose is not the correctness oracle.

These candidate labels are not independently human-reviewed, and MCP/CLI state
is synthetic rather than proof of a live external operation. Missing required
evidence is insufficient evidence, not a correct bad-case rejection establishing
assurance. The test-local checks do not change `run`, `grade`, golden tasks,
defaults or exit codes. See [Baseline Grader Challenges](GRADER-CHALLENGES.md)
for the matrix, runnable command and evidence limitations.

### Strict preserved-file assurance

For native selected tool-event and workspace evidence, the separate internal
`assurance.VerifyPreserved` API and `assurance.ParsePreservedReport` select
report `1.2`; default CLI/report/dashboard flows do not. Native origin,
artifact completeness, raw argument presence and actual grader agreement are
required. File subsets cannot prove absence, unknown historical capture remains
unknown, and calibration is always not selected with null billing. See the
[preserved-native scope and limits](GRADER-CHALLENGES.md#separately-selected-preserved-native-observations).

`waza assure eval.yaml --references labels.json` observes native file graders
against complete unredacted preserved files, without a task agent or model.
Finite authored output can separately challenge native text and inline-schema
graders; its exact supplied bytes never establish historical execution or billing.
It emits standalone `waza.grader-assurance` JSON with scoped requirements,
actual verdicts/scores/feedback, declared and observed coverage, binding domains
and supplied-review eligibility. The new command exits 1 unless strict
finite-corpus assessment passes; existing `grade` and golden behavior is unchanged.

Labels use `schemas/grader-reference-1.0.schema.json`; a separate
`schemas/grader-review-1.0.schema.json` declaration needs `--review` and explicit
`--accept-review-source`. Original label bytes, resolved native declarations,
source bytes and observed on-disk executable-path bytes are distinct binding
domains. The executable digest reads the file opened at `os.Executable()` at
observation time; it does not certify the mapped image, running RAM,
authenticity or historical execution. Replacement can change that observation
or make acquisition fail; missing applicable provenance cannot pass.
Reports follow `schemas/grader-assurance-1.0.schema.json`. A case selects exactly
one historical `snapshot` or `authored_input` with rooted path and external
SHA-256 of the original authored envelope bytes.
Authored envelopes require exact outer `schemaVersion: "2.0"` and
`kind: "waza.grader-reference-input"` compatibility fences plus `payload` and
`evidence`. These markers prevent native historical import, not upgrade native
snapshot/result schemas; the inner payload remains `1.0`, evidence `1.1`.
Missing applicable provenance, unreviewed labels, absent required evidence and
operational failures cannot pass. A subset of captured files cannot establish
absence. Any wrongly accepted targeted critical negative independently fails,
regardless of domain agreement. These are finite corpus checks, not confidence.

Legacy `assure --calibrate` remains unavailable: it makes zero paid calls and
reports not assessed, with null usage/credits. Bundled candidates have not had
actual human review. See [the contract and support boundary](GRADER-CHALLENGES.md#strict-file-content-assurance).

The [authored-output example](../examples/grader-challenges/authored-output/README.md)
generates four finite evaluator-only inputs against a selected binary. Its native
grader outputs agree with the candidate labels, but the expected result remains
exit 1/not assessed without human review. The dashboard's separate **Assurance**
view inspects the report locally without uploading it, rechecking its supplied
claims or associating it with historical runs.

The separate evaluator API `assurance.Calibrate` requires explicit selection,
a bound local rubric root, a construction-only engine factory and a successful
paid-call notice before any initialization. It has no default factory.
The separate `waza assure calibrate` command explicitly installs an owned factory
and requires `--references`, `--review`, `--accept-review-source` and
`--rubric-root`. Without `--accept-paid-calls` its notice refuses execution,
including noninteractive use; there is no prompt or generic `--yes` fallback.
The reviewed model, admitted job count and count-versus-spend warning are printed
before construction. A positive `--timeout` defaults to 10m for cooperative
assessment, not forced cleanup or a spending cap. Existing
`waza assure --calibrate` still makes zero paid calls. Its
version-1.1 reports require explicit inspector selection and show supplied judge
lifecycle/model/accounting claims. See the
[calibration API boundary](GRADER-CHALLENGES.md#explicit-evaluator-calibration-api)
for supported modes, fixed-domain criteria, ownership and missing/error states.

### Existing-workflow compatibility examples

The [recorded compatibility examples](../examples/compatibility/README.md) cover
MCP lookup, read-only CLI inspection, and repository artifact preservation, each
with known-good, deliberately-bad, and alternative-valid outcomes. They grade
fixed evidence offline rather than executing an agent or claiming assurance.
Contributor integration gates, fixture paths, feature ownership, historical
artifact policies and known-bug boundaries are in
[COMPATIBILITY.md](COMPATIBILITY.md).

### Caching and Reproducibility

Scenario eval `2.0` bypasses result caching with an explicit notice, even with
`--cache`, because external dependencies are not fully fingerprinted. The cache
behavior below applies to legacy `1.x` suites.

Cache evaluation results to avoid redundant runs:

```bash
# Enable caching (results stored in .waza-cache/)
waza run evals/code-explainer/eval.yaml --cache -o results.json
```

The cache stores results by task ID and model, keyed on the prompt and expected output. Same inputs = same cached results. MCP-backed task keys also include the resolved mock configuration, so changed inline or eval-relative fixture responses run again. Tasks without MCP mocks retain their existing cache keys.

**Use cases:**
- Development — Iterate on validators without re-running tasks
- CI/CD — Avoid redundant API calls for unchanged evals
- Offline — Use cached results when connection is unavailable

Disable caching:
```bash
waza run evals/code-explainer/eval.yaml --no-cache
```

Specify cache location:
```bash
waza run evals/code-explainer/eval.yaml --cache --cache-dir ./my-cache
```

### Registry Grader Presets

Use registry commands to discover shared grader presets and add them to an eval without copying full grader definitions by hand:

```bash
waza registry search factual --kind grader
waza registry add github.com/waza-evals/fact#factuality@v1.0.0 \
  --eval evals/code-explainer/eval.yaml \
  --name factuality_strict \
  --set config.threshold=0.9
```

`waza registry search` queries configured index sources. By default, Waza searches the public `https://github.com/waza-evals` source. Project `.waza.yaml` files can define a top-level `registries:` list with named sources, and `--registry <name>` limits search to one of them. The current implementation returns bundled sample metadata while live registry index integration is pending.

`waza registry add` appends a `ref:` grader entry to `eval.yaml`, resolves the ref with the shared remote grader resolver, and updates `waza.lock` with the pinned commit SHA and content digest. Remote executable program graders require `--allow-exec` or interactive confirmation because they can run local commands.

---

### Testing Against a Local Git Repo

When you are developing skills inside the same repository the skills are meant to operate on (for example, `eng/` tooling in `azure-sdk-for-rust`), you can have each task start with a clean checkout of that repo materialized into its workspace:

```yaml
# tasks/repo-aware.yaml
id: repo-aware
name: Repo-aware task
inputs:
  prompt: "Explain how the eng/ tooling builds release packages"
  workdir: my-repo
  repos:
    - type: worktree
      source: /Users/me/code/azure-sdk-for-rust
      commit: main
      dest: my-repo
expected:
  output_contains: ["eng/"]
```

The `worktree` strategy uses `git worktree add --detach` against the local source clone — it shares the source repo's `.git` object store (so it's cheap, no network) and uses `--detach` so branch/tag names won't conflict with the source repo's current checkout. Worktrees are removed via `git worktree remove --force` when the engine shuts down. See `## Git Repository Resources` in the README for the full field reference.

For non-git fixture workspaces, set `inputs.context.fixture` to a file or directory relative to the eval spec directory. Directory fixture contents are copied into the fresh task workspace before the agent runs:

```yaml
inputs:
  context:
    fixture: fixtures/demo
  prompt: "Inspect repository files and summarize what you find."
```

---

### Skill Body Injection

By default, an eval with `skill: <name>` injects the target `SKILL.md` or `.agent.md` body into the agent system prompt. The injected `<skill_context>` block starts with `Base directory for this skill: <absolute path>`, so relative links in the body (for example `scripts/install.sh` or `assets/templates/`) resolve from the skill's or agent file's own directory even though the agent runs in a temp workspace. Set `config.inject_skill_body: false` to skip the injection; see `### Skill Body Injection` in the README for details.

---

### Filtering and Selective Runs

Run only specific tasks:

```bash
# By task name (glob patterns)
waza run evals/code-explainer/eval.yaml --task "basic*" --task "edge*"

# By tag
waza run evals/code-explainer/eval.yaml --tags "critical"
```

**Task names and tags** are defined in your task YAML files:

```yaml
# tasks/basic-usage.yaml
id: basic-usage
description: "Explain a simple function"
tags: ["basic", "core"]
```

---

### Parallel Execution

Run tasks concurrently to speed up evaluation:

```bash
# Use auto-sized workers
waza run evals/code-explainer/eval.yaml --parallel

# Use custom worker count
waza run evals/code-explainer/eval.yaml --parallel --workers 8
```

**When to use parallel execution:**
- Many tasks (50+)
- Long-running validators
- Development machines with spare CPU

---

### Multi-Model Comparison

Run the same evaluation against multiple models:

```bash
waza run evals/code-explainer/eval.yaml \
  --model "gpt-4o" \
  --model "claude-sonnet-4.6" \
  --model "gpt-4-turbo"
```

Results are grouped by model. Compare them in the dashboard or with:

```bash
waza compare results-gpt4.json results-sonnet.json
```

---

### CI/CD Integration

Waza's own repository checks use locked frontend dependencies. `Site Build` runs the full site Chromium suite, including preflight, compatibility, mobile-menu and screenshot tests. `Dashboard Build and Test` runs lint, a production build and the full dashboard Chromium suite when dashboard files or its workflow change. Both suites disable retries in CI and upload test/screenshot evidence; the dashboard artifact also retains the rebuilt embed inputs. PRs without dashboard changes report an explicit skip, not frontend qualification. After the dashboard workflow is available on the default branch, `workflow_dispatch` can qualify a selected branch even without dashboard file changes. These frontend checks do not replace Go, compatibility or native-platform gates.

Run waza as part of your GitHub Actions workflow:

```yaml
# .github/workflows/eval.yml
name: Evaluate Skills

on: [push, pull_request]

jobs:
  evaluate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v4
        with:
          go-version: '1.26'
      - name: Install waza
        run: |
          curl -fsSL https://raw.githubusercontent.com/microsoft/waza/main/install.sh | bash
      - name: Run evals
        run: |
          waza run --output results.json -v
      - name: Comment PR
        if: github.event_name == 'pull_request'
        uses: actions/github-script@v7
        with:
          script: |
            const fs = require('fs');
            const results = JSON.parse(fs.readFileSync('results.json'));
            github.rest.issues.createComment({
              issue_number: context.issue.number,
              owner: context.repo.owner,
              repo: context.repo.repo,
              body: `Evaluation Results: ${results.passCount}/${results.totalCount} passed`
            });
```

---

### Session Logging

Capture detailed session logs for debugging:

```bash
waza run evals/code-explainer/eval.yaml --session-log --session-dir ./logs
```

For custom agents evaluated with `copilot-sdk`, each `run_complete` log event includes the session digest's effective `tool_policy_mode` and any `tool_policy_denials`. The same fields are saved in results schema 1.3 and displayed in the dashboard trajectory view. Policies follow the selected agent and task-level skill paths, including resumed turns; omitted `tools:` is unrestricted, `tools: []` denies all tools, and populated lists allow only declared names and documented aliases. See the [custom agents guide](../site/src/content/docs/guides/custom-agents.mdx) for details and sandbox limitations.

Logs are stored in NDJSON format (one event per line):
```json
{"event":"task_started","id":"basic-usage","timestamp":"2024-01-15T10:30:00Z"}
{"event":"task_completed","id":"basic-usage","passed":true,"timestamp":"2024-01-15T10:30:05Z"}
```

---

### Output Formats

**Default format** (human-readable):
```bash
waza run evals/code-explainer/eval.yaml
```

**GitHub Comment format** (for PR comments):
```bash
waza run evals/code-explainer/eval.yaml --format github-comment
```

**JSON output** (for automation):
```bash
waza run evals/code-explainer/eval.yaml -o results.json
```

---

## Snapshot inspection and regrading

### Frozen independent prompt judging

Final eval/task prompt graders may opt into `config.evidence_context: {}` for
recorded task, visible conversation, canonical tools/results and output, with
optional exact `files` and reviewed `evaluator_only` exclusions. The fresh judge
has neither the actor session nor its live workspace. Omission preserves legacy
behavior; checkpoint, pairwise, continuation and `--evidence-snapshot` combinations
are rejected. This requires the current implementation, not an older CLI that
does not implement evidence context.

```bash
waza preflight examples/evidence-judge/eval.yaml --context-dir examples/evidence-judge/fixtures
waza run examples/evidence-judge/eval.yaml --context-dir examples/evidence-judge/fixtures --output results.json
waza grade examples/evidence-judge/eval.yaml --results results.json --task report --output regraded.json
```

Provide an explicit hosted token or BYOK environment configuration; stored login,
ambient actor config, hooks and skills are not inherited. The selected-file path
requires Linux/macOS, with 64 files, 64 KiB/file and 256 KiB/compact JSON bundle
limits. Formatting whitespace outside strings does not count toward that limit.
Missing/incomplete/redacted evidence fails before model startup, without
truncation or workspace recovery. Evidence-only regrading does not inspect
`--workspace` or `snapshot_path`. It uses the recorded task, even if the current
task changed, and newly selected files must already have been captured.
Current eval/task selection unions and evaluator-only exclusions are checked
again before any saved-evidence judge runs. Omitted judge models fall back to
the eval model (then `auto`); explicit hosted reasoning effort must pass the
isolated client's model-catalog checks before session creation.
Valid, run-bound legacy snapshot manifests are retained as provenance only:
old validations, result, grader-configuration and checkpoint artifacts become
unavailable for the new assessment. Invalid or mismatched unused manifests are
omitted with a warning; the frozen judge bundle is validated independently.

Ordinary failed judgments retain `grade`'s zero exit and report `passed: false` /
`status: failed`. Operational errors return nonzero and `status: error`, without
an `overall_score` in stdout. After evidence grading starts, requested output
retains safe failure diagnostics; publication errors propagate. Regrading can
incur new model charges, recorded once per session in `regrade_usage.sessions`;
this field covers the current regrade only and is omitted for a subsequent
mechanical-only regrade. In a mixed-task regrade, a later mechanical grader's
operational failure still publishes earlier judge observations and current
charges to the requested output, while withholding the overall score.
If verbose diagnostic output fails after judging, the saved observations and
charges are retained and the affected run is marked as an error.
Mechanical errors remain fail-fast and do not start any further judges;
this also applies within a task that mixes mechanical and evidence graders,
regardless of validator order. An error before the first evidence attempt
creates no new judge session.
Local evidence-admission and diagnostic-output errors also stop further judges;
prompt-execution failures can still aggregate across independent tasks.
Original `evaluation_usage` and actor billing remain
unchanged. Missing credits are unavailable, not zero. The existing Evidence view
reports per-run/per-grader submission metadata, not proof of inspection, accuracy,
or authenticity.
See the [canonical reference](https://microsoft.github.io/waza/guides/graders/#evidence-backed-independent-judging)
for complete YAML, authentication, native-runtime scope and privacy limits.

### Snapshot inspection

```bash
waza run eval.yaml --output results.json --snapshot ./snapshots/
waza replay ./snapshots/my-task-run1.json
waza grade eval.yaml --results results.json --workspace ./preserved-agent-workspace
```

Replay checks stored event sequencing and grader score/pass consistency without contacting an agent. It does not re-run graders, reconstruct files, or verify external state. Fixture and instruction hashes identify bytes but do not preserve them; file-based grading needs the actual agent workspace. Prompt graders may call the configured judge model. Live replay is not implemented.

Portable evidence (manifest `1.0`, snapshot `1.1`) keeps missing-version defaults
for legacy eval/results readers at `1.4`, while newly generated results use result
schema `1.7`. Task YAML has no independent schema-version field or default.
It describes every trial, full source origin, source/content
digests, availability, completeness, redaction, unavailable versions, requested
`no_skills`/native skill control, and operational uncertainty. Requested
controls are not SDK enforcement proof. Cached trials retain source origin;
historical absence remains unassessed.
Raw manifest JSON must satisfy the complete embedded evidence schema before
decoded metadata can be assessed; missing fields and invalid null scalars are
withheld, not repaired into zero values. Schema-declared nullable fields remain valid.
Invalid metadata is withheld. Scoped requirement checks are existing grader
observations, not enforcement, causal explanations, new scores, or changed defaults.
API, interpretation reports and dashboard views inspect metadata without fetching
snapshot bytes. Their requirement references identify captured artifact metadata;
artifact-relative value pointers are unverified and withheld, with an explicit
limitation. Explicit snapshot-content verification independently checks digests
and pointer existence when the actual bytes are supplied.
Checkpoint observation labels include their declared `after_turn`, so the same
grader's observations at different turns remain distinguishable in `--interpret`.
Interpretation also lists each captured-artifact reference's ID and source origin
as metadata only, without claiming verified values. Present zero-valued source
run/attempt fields mean unavailable attribution; the dashboard labels them
unavailable without copying the selected result's lineage. Nonzero mismatches
remain withheld, and omitted/null required fields remain invalid.
Requirements with omitted, null, or empty check lists remain visible as unassessed
in interpretation; populated sibling observations are retained.
Finite authored inputs select a separate manifest schema `1.1`; native execution
evidence remains `1.0`. Both raw schema profiles reject malformed required fields
without resealing them, and authored-input manifests cannot acquire executed or
cached-run attribution.

Snapshot capture sanitizes typed JSON and structurally sensitive keys before
serialization without modifying original results. Unsafe keys/identifiers,
opaque bytes, duplicate/trailing JSON, excessive depth, unsupported values, and
invalid policies fail safely. Numeric lexical tokens are retained. Environment
capture is default-deny; custom redaction replaces defaults unless `extend: true`.
Pattern matching cannot certify confidentiality: inspect before sharing. Rule
counts include selected files; normalization-only changes are not secret matches.

New captures report each artifact's `redacted` flag from its own materialized
content changes, independently of the capture-wide match count. Validation
changes also mark their enclosing result, but metadata, environment and file
changes do not contaminate unchanged siblings. Core JSON representation-only
normalization and no-op replacements are not changes; removal of a materialized
field is. Historical flags remain as recorded.

```bash
# Select only reviewed agent-visible UTF-8 regular files (Linux/macOS)
waza run eval.yaml --snapshot ./snapshots --snapshot-file report.txt \
  --snapshot-evaluator-only private-rubric.txt \
  --snapshot-max-file-bytes 1048576 --snapshot-max-total-bytes 10485760 \
  --output results.json

# Narrow file-only regrading from an explicitly reviewed snapshot
waza grade eval.yaml --results results.json --task report-task \
  --evidence-snapshot ./reviewed-snapshot.json --output regraded.json
```

Repeatable selected files/exclusions require `--snapshot`; both byte limits must
be positive (defaults: 1 MiB/file, 10 MiB total). Credentials and evaluator-only
files must never be captured. Allow-listing is not authorization proof: review
agent visibility and exclusions. Selected files use private directories/files
and randomized non-overwriting names; ordinary no-file snapshot defaults remain.
Partial capture and capture/publication failure have safe missing/unavailable
states. Capture adds no path fetching or export/upload. Existing explicitly
configured result storage still uploads results, including evidence metadata,
but does not fetch or upload separate snapshot files.

Immediate capture-to-write uses private publication for any nonempty selected-file
request, including zero preserved files. The internal request marker is not
serialized; loaded/manual zero-file snapshots keep historical writer routing.
Failure evidence retains policy-validated eval/task identity when available;
otherwise it records unavailable attribution with numeric lineage only.
This does not sanitize or replace original execution result fields.
The dashboard and `--interpret` report missing source identifiers as unavailable;
they do not substitute the displayed evaluation or task identity.
Requirement observations may reference captured artifacts, including explicitly
partial or redacted captures, but not unavailable, unrequested, or digest-only
artifacts. A resolvable metadata identity does not establish observed content.

Before selected content is read, metadata-only exclusion admission permits at
most 256 entry inspections and depth 32, including descendants and repeated
inspections. Limit, inspection and resource failures prevent capture; no silent
pruning or descriptor-limit increases are attempted. Unrelated hard links and
missing-exclusion neighbors remain eligible. Root/child admission rejects
symlinks except the explicitly validated macOS `/var` and `/tmp` system aliases.
Identity and before/after metadata checks do not freeze concurrent same-user
directory membership or certify atomic/authentic state. A late close failure
after verified publication retains a residual path with unavailable evidence.
Private-file cleanup failure can similarly retain one independently verified
complete residual, including a hidden temporary file, with an explicit error
and unavailable capture evidence. Additional possible residue is reported
separately; one path cannot enumerate it. Partial/changed bytes and unverified
moved-root locations do not become complete snapshots or cleanup success.

Selected-state destination publication on Linux/macOS uses component-wise
no-follow creation/admission, including intermediate directories. Missing
directories are created `0700`; existing ancestors need not be private, but the
final directory must be `0700`. Identity rechecks before evidence writes and
before/after linking never recreate missing directories. Failures may leave
newly created empty directories; these observations do not protect a moving
same-user namespace. Ordinary metadata-only snapshot output is unchanged.

Evidence-snapshot regrading on Linux/macOS requires `--task`, exactly one assessed matching run,
no explicit `--workspace`, original manifest/full-origin verification, and
complete unredacted file text/digests. Only explicit required file-grader
`must_exist`/`content_patterns` inputs are privately materialized. It rejects
all non-file graders, output-based expectations, absence/wildcard checks, and
file graders without explicit required paths. Checks cover only the materialized
captured subset: `must_not_match` checks a selected file's complete verified text,
not absence from the original workspace.
Regrading keeps `source_manifest_sha256` and invalidates
original result/validation/grader-configuration/checkpoint links. Older workspace
grading is unchanged; no extra automatic model calls are introduced.
Capture byte limits are not reapplied to independently supplied snapshots during
workspace verification/materialization; review their sizes before selection.
Expected materialized bytes and observed metadata are checked before workspace
return; integrity after that point is not certified.
The [task authoring example](../site/src/content/docs/guides/eval-yaml.mdx#link-intent-to-existing-checks)
shows how descriptive `requirements` reference existing scoped graders without
adding assertions or changing pass/fail.

See the [complete evidence guide](../site/src/content/docs/guides/snapshot-replay.mdx)
for hash canonicalization, completeness limits, and a runnable mock example;
the [dashboard Evidence tab](../site/src/content/docs/guides/dashboard.mdx#portable-evidence)
shows every trial without local-file fetching.

## Dashboard

The `waza serve` command launches an interactive web dashboard for viewing and analyzing evaluation results.

AI Credits uses the Copilot SDK's final `session.usage.getMetrics` totals, with an independently bounded shutdown fallback. The evaluation's execution ledger covers all attempts (including discarded retries), prompt graders, responders/classifiers, triggers, and evaluation-analysis sessions. Resumed cumulative snapshots count once; multi-turn token-limit grading uses the latest snapshot before shutdown. Cached task diagnostics remain visible but their historical credits are excluded from current billing. Result JSON persists this scope in `evaluation_usage.sessions`; an empty scope never falls back to cached billing. Cross-evaluation session reuse subtracts known starting counters, otherwise affected billing stays unavailable.

A run or model aggregate is unavailable if any contributing metric or model attribution is missing. The average KPI excludes unavailable runs, and Trends explicitly counts them instead of plotting zero. Primary billing views and CSV omit estimated USD; credits display up to nine fractional digits, while CSV preserves the numeric API value with an empty cell for unavailable usage.

### Starting the Dashboard

```bash
waza serve
```

The dashboard opens automatically at `http://localhost:3000`.

### Navigation

- **Overview** — Evaluation summary, pass rates, recent runs
- **Run Details** — Click a run to inspect task results, resolved prompts, and execution trajectories
- **Compare** — Select multiple runs to compare models
- **Trends** — Historical pass rates and performance over time
- **Live View** — Real-time results during active evaluations

### Dashboard Features

- **Sortable run history** — Click a column's sort button to cycle through both directions and the original order. Text columns start ascending, numeric columns start descending, and **When** sorts by the run timestamp. Pass Rate and W. Score are display-only columns. Click a row to open its run details.
- **Live updates** — See results as tasks complete
- **Prompt inspection** — Review the raw resolved prompt sent to each task, format JSON prompts, and copy prompt text
- **Recorded reliability** — Run detail shows stored Wilson pass-rate intervals with trial counts, the suite success-rate interval (one sample per task), and pass^k estimates (all k trials pass, averaged over tasks). These are separate from the bootstrap weighted-score interval and are not between-run significance tests. Missing historical fields show **Not assessed**, never reconstructed estimates.
- **Search** — Find runs, tasks, and models
- **Filtering** — Filter by status (passed/failed), tags, date range
- **Export** — Download results as CSV or JSON
- **Dark mode** — Switch to dark theme for comfortable viewing

### Advanced Dashboard Flags

**Custom port:**
```bash
waza serve --port 8080
```

**Skip auto-open:**
```bash
waza serve --no-browser
```

**Load results from archive:**
```bash
waza serve --results-dir ./previous-runs
```

**JSON-RPC for IDE integration:**
```bash
waza serve --tcp :9000
```

---

## Troubleshooting

### Port Already in Use

If you see "address already in use," the default port 3000 is taken.

**Solution:** Use a different port:
```bash
waza serve --port 3001
```

Or find and stop the process using port 3000:
```bash
# macOS/Linux
lsof -i :3000
kill -9 <PID>

# Windows
netstat -ano | findstr :3000
taskkill /PID <PID> /F
```

---

### No Results Found in Dashboard

If the dashboard shows no results:

1. **Check results directory:** Make sure you've saved results with `-o`:
   ```bash
   waza run evals/code-explainer/eval.yaml -o results.json
   ```

2. **Specify results directory:** If results are in a subdirectory:
   ```bash
   waza serve --results-dir ./eval-results
   ```

3. **Verify file format:** Results must be JSON files (`*.json`).

---

### Fixture Directory Not Found

If you see "fixture directory not found":

1. **Check the path:** Is your `--context-dir` correct?
   ```bash
   waza run eval.yaml --context-dir ./evals/fixtures
   ```

2. **Use relative paths:** Path is relative to the spec file:
   ```
   eval.yaml
   fixtures/
   ```

3. **From any directory:** Specify absolute paths:
   ```bash
   waza run /path/to/eval.yaml --context-dir /path/to/fixtures
   ```

---

### Validation Failures

If tasks fail validation unexpectedly:

1. **Enable verbose output:** See what the model returned:
   ```bash
   waza run eval.yaml -v
   ```

2. **Check your validators:** Are the rules too strict?
   ```yaml
   validators:
     - type: contains
       value: "function"
       caseSensitive: false  # Don't be too strict
   ```

3. **Adjust expected output:** Make rules realistic:
   ```yaml
   expectedOutput:
     - type: contains
       value: "important keyword"  # Not every word needs to match
   ```

---

## Next Steps

- **[Create your first skill](./GETTING-STARTED.md)** — Step-by-step walkthrough
- **[Skill best practices](./SKILL-BEST-PRACTICES.md)** — Guidelines for effective skills
- **[Validators reference](./GRADERS.md)** — All validator types and options
- **[Token limits](./TOKEN-LIMITS.md)** — Optimize SKILL.md for size
- **[Examples](../examples/)** — Real eval suites to explore

---

## Getting Help

- **[GitHub Issues](https://github.com/microsoft/waza/issues)** — Report bugs or request features
- **[Discussions](https://github.com/microsoft/waza/discussions)** — Ask questions, share ideas
- **[Waza Examples](../examples/)** — Runnable evaluation suites

---

**Happy evaluating! 🚀**
