package controlledcomparison_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/controlledcomparison"
	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/stretchr/testify/require"
)

func TestNativeFrozenResourcesReachFreshSDKWorkspace(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native source capture unsupported")
	}
	source := nativeFixture(t, "fixed-model")
	base, err := filepath.EvalSymlinks(filepath.Dir(source.EvalPath))
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(base, "fixtures"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(base, "prompt.txt"), []byte("say hello"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(base, "fixtures", "fixture.txt"), []byte("actual retained fixture"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(base, "fixtures", "instruction.md"), []byte("actual retained instruction"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(base, "task.yaml"), []byte(
		"id: task-0\nname: native\ngolden: true\ninputs:\n  prompt_file: prompt.txt\n  files:\n    - path: fixture.txt\ninstruction_files: [instruction.md]\n"), 0o600))
	policy, err := controlledcomparison.PlanNative(t.Context(), source, source, nativeDesign(), nativeRequirements(), nil)
	require.NoError(t, err)
	calls := 0
	workspaces := map[string]bool{}
	setup := func(_ int, client *nativeTestClient) {
		client.verifyWorkspace = func(config *copilot.SessionConfig) {
			require.NotEqual(t, base, config.WorkingDirectory)
			require.False(t, workspaces[config.WorkingDirectory], "each attempt needs a fresh workspace")
			workspaces[config.WorkingDirectory] = true
			for name, expected := range map[string]string{"fixture.txt": "actual retained fixture", "instruction.md": "actual retained instruction"} {
				got, err := os.ReadFile(filepath.Join(config.WorkingDirectory, name))
				require.NoError(t, err)
				require.Equal(t, expected, string(got))
			}
		}
	}
	require.NoError(t, controlledcomparison.CollectNativeWithOptions(t.Context(), policy, source, source,
		filepath.Join(t.TempDir(), "native"), controlledcomparison.NativeCollectorOptions{NewCopilotClient: nativeFactory(t, &calls, setup)}))
	require.Equal(t, 2, calls)
	for workspace := range workspaces {
		require.NoDirExists(t, workspace, "owned workspaces must be cleaned before collector success")
	}
	got, err := os.ReadFile(filepath.Join(base, "fixtures", "fixture.txt"))
	require.NoError(t, err)
	require.Equal(t, "actual retained fixture", string(got), "source fixture remains unchanged")
}

func TestNativeUnsupportedPlatformBeforeEffects(t *testing.T) {
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		t.Skip("native platform rejection is exercised on unsupported hosts")
	}
	directory := filepath.Join(t.TempDir(), "must-not-exist")
	calls := 0
	err := controlledcomparison.CollectNativeWithOptions(t.Context(), []byte("{}"),
		controlledcomparison.Source{}, controlledcomparison.Source{}, directory,
		controlledcomparison.NativeCollectorOptions{NewCopilotClient: nativeFactory(t, &calls)})
	require.ErrorContains(t, err, "unsupported")
	require.NoDirExists(t, directory)
	require.Zero(t, calls)
}

func TestNativeStaticRequirementsAndConfiguration(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native source capture unsupported")
	}
	for _, name := range []string{"runtime", "assurance", "currency", "auto-model", "skills", "script-grader", "custom-provider", "task-override"} {
		t.Run(name, func(t *testing.T) {
			baseline, candidate := nativeFixture(t, "fixed-model"), nativeFixture(t, "fixed-model")
			required := nativeRequirements()
			switch name {
			case "runtime":
				required.Runtime = true
			case "assurance":
				required.Assurance = true
			case "currency":
				required.Billing = []releasepolicy.BillingRequirement{{Axis: "provider_currency", Currency: "USD", Maximum: "1"}}
			case "custom-provider":
				t.Setenv("COPILOT_BASE_URL", "https://unused.invalid")
			default:
				path := candidate.EvalPath
				old, replacement := "model: fixed-model", "model: auto"
				switch name {
				case "skills":
					old, replacement = "disabled_skills: ['*']", "disabled_skills: []"
				case "script-grader":
					old, replacement = "type: text", "type: script"
				case "task-override":
					path = filepath.Join(filepath.Dir(path), "task.yaml")
					old, replacement = "name: native", "name: native\nskill_paths: ['skills']"
				}
				raw, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Contains(t, string(raw), old)
				require.NoError(t, os.WriteFile(path, []byte(strings.Replace(string(raw), old, replacement, 1)), 0o600))
			}
			_, err := controlledcomparison.PlanNative(t.Context(), baseline, candidate, nativeDesign(), required, nil)
			require.Error(t, err)
		})
	}
}

