# Waza Examples

This directory contains example evaluation suites demonstrating various features and use cases of waza.

## Available Examples

### 1. [code-explainer](./code-explainer/)
**Purpose**: Complete eval suite for a skill that explains code snippets

**Demonstrates**:
- Multi-trial testing across different programming languages
- Custom script graders for complex evaluation logic
- Task organization with YAML files
- Metrics definition and weighting

**Quick Start**:
```bash
waza run examples/code-explainer/eval.yaml --context-dir examples/code-explainer/fixtures -v
```

---

### 2. [grader-showcase](./grader-showcase/)
**Purpose**: Comprehensive demonstration of all grader types

**Demonstrates**:
- `code` grader: Python assertion-based validation
- `regex` grader: Pattern matching in output
- `file` grader: File existence and content validation
- `behavior` grader: Agent behavior constraints
- `action_sequence` grader: Tool call sequence validation

**Quick Start**:
```bash
waza run examples/grader-showcase/eval.yaml --context-dir examples/grader-showcase/fixtures -v
```

**What's Inside**:
- 5 task examples, one for each grader type
- Detailed README with configuration options
- Runnable fixtures and test data
- Examples of global vs. task-specific graders

---

### 3. [ci](./ci/)
**Purpose**: GitHub Actions workflow examples for CI/CD integration

**Demonstrates**:
- Basic waza evaluation in CI pipeline
- Matrix testing across multiple models
- Result comparison and reporting
- Reusable workflow patterns

**Quick Start**:
See [ci/README.md](./ci/README.md) for integration instructions.

---

## Usage Patterns

### Running Examples

Run an entire eval suite:
```bash
waza run examples/<example-name>/eval.yaml -v
```

Run with context directory (for file-based tasks):
```bash
waza run examples/<example-name>/eval.yaml --context-dir examples/<example-name>/fixtures -v
```

Run specific tasks only:
```bash
waza run examples/<example-name>/eval.yaml --filter="task-name-pattern" -v
```

Save results to JSON:
```bash
waza run examples/<example-name>/eval.yaml -o results.json
```

### Using Examples as Templates

1. **Copy an example**:
   ```bash
   cp -r examples/grader-showcase my-eval
   cd my-eval
   ```

2. **Modify for your use case**:
   - Update `eval.yaml` with your skill name and config
   - Edit or create task files in `tasks/`
   - Add fixture files to `fixtures/`
   - Adjust graders to match your validation needs

3. **Run your eval**:
   ```bash
   waza run eval.yaml -v
   ```

## Example Comparison

| Example | Best For | Complexity | Grader Types |
|---------|----------|------------|--------------|
| **code-explainer** | Complete real-world eval | Medium | code, regex, script |
| **grader-showcase** | Learning grader types | Low | All types |
| **ci** | GitHub Actions integration | Low | N/A (workflow examples) |

## Learning Path

1. **Start with grader-showcase** to understand grader types
2. **Study code-explainer** for realistic eval structure
3. **Reference ci** for production integration

## File Structure Convention

Most examples follow this structure:
```
example-name/
├── eval.yaml           # Main benchmark spec
├── README.md           # Documentation
├── fixtures/           # Context files for tasks
│   ├── file1.ext
│   └── file2.ext
├── tasks/              # Individual task definitions
│   ├── task-1.yaml
│   └── task-2.yaml
└── graders/            # Optional: custom script graders
    └── custom_grader.py
```

## Related Documentation

- **Grader Reference**: [docs/GRADERS.md](../docs/GRADERS.md)
- **Main README**: [README.md](../README.md)
- **Implementation Details**: [IMPLEMENTATION.md](../IMPLEMENTATION.md)

## Contributing Examples

To add a new example:

1. Create a directory: `examples/your-example/`
2. Add required files: `eval.yaml`, `README.md`, `tasks/`, `fixtures/`
3. Document in this README
4. Test with `waza run examples/your-example/eval.yaml -v`
5. Submit a PR

## Support

- **Issues**: Open an issue on GitHub
- **Discussions**: Check existing examples for patterns
- **Documentation**: See linked docs above
