# History: Turk — Go Performance Specialist

## Project Context

- **Project:** waza — CLI tool for evaluating Agent Skills
- **Owner:** Shayne Boyer
- **Stack:** Go (primary), TypeScript/React 19 (dashboard)
- **Repository:** spboyer/waza
- **Codebase:** 239 Go files, ~53K LOC across `cmd/waza/` and `internal/`
- **Azure integration:** azd SDK, azcore, Copilot SDK via JSON-RPC

## Key Packages

- `internal/orchestration/` — Test runner, goroutine management, fixture I/O (1548 LOC)
- `internal/execution/` — Copilot SDK integration, JSON-RPC transport, session events
- `internal/cache/` — Caching layer with concurrent access
- `internal/tokens/bpe/` — BPE tokenizer (CPU-bound, 616 LOC)
- `internal/jsonrpc/` — JSON-RPC transport and handlers
- `internal/webapi/` — REST API handlers, data store
- `internal/webserver/` — HTTP server
- `cmd/waza/` — CLI commands, flag parsing, result reporting

## Learnings

- `internal/orchestration/runner.go` is the primary runtime hotspot: it recreates grader instances per run, does O(n²) stop-on-error scanning, and reads fixture resources into memory before copying into temp workspaces.
- `cmd/waza/cmd_run.go` and runner context wiring rely on `context.Background()` for long runs, so Ctrl+C/signal cancellation does not propagate cleanly through benchmark execution and shutdown paths.
- `internal/webapi/store.go` performs directory walk + full JSON load while holding a lock and recomputes run summaries on each request; this will bottleneck dashboard API latency as result volume grows.
- `internal/execution/copilot.go` accumulates per-run temp workspaces until engine shutdown; disk and inode pressure can spike on large evals before cleanup occurs.
- `internal/tokens/bpe/tokenizer.go` and `internal/checks/token_limits.go` contain CPU/allocation-heavy token counting paths (`regexp` loops, repeated `[]byte` conversions, full-string CRLF normalization) that are prime candidates for preallocation and byte-level processing.
- `internal/graders/inline_script_grader.go` and `internal/graders/program_grader.go` pay repeated process and file-I/O costs per grading run (temp script file creation and repeated `.waza.yaml` loads), which compounds heavily with multi-run benchmarks.
- `cmd/waza/dev/links.go` uses concurrent URL checks but leaves connection reuse efficiency on the table (GET responses are closed without draining, parser/client setup repeated), making large link-check batches slower than necessary.

### Opus 4.6 Pass (2026-02-22)

- **23 findings total** (3 P0, 9 P1, 11 P2) across 20 source files. Confirmed and deepened earlier Codex findings with line-level specificity.
- The JSON-RPC transport (`internal/jsonrpc/transport.go`) allocates per-message via `json.Marshal` + append; switching to `json.Encoder` on a buffered writer would eliminate these.
- `SessionEventsCollector.On()` is not goroutine-safe — if the Copilot SDK ever delivers events from multiple goroutines, this is a data race. Needs a mutex or sequential delivery guarantee documented.
- `HandlerContext.runs` map in JSON-RPC handlers is never pruned — completed runs accumulate indefinitely, a memory leak for long-running MCP servers.
- BPE tokenizer's `Decode()` starts with `make([]byte, 0)` — no capacity hint. Pre-allocating `len(tokens)*4` would reduce growths for typical output.
- Spinner uses `time.After` in a select loop, creating a new timer per tick that isn't collected until it fires. `time.NewTicker` is the idiomatic fix.
- Trigger runner goroutines don't check `ctx.Done()` before acquiring the semaphore, leading to wasted work after cancellation.
- `CopilotEngine.Shutdown` accepts a context but ignores it — `os.RemoveAll` calls on slow filesystems could hang indefinitely.

## Architecture Notes

- The execution path is layered as `cmd_run -> orchestration.TestRunner -> execution.AgentEngine (Copilot SDK) -> graders`, so per-test overhead in runner/graders multiplies rapidly with `runs_per_test` and task count.
- Web dashboard request cost is currently tied to on-demand transformation of full `EvaluationOutcome` structures; introducing cached/derived summaries would isolate API latency from historical result volume.
- Cancellation semantics are uneven across runner, trigger, and JSON-RPC paths; consistent `ctx.Done()` checks at worker acquisition boundaries are required to avoid wasted compute after cancellation.
