package orchestration

import (
	"context"
	"fmt"
	"sync"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/hooks"
)

// RunSetup owns one execution of an eval's before_run hooks and the matching
// after_run hooks. Setup may generate task files, prompt files or CSV datasets,
// so it runs before tasks are loaded for MCP admission and before any engine
// starts. Teardown runs after_run at most once.
type RunSetup struct {
	cfg      *config.EvalConfig
	hooks    *hooks.Runner
	teardown sync.Once
}

// StartRunSetup runs the eval's before_run hooks. If they fail, it runs the
// after_run hooks, as a failed benchmark always has, and returns the error.
func StartRunSetup(ctx context.Context, cfg *config.EvalConfig, verbose bool) (*RunSetup, error) {
	setup := &RunSetup{cfg: cfg, hooks: &hooks.Runner{Verbose: verbose}}
	if before := cfg.Spec().Hooks.BeforeRun; len(before) > 0 {
		if err := setup.hooks.Execute(ctx, "before_run", before); err != nil {
			setup.Teardown(ctx)
			return nil, fmt.Errorf("before_run hook failed: %w", err)
		}
	}
	return setup, nil
}

// Teardown runs the eval's after_run hooks once; later calls do nothing.
func (s *RunSetup) Teardown(ctx context.Context) {
	if s == nil {
		return
	}
	s.teardown.Do(func() {
		if after := s.cfg.Spec().Hooks.AfterRun; len(after) > 0 {
			if err := s.hooks.Execute(ctx, "after_run", after); err != nil {
				fmt.Printf("[WARN] after_run hook error: %v\n", err)
			}
		}
	})
}

// WithRunSetup hands a setup that already ran to the runner. Its first
// benchmark pass uses it instead of running before_run again and owns its
// teardown; the caller may still call Teardown, which is then a no-op.
func WithRunSetup(setup *RunSetup) RunnerOption {
	return func(r *EvalRunner) {
		r.setup = setup
	}
}

// takeSetup returns the pending setup for a benchmark pass, starting a new one
// when none is pending (for example, the second baseline pass).
func (r *EvalRunner) takeSetup(ctx context.Context) (*RunSetup, error) {
	setup := r.setup
	r.setup = nil
	if setup != nil {
		return setup, nil
	}
	return StartRunSetup(ctx, r.cfg, r.verbose)
}
