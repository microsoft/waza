package ownedsubmission

import (
	"time"

	"github.com/microsoft/waza/internal/runnerunit"
)

// NewLifecycleCaptureHooks creates one invocation-owned non-writing observer and
// conservative capture policy. It does not register or authorize public routes.
func NewLifecycleCaptureHooks(now func() time.Time) (runnerunit.SubmissionHooks, error) {
	observer, err := runnerunit.NewPreparedLifecycleObserver(now)
	if err != nil {
		return runnerunit.SubmissionHooks{}, err
	}
	hooks := observer.Hooks()
	hooks.Factory = runnerunit.NewEngineFactory(observer.RuntimeObservations)
	return CaptureHooks(hooks)
}
