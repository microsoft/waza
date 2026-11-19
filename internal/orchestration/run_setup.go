package orchestration

import (
	"context"
	"fmt"
	"sync"
	"time"

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
	setup := &RunSetup{cfg: cfg, hooks: &hooks.Runner{Verbose: verbose, Vars: cfg.Spec().Inputs}}
	if before := cfg.Spec().Hooks.BeforeRun; len(before) > 0 {
		if err := setup.hooks.Execute(ctx, "before_run", before); err != nil {
			setup.Teardown(ctx)
			return nil, fmt.Errorf("before_run hook failed: %w", err)
		}
	}
	return setup, nil
}

// interruptedTeardownTimeout bounds after_run when the run was interrupted, so
// cleanup still runs but cannot keep an interrupted process alive indefinitely.
const interruptedTeardownTimeout = time.Minute

// Teardown runs the eval's after_run hooks once; later calls do nothing. If
// ctx was canceled (for example by Ctrl+C), after_run still runs, bounded by
// interruptedTeardownTimeout.
func (s *RunSetup) Teardown(ctx context.Context) {
	if s == nil {
		return
	}
	s.teardown.Do(func() {
		if ctx.Err() != nil {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), interruptedTeardownTimeout)
			defer cancel()
		}
		if after := s.cfg.Spec().Hooks.AfterRun; len(after) > 0 {
			if err := s.hooks.Execute(ctx, "after_run", after); err != nil {
				fmt.Printf("[WARN] after_run hook error: %v\n", err)
			}
		}
	})
}