func TestNativeSourceCurrentnessBeforeSideEffects(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native source capture unsupported")
	}
	for _, name := range []string{"eval", "task", "prompt", "fixture", "instruction"} {
		t.Run(name, func(t *testing.T) {
			candidate := nativeFixture(t, "fixed-model")
			base := filepath.Dir(candidate.EvalPath)
			require.NoError(t, os.Mkdir(filepath.Join(base, "context"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(base, "prompt.txt"), []byte("say hello"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(base, "context", "fixture.txt"), []byte("fixture"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(base, "context", "instruction.md"), []byte("instruction"), 0o600))
			taskPath := filepath.Join(base, "task.yaml")
			require.NoError(t, os.WriteFile(taskPath, []byte(
				"id: task-0\nname: native\ngolden: true\ninputs:\n  prompt_file: prompt.txt\n  files:\n    - path: fixture.txt\ninstruction_files: [instruction.md]\n"), 0o600))
			candidate.ContextDir = filepath.Join(base, "context")
			// Both arms need the same effective task/request identities.
			policy, err := controlledcomparison.PlanNative(t.Context(), candidate, candidate, nativeDesign(), nativeRequirements(), nil)
			require.NoError(t, err)
			path := map[string]string{"eval": candidate.EvalPath, "task": taskPath, "prompt": filepath.Join(base, "prompt.txt"),
				"fixture": filepath.Join(base, "context", "fixture.txt"), "instruction": filepath.Join(base, "context", "instruction.md")}[name]
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			require.NoError(t, err)
			_, err = file.WriteString("\n")
			require.NoError(t, err)
			require.NoError(t, file.Close())
			directory := filepath.Join(t.TempDir(), "native")
			calls := 0
			err = controlledcomparison.CollectNativeWithOptions(t.Context(), policy, candidate, candidate, directory,
				controlledcomparison.NativeCollectorOptions{NewCopilotClient: nativeFactory(t, &calls)})
			require.Error(t, err)
			require.Zero(t, calls)
			require.NoDirExists(t, directory)
		})
	}
}

func TestNativeLateFailurePreservesEveryStartedAttempt(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native durable creation requires Linux or macOS")
	}
	for _, name := range []string{"before-start", "during-execution", "during-shutdown", "between-attempts", "before-publication", "publication-conflict", "overshoot", "reused-session"} {
		t.Run(name, func(t *testing.T) {
			baseline, candidate := nativeFixture(t, "fixed-model"), nativeFixture(t, "fixed-model")
			required := nativeRequirements()
			if name == "overshoot" {
				required.Billing = []releasepolicy.BillingRequirement{{Axis: "input_tokens", Maximum: "7"}}
			}
			policy, err := controlledcomparison.PlanNative(t.Context(), baseline, candidate, nativeDesign(), required, nil)
			require.NoError(t, err)
			directory := filepath.Join(t.TempDir(), "native")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			mutate := func() {
				file, err := os.OpenFile(candidate.EvalPath, os.O_APPEND|os.O_WRONLY, 0)
				require.NoError(t, err)
				_, err = file.WriteString("\n")
				require.NoError(t, err)
				require.NoError(t, file.Close())
			}
			if name == "before-start" {
				cancel()
			}
			setup := func(ordinal int, client *nativeTestClient) {
				switch name {
				case "during-execution":
					client.session.afterSend = cancel
				case "during-shutdown":
					client.onStop = cancel
				case "between-attempts":
					client.onStop = mutate
				case "before-publication":
					if ordinal == 2 {
						client.onStop = mutate
					}
				case "publication-conflict":
					if ordinal == 2 {
						client.onStop = func() {
							require.NoError(t, os.WriteFile(filepath.Join(directory, "journal.json"), []byte("competing owner"), 0o600))
						}
					}
				case "overshoot":
					client.session.inputTokens = 9
				case "reused-session":
					client.session.id = "reused-observed-session"
				}
			}
			err = controlledcomparison.CollectNativeWithOptions(ctx, policy, baseline, candidate, directory,
				controlledcomparison.NativeCollectorOptions{NewCopilotClient: nativeFactory(t, &calls, setup)})
			require.Error(t, err)
			if name == "before-start" {
				require.ErrorIs(t, err, context.Canceled)
				require.Zero(t, calls)
				require.NoDirExists(t, directory)
				return
			}
			decision, readErr := controlledcomparison.ReadSelectedDecision(t.Context(), directory, policy)
			require.Error(t, readErr)
			require.False(t, decision.Accepted)
			require.NotNil(t, decision.Native)
			require.Len(t, decision.Native.Attempts, calls)
			require.FileExists(t, filepath.Join(directory, "native-stop.json"))
			if name == "publication-conflict" {
				raw, err := os.ReadFile(filepath.Join(directory, "journal.json"))
				require.NoError(t, err)
				require.Equal(t, "competing owner", string(raw))
			}
			if name == "during-execution" || name == "during-shutdown" {
				require.Equal(t, 1, calls)
				require.Equal(t, "operational", decision.Native.Attempts[0].Category)
				require.True(t, errors.Is(err, context.Canceled))
			}
			if name == "overshoot" {
				require.Equal(t, 1, calls)
				require.Contains(t, err.Error(), "overshoot")
				var found bool
				for _, axes := range decision.Usage {
					for _, axis := range axes {
						if axis.Axis == "input_tokens" && axis.Value != nil && axis.Value.String() == "9" {
							found = true
						}
					}
				}
				require.True(t, found, "final observed overshoot must remain visible, not clamped to the cap")
			}
			if name == "reused-session" {
				require.Equal(t, 2, calls)
				require.Contains(t, readErr.Error(), "reused")
				for _, axes := range decision.Usage {
					for _, axis := range axes {
						require.Nil(t, axis.Value, "repeated sessions must not become authoritative totals in either arm")
					}
				}
			}
		})
	}
}
