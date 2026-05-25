package orchestration

import (
	"fmt"
	"io"
	"os"
	"runtime"
)

// DefaultWorkerCap is the conservative upper bound applied when --workers is
// not set explicitly. It protects high-CPU CI runners from spawning a session
// per core (memory pressure, Copilot session limits) until R4 introduces a
// separate --sessions knob. See docs/design/135-improve-concurrency.md.
const DefaultWorkerCap = 8

// ResolveWorkers picks the effective worker count for a phase that is about
// to dispatch `jobs` units of work.
//
//   - When `requested <= 0`, defaults to min(runtime.NumCPU(), jobs, DefaultWorkerCap).
//   - When `requested > 0`, clamps to `jobs` (no point spawning more workers
//     than there is work) and emits a one-line notice to `out` so users
//     understand actual parallelism.
//
// `phase` is a short label such as "tasks" or "trigger tests" used in the
// notice. Pass `nil` for `out` to silence the message (tests, etc.).
func ResolveWorkers(requested, jobs int, phase string, out io.Writer) int {
	if jobs <= 0 {
		return 0
	}

	if requested <= 0 {
		// Auto-size with conservative cap (B1 from #135 critique).
		w := runtime.NumCPU()
		if w > DefaultWorkerCap {
			w = DefaultWorkerCap
		}
		if w > jobs {
			w = jobs
		}
		return w
	}

	if requested > jobs {
		if out != nil {
			_, _ = fmt.Fprintf(out, "workers=%d capped to %d (%s)\n", requested, jobs, phase)
		}
		return jobs
	}
	return requested
}

// ResolveWorkersStderr is a convenience wrapper that logs to os.Stderr.
func ResolveWorkersStderr(requested, jobs int, phase string) int {
	return ResolveWorkers(requested, jobs, phase, os.Stderr)
}
