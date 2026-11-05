package controlledcomparison_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/controlledcomparison"
	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/stretchr/testify/require"
)

type corePolicyFailureContext struct {
	context.Context
	caller     string
	occurrence int
	calls      int
	triggered  bool
	fault      func()
}

func (c *corePolicyFailureContext) Err() error {
	var pcs [8]uintptr
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs[:])])
	frame, _ := frames.Next()
	if !c.triggered && strings.HasSuffix(frame.Function, c.caller) {
		c.calls++
		if c.calls == c.occurrence {
			c.triggered = true
			c.fault()
		}
	}
	return c.Context.Err()
}

func TestNativeReaderPreservesPrePolicyCoreFailures(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native durable creation requires Linux or macOS")
	}
	source := nativeFixture(t, "fixed-model")
	policy, err := controlledcomparison.PlanNative(t.Context(), source, source, nativeDesign(), nativeRequirements(), nil)
	require.NoError(t, err)
	policy = fixedNativePolicy(t, policy)
	original := filepath.Join(t.TempDir(), "original")
	calls := 0
	require.NoError(t, controlledcomparison.CollectNativeWithOptions(t.Context(), policy, source, source, original,
		controlledcomparison.NativeCollectorOptions{NewCopilotClient: nativeFactory(t, &calls)}))
	require.Equal(t, 2, calls)
	clean, err := controlledcomparison.ReadSelectedDecision(t.Context(), original, policy)
	require.NoError(t, err)
	require.Equal(t, "compatible", clean.Compatibility.State)
	require.Equal(t, "complete", clean.Completeness.State)
	require.Len(t, clean.Native.Attempts, 2)

	for _, caller := range []string{"selected", "retained-root"} {
		t.Run(caller, func(t *testing.T) {
			for _, fault := range []struct {
				name       string
				diagnostic string
				cause      error
				frame      string
				occurrence int
			}{
				{name: "missing", diagnostic: "core-policy.json", cause: os.ErrNotExist},
				{name: "missing-with-journal-read-failure", diagnostic: "core-policy.json", cause: os.ErrNotExist},
				{name: "directory", diagnostic: "must be a regular file"},
				{name: "symlink", diagnostic: "must be a regular file"},
				{name: "oversize", diagnostic: "document exceeds byte limit"},
				{name: "removed-before-open", diagnostic: "core-policy.json", cause: os.ErrNotExist,
					frame: "/assurance.ReadDocument", occurrence: 1},
				{name: "cancel-before-read", diagnostic: "context canceled", cause: context.Canceled,
					frame: "/releasepolicy.ReadCoreDecisionRetainedRootContext.func1", occurrence: 1},
				{name: "cancel-before-stat", diagnostic: "context canceled", cause: context.Canceled,
					frame: "/releasepolicy.readArtifactRootContext", occurrence: 1},
				{name: "cancel-before-open", diagnostic: "context canceled", cause: context.Canceled,
					frame: "/assurance.ReadDocument", occurrence: 1},
				{name: "cancel-during-read", diagnostic: "context canceled", cause: context.Canceled,
					frame: "/assurance.ReadDocument", occurrence: 2},
				{name: "cancel-after-read", diagnostic: "context canceled", cause: context.Canceled,
					frame: "/assurance.ReadDocument", occurrence: 4},
				{name: "replaced-after-read", diagnostic: "core-policy.json changed while reading",
					frame: "/assurance.ReadDocument", occurrence: 4},
			} {
				t.Run(fault.name, func(t *testing.T) {
					directory := filepath.Join(t.TempDir(), "collection")
					require.NoError(t, os.Mkdir(directory, 0o700))
					copyCollectionArtifacts(t, original, directory)
					path := filepath.Join(directory, "core-policy.json")
					journal, err := os.ReadFile(filepath.Join(directory, "journal.ndjson"))
					require.NoError(t, err)
					require.Contains(t, string(journal), `"type":"attempt_start"`)
					coreBytes, err := os.ReadFile(path)
					require.NoError(t, err)
					require.Less(t, len(coreBytes), 64*1024, "the fourth document cancellation check follows EOF, not another chunk")
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					var boundary *corePolicyFailureContext
					if fault.frame != "" {
						boundary = &corePolicyFailureContext{Context: ctx, caller: fault.frame, occurrence: fault.occurrence,
							fault: func() {
								switch fault.name {
								case "removed-before-open":
									require.NoError(t, os.Remove(path))
								case "replaced-after-read":
									require.NoError(t, os.Rename(path, path+".original"))
									require.NoError(t, os.WriteFile(path, coreBytes, 0o600))
								default:
									cancel()
								}
							}}
						ctx = boundary
					} else {
						require.NoError(t, os.Remove(path))
						switch fault.name {
						case "directory":
							require.NoError(t, os.Mkdir(path, 0o700))
						case "symlink":
							require.NoError(t, os.Symlink(filepath.Join(original, "core-policy.json"), path))
						case "oversize":
							file, err := os.Create(path)
							require.NoError(t, err)
							require.NoError(t, file.Truncate((16<<20)+1))
							require.NoError(t, file.Close())
						case "missing-with-journal-read-failure":
							require.NoError(t, os.Rename(filepath.Join(directory, "journal.ndjson"), filepath.Join(directory, "journal.original")))
						}
					}
					var decision releasepolicy.Decision
					if caller == "selected" {
						decision, err = controlledcomparison.ReadSelectedDecision(ctx, directory, policy)
					} else {
						root, openErr := os.OpenRoot(directory)
						require.NoError(t, openErr)
						decision, err = controlledcomparison.ReadDecisionRootContext(ctx, root)
						require.NoError(t, root.Close())
					}
					if boundary != nil {
						require.True(t, boundary.triggered, "fault must occur at the actual core-policy read boundary")
					}
					require.Error(t, err)
					t.Logf("original fault=%s; returned error=%v; compatibility=%s completeness=%s operations=%s",
						fault.name, err, decision.Compatibility.State, decision.Completeness.State, decision.Operations.State)
					require.ErrorContains(t, err, "reading precollection policy")
					require.ErrorContains(t, err, fault.diagnostic)
					if fault.cause != nil {
						require.ErrorIs(t, err, fault.cause)
					}
					if fault.name == "missing-with-journal-read-failure" {
						require.ErrorContains(t, err, "journal.ndjson")
					}
					require.False(t, decision.Accepted)
					require.Equal(t, "invalid", decision.Compatibility.State)
					require.Contains(t, strings.Join(decision.Compatibility.Reasons, "\n"), "reading precollection policy")
					require.Equal(t, "missing", decision.Completeness.State)
					require.Equal(t, "not_assessed", decision.Operations.State)
					require.Equal(t, "not_assessed", decision.Statistics.State)
					require.Empty(t, decision.Accounting, "unread policy must not invent attempt accounting")
					require.Empty(t, decision.Usage, "unread policy must not invent authoritative usage")
					journalName := "journal.ndjson"
					if fault.name == "missing-with-journal-read-failure" {
						journalName = "journal.original"
					}
					retained, readErr := os.ReadFile(filepath.Join(directory, journalName))
					require.NoError(t, readErr)
					require.Equal(t, journal, retained, "failure reporting must not alter the started-attempt journal")
				})
			}
		})
	}
	repeated, err := controlledcomparison.ReadSelectedDecision(t.Context(), original, policy)
	require.NoError(t, err)
	require.Equal(t, clean, repeated, "failed invocations must not contaminate an intact collection")
	require.Equal(t, 2, calls, "reader faults must never start an SDK runtime")
}
