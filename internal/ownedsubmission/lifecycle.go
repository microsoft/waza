package ownedsubmission

import (
	"time"

	"github.com/microsoft/waza/internal/runnerunit"
)

// NewLifecycleCaptureHooks creates one invocation-owned non-writing observer and
// conservative capture policy. It does not register or authorize public routes.
func NewLifecycleCaptureHooks(now func() time.Time) (runnerunit.SubmissionHooks, error) {
	hooks, err := NewLifecycleObserverHooks(now, nil)
	if err != nil {
		return runnerunit.SubmissionHooks{}, err
	}
	return CaptureHooks(hooks)
}

// NewLifecycleObserverHooks stages one invocation's lifecycle without applying
// a publication policy. Checkpoint callers retain their captured projection and
// apply CaptureHooks once through SubmitCaptured or ResumeCaptured.
func NewLifecycleObserverHooks(now func() time.Time, service *runnerunit.Service) (runnerunit.SubmissionHooks, error) {
	observer, err := runnerunit.NewPreparedLifecycleObserver(now)
	if err != nil {
		return runnerunit.SubmissionHooks{}, err
	}
	hooks := observer.Hooks()
	if service == nil {
		hooks.Factory = runnerunit.NewEngineFactory(observer.RuntimeObservations)
	} else {
		hooks.Factory = service.NativeEngineFactory(observer.RuntimeObservations)
	}
	return hooks, nil
}
