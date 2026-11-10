package manifestaggregate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/testutil/manifestfixture"
	"github.com/stretchr/testify/require"
)

func setup(t *testing.T) (*artifactmanifest.Local, Sources, string) {
	t.Helper()
	directory := t.TempDir()
	backend, err := artifactmanifest.OpenLocal(directory)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	namespace, err := artifactmanifest.NewNamespace(backend, artifactmanifest.ExecutionLedgerNamespaceV1)
	require.NoError(t, err)
	reader, err := namespace.Reader(func(read artifactmanifest.ReadBackend) artifactmanifest.ReadBackend { return read })
	require.NoError(t, err)
	return backend, Sources{Objects: backend, Namespace: reader}, directory
}

func publish(t *testing.T, backend *artifactmanifest.Local, change func(*runnerunit.Report)) Input {
	t.Helper()
	source := manifestfixture.Publish(t, backend, "../../examples/runner/unit.json", nil, change)
	return Input{Manifest: source.Manifest, Evidence: source.Evidence}
}

func TestActualRepeatedExecutionsAndCompatibleViewsAreDeterministic(t *testing.T) {
	backend, sources, _ := setup(t)
	first := publish(t, backend, manifestfixture.RecordedZero)
	second := publish(t, backend, manifestfixture.RecordedZero)
	require.NotEqual(t, first, second, "fresh owners must actually reserve distinct executions")
	inspection, err := evidencestate.Inspect(t.Context(), backend, first.Evidence)
	require.NoError(t, err)
	view := *inspection.Document
	view.Lineage = []evidencestate.Lineage{{Manifest: second.Manifest, Relationship: evidencestate.Aggregation}}
	otherView, err := evidencestate.Publish(t.Context(), backend, view)
	require.NoError(t, err)
	third := Input{Manifest: first.Manifest, Evidence: otherView.EvidenceSHA256}
	inputs := []Input{third, second, first, first}
	report, err := Build(t.Context(), sources, inputs, DefaultLimits())
	require.NoError(t, err)
	require.Equal(t, "complete", report.State)
	require.Equal(t, Counts{Members: 3, VerifiedManifests: 2, VerifiedExecutions: 2,
		MemberStates: map[string]int{"admitted": 3}, MemberOutcomes: map[string]int{"success": 3}}, report.Counts)
	require.Len(t, report.Groups, 1, "same complete native binding has one display profile, not a conflict")
	require.Len(t, report.Groups[0].Members, 3)
	require.Equal(t, "complete", report.Usage.State)
	require.NotNil(t, report.Usage.Recorded)
	require.NotNil(t, report.Usage.Recorded.AICredits)
	require.Zero(t, *report.Usage.Recorded.AICredits)
	require.Equal(t, 2, report.Usage.Executions)
	wire, err := Encode(*report)
	require.NoError(t, err)
	require.NotContains(t, string(wire), `"recorded_historical_usage":null`)
	require.NotContains(t, string(wire), "hello", "public aggregate must not copy captured content")
	require.NotContains(t, string(wire), "execution-evidence-usage.json")
	reordered, err := Build(t.Context(), sources, []Input{first, second, third}, DefaultLimits())
	require.NoError(t, err)
	again, err := Encode(*reordered)
	require.NoError(t, err)
	require.Equal(t, wire, again, "same admitted set and source state must produce identical canonical bytes")
	parsed, err := Parse(wire)
	require.NoError(t, err)
	require.Equal(t, report, parsed)
}

func TestMissingCorruptAndUnsupportedSourcesRetainValidNeighbors(t *testing.T) {
	for _, control := range []string{"missing", "corrupt", "mixed-version", "sidecar-missing", "sidecar-wrong-origin", "marker-conflict"} {
		t.Run(control, func(t *testing.T) {
			backend, sources, directory := setup(t)
			good := publish(t, backend, nil)
			bad := publish(t, backend, nil)
			switch control {
			case "missing":
				bad.Manifest = strings.Repeat("a", 64)
			case "corrupt":
				target := filepath.Join(directory, "manifest-"+bad.Manifest)
				require.NoError(t, os.Chmod(target, 0o600))
				require.NoError(t, os.WriteFile(target, []byte("{}"), 0o600))
			case "mixed-version":
				wire, err := backend.Get(t.Context(), "manifest-"+bad.Manifest, artifactmanifest.MaxManifestBytes)
				require.NoError(t, err)
				wire = bytes.Replace(wire, []byte(`"schema_version":"1.0"`), []byte(`"schema_version":"2.0"`), 1)
				bad.Manifest = artifactmanifest.Hash(wire)
				created, err := backend.Create(t.Context(), "manifest-"+bad.Manifest, wire)
				require.NoError(t, err)
				require.True(t, created, "unsupported profile must actually be stored under its content hash")
			case "sidecar-missing":
				bad.Evidence = strings.Repeat("b", 64)
			case "sidecar-wrong-origin":
				bad.Evidence = good.Evidence
			case "marker-conflict":
				target := filepath.Join(directory, "final-"+bad.Manifest)
				require.NoError(t, os.Chmod(target, 0o600))
				require.NoError(t, os.WriteFile(target, []byte(strings.Repeat("f", 64)), 0o600))
			}
			report, err := Build(t.Context(), sources, []Input{bad, good}, DefaultLimits())
			require.Error(t, err)
			require.NotNil(t, report)
			require.Len(t, report.Members, 2)
			var valid, invalid *Member
			for index := range report.Members {
				member := &report.Members[index]
				if member.Input == good {
					valid = member
				} else {
					invalid = member
				}
			}
			require.NotNil(t, valid)
			require.Equal(t, "admitted", valid.State)
			require.NotNil(t, invalid)
			require.NotEqual(t, "admitted", invalid.State)
			require.NotEmpty(t, invalid.Diagnostics)
			if control == "mixed-version" {
				require.Contains(t, invalid.Diagnostics, "unsupported_or_invalid_profile")
			}
			require.Nil(t, report.Usage.Recorded, "a partial historical subtotal is never authoritative")
			_, encodeErr := Encode(*report)
			require.NoError(t, encodeErr, "incomplete reports must remain serializable")
		})
	}
}

func TestLegitimateOmissionsLegacyMetadataAndFailedOutcomesAreComplete(t *testing.T) {
	backend, sources, _ := setup(t)
	success := publish(t, backend, nil)
	failed := publish(t, backend, func(report *runnerunit.Report) {
		report.Status = "execution_error"
		report.Observation.Status = models.StatusError
		report.Observation.ErrorMsg = "synthetic restricted diagnostic"
	})
	legacy := Input{Manifest: success.Manifest}
	report, err := Build(t.Context(), sources, []Input{success, failed, legacy}, DefaultLimits())
	require.NoError(t, err)
	require.Equal(t, "complete", report.State, "admission completeness is not terminal success or optional sidecar presence")
	require.Equal(t, 1, report.Counts.MemberOutcomes["infrastructure_failure"])
	require.Equal(t, 2, report.Counts.MemberOutcomes["success"])
	require.Equal(t, 3, report.Counts.Members)
	require.Equal(t, 2, report.Counts.VerifiedExecutions)
	require.Equal(t, "unavailable", report.Usage.State)
	require.Nil(t, report.Usage.Recorded)
	var redacted bool
	for _, member := range report.Members {
		for _, category := range member.Categories {
			if category.Category == evidencestate.Conversation {
				require.Equal(t, evidencestate.Redacted, category.State)
				require.Nil(t, category.ObservedCount)
				redacted = true
			}
		}
	}
	require.True(t, redacted, "the actual default privacy control must be exercised")
	wire, err := Encode(*report)
	require.NoError(t, err)
	require.NotContains(t, string(wire), "synthetic restricted diagnostic")
}

func TestCachedViewsWithholdUsageWithoutInventingConflicts(t *testing.T) {
	backend, sources, _ := setup(t)
	first, second := publish(t, backend, manifestfixture.RecordedZero), publish(t, backend, nil)
	inspection, err := evidencestate.Inspect(t.Context(), backend, first.Evidence)
	require.NoError(t, err)
	document := *inspection.Document
	document.Lineage = []evidencestate.Lineage{{Manifest: second.Manifest, Relationship: evidencestate.Cache}}
	cached, err := evidencestate.Publish(t.Context(), backend, document)
	require.NoError(t, err)
	report, err := Build(t.Context(), sources, []Input{first, {Manifest: first.Manifest, Evidence: cached.EvidenceSHA256}}, DefaultLimits())
	require.NoError(t, err)
	require.Equal(t, "complete", report.State)
	require.Equal(t, 1, report.Counts.VerifiedExecutions)
	require.Nil(t, report.Usage.Recorded)
	require.Equal(t, "unavailable", report.Usage.State)
	var found bool
	for _, member := range report.Members {
		if member.Evidence == cached.EvidenceSHA256 {
			require.Contains(t, member.Diagnostics, "cached_historical_usage_excluded")
			found = true
		}
	}
	require.True(t, found, "the actual cached view must be retained")
}

func TestCapabilityAndCumulativeBudgetFailuresStayStructured(t *testing.T) {
	backend, sources, _ := setup(t)
	input := publish(t, backend, nil)
	for _, control := range []struct {
		name    string
		sources Sources
		limits  Limits
		code    string
	}{
		{"missing namespace", Sources{Objects: backend}, DefaultLimits(), "execution_read_capability_unavailable"},
		{"object budget", sources, Limits{Objects: 1, Bytes: 512 << 20}, "read_budget_exhausted"},
		{"byte budget", sources, Limits{Objects: 4096, Bytes: artifactmanifest.MaxManifestBytes}, "read_budget_exhausted"},
	} {
		t.Run(control.name, func(t *testing.T) {
			report, err := Build(t.Context(), control.sources, []Input{input}, control.limits)
			require.Error(t, err)
			require.ErrorIs(t, err, ErrIncomplete)
			require.NotNil(t, report)
			require.Len(t, report.Members, 1)
			require.Contains(t, report.Members[0].Diagnostics, control.code)
			require.Empty(t, report.Members[0].Execution)
			_, encodeErr := Encode(*report)
			require.NoError(t, encodeErr)
		})
	}
}

func TestDifferentWorkProfilesAreNotPooledOrConflicted(t *testing.T) {
	backend, sources, _ := setup(t)
	first := publish(t, backend, nil)
	second := manifestfixture.Publish(t, backend, "../../examples/runner/unit.json", func(request *runnercontract.Request) {
		request.RunKey = "independent-eval-run"
	}, nil)
	report, err := Build(t.Context(), sources, []Input{first, {Manifest: second.Manifest, Evidence: second.Evidence}}, DefaultLimits())
	require.NoError(t, err)
	require.Equal(t, "complete", report.State)
	require.Len(t, report.Groups, 2)
	require.Equal(t, 2, report.Counts.VerifiedExecutions)
}

func TestIncompatibleExecutionClaimDoesNotSelectAWinningView(t *testing.T) {
	backend, sources, _ := setup(t)
	first, second := publish(t, backend, manifestfixture.RecordedZero), publish(t, backend, nil)
	one, err := evidencestate.Inspect(t.Context(), backend, first.Evidence)
	require.NoError(t, err)
	two, err := evidencestate.Inspect(t.Context(), backend, second.Evidence)
	require.NoError(t, err)
	document := *one.Document
	document.ExecutionSource.ExecutionID = two.Document.ExecutionSource.ExecutionID
	wire, err := evidencestate.Encode(document)
	require.NoError(t, err, "negative control must have a valid declaration profile")
	digest := artifactmanifest.Hash(wire)
	require.NoError(t, artifactmanifest.NewStore(backend).Put(t.Context(), artifactmanifest.Artifact{
		Name: "incompatible-view", SHA256: digest, SizeBytes: int64(len(wire)), MediaType: "application/json",
	}, wire), "model an actually stored incompatible public view, not a published verified source")
	report, err := Build(t.Context(), sources, []Input{first, {Manifest: first.Manifest, Evidence: digest}}, DefaultLimits())
	require.ErrorIs(t, err, ErrConflicting)
	require.Equal(t, "conflicting", report.State)
	require.Len(t, report.Members, 2)
	require.Equal(t, 1, report.Counts.MemberStates["admitted"])
	require.Equal(t, 1, report.Counts.MemberStates["conflicting"])
	require.Equal(t, 1, report.Counts.VerifiedExecutions)
	require.Nil(t, report.Usage.Recorded, "a good view must not hide an incompatible supplied view")
	_, encodeErr := Encode(*report)
	require.NoError(t, encodeErr)
}

func TestOriginalCancellationRemainsVisibleWithoutInvalidatingAdmission(t *testing.T) {
	backend, sources, _ := setup(t)
	input := publish(t, backend, nil)
	store := artifactmanifest.NewStore(backend)
	source, err := store.ReadCompleted(t.Context(), input.Manifest)
	require.NoError(t, err)
	manifest := source.Manifest
	terminal, err := runnerterminal.Validate(source.ArtifactBytes[manifest.TerminalArtifact], manifest.Binding.TerminalBinding())
	require.NoError(t, err)
	terminal.Outcome = runnerterminal.Canceled
	terminal.ExitCode, _ = terminal.Outcome.ExitCode()
	terminal.FailureBoundary = "shutdown"
	terminal.Diagnostics = []runnerterminal.Diagnostic{{Code: "cancellation", Message: "Synthetic original cancellation"}}
	terminalBytes, err := runnerterminal.Encode(*terminal, manifest.Binding.TerminalBinding())
	require.NoError(t, err)
	// This authored terminal-only source deliberately has no execution sidecar;
	// it must remain metadata-only rather than borrowing the original owner.
	manifest.Artifacts = []artifactmanifest.Artifact{{
		Name: manifest.TerminalArtifact, SHA256: artifactmanifest.Hash(terminalBytes),
		SizeBytes: int64(len(terminalBytes)), MediaType: "application/json",
	}}
	require.NoError(t, store.Put(t.Context(), manifest.Artifacts[0], terminalBytes))
	wire, err := artifactmanifest.Encode(manifest)
	require.NoError(t, err)
	digest, err := store.Create(t.Context(), wire)
	require.NoError(t, err)
	_, err = store.Finalize(t.Context(), digest)
	require.NoError(t, err)
	report, err := Build(t.Context(), sources, []Input{{Manifest: digest}, input}, DefaultLimits())
	require.NoError(t, err)
	require.Equal(t, "complete", report.State)
	require.Equal(t, 1, report.Counts.MemberOutcomes[string(runnerterminal.Canceled)])
	require.Equal(t, 1, report.Counts.MemberOutcomes["success"])
	require.Equal(t, 1, report.Counts.VerifiedExecutions)
	require.Nil(t, report.Usage.Recorded)
	encoded, err := Encode(*report)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "Synthetic original cancellation")
}

func TestNamespaceDenialNeverFallsBackToProviderOrOrdinal(t *testing.T) {
	backend, sources, _ := setup(t)
	input := publish(t, backend, nil)
	namespace, err := artifactmanifest.NewNamespace(backend, artifactmanifest.ExecutionLedgerNamespaceV1)
	require.NoError(t, err)
	sources.Namespace, err = namespace.Reader(func(artifactmanifest.ReadBackend) artifactmanifest.ReadBackend { return readFailure{} })
	require.NoError(t, err)
	report, err := Build(t.Context(), sources, []Input{input}, DefaultLimits())
	require.ErrorIs(t, err, ErrIncomplete)
	require.Empty(t, report.Members[0].Execution)
	require.Zero(t, report.Counts.VerifiedExecutions)
	require.Contains(t, report.Members[0].Diagnostics, "source_read_error")
	require.Nil(t, report.Usage.Recorded)
}

func TestRecordedUsageOverflowWithholdsTotalThroughPublicSources(t *testing.T) {
	backend, sources, _ := setup(t)
	makeUsage := func(report *runnerunit.Report) {
		manifestfixture.RecordedZero(report)
		report.Observation.Usage.InputTokens = math.MaxInt
	}
	first, second := publish(t, backend, makeUsage), publish(t, backend, makeUsage)
	report, err := Build(t.Context(), sources, []Input{first, second}, DefaultLimits())
	require.NoError(t, err, "source admission remains complete independently of unavailable arithmetic")
	require.Equal(t, "complete", report.State)
	require.Equal(t, 2, report.Counts.VerifiedExecutions)
	require.Equal(t, "unavailable", report.Usage.State)
	require.Nil(t, report.Usage.Recorded)
	require.Contains(t, report.Usage.Diagnostics, "recorded_usage_numeric_precision_unavailable")
	_, err = Encode(*report)
	require.NoError(t, err)
}

func TestNonemptyRecordedZeroIsNotAnAbsentPopulation(t *testing.T) {
	usage, reason := sumRecordedUsage([]*models.UsageStats{{}})
	require.Empty(t, reason)
	require.NotNil(t, usage)
	require.Nil(t, usage.AICredits, "missing credits are not invented as zero")
	require.Zero(t, usage.InputTokens)
	absent, reason := sumRecordedUsage(nil)
	require.Equal(t, "complete_historical_usage_unavailable", reason)
	require.Nil(t, absent)
}

func TestIndividuallyValidLargeUsageCannotBecomeRoundedUITotal(t *testing.T) {
	largeCount := int64(1) << 52
	if int64(int(largeCount)) != largeCount {
		t.Skip("large native counter control requires a 64-bit int platform")
	}
	for _, control := range []struct {
		name      string
		change    func(*models.UsageStats, bool)
		available bool
	}{
		{"safe input counters exceed safe total", func(usage *models.UsageStats, _ bool) {
			usage.InputTokens = int(largeCount)
		}, false},
		{"model counters exceed safe total", func(usage *models.UsageStats, _ bool) {
			model := usage.ModelMetrics["fixture-model"]
			model.InputTokens = int(largeCount)
			usage.ModelMetrics["fixture-model"] = model
		}, false},
		{"safe credit values exceed safe total", func(usage *models.UsageStats, _ bool) {
			value := float64(1 << 52)
			usage.AICredits = &value
		}, false},
		{"positive fractional usage disappears at large magnitude", func(usage *models.UsageStats, second bool) {
			value := float64(1 << 52)
			if second {
				value = 0.25
			}
			usage.PremiumRequests = value
		}, false},
		{"model request values exceed safe total", func(usage *models.UsageStats, _ bool) {
			model := usage.ModelMetrics["fixture-model"]
			model.RequestCount = 1 << 52
			usage.ModelMetrics["fixture-model"] = model
		}, false},
		{"small exact fractions remain supported", func(usage *models.UsageStats, _ bool) {
			usage.PremiumRequests = 0.125
		}, true},
	} {
		t.Run(control.name, func(t *testing.T) {
			backend, sources, _ := setup(t)
			inputs := []Input{}
			for _, second := range []bool{false, true} {
				input := publish(t, backend, func(report *runnerunit.Report) {
					manifestfixture.RecordedZero(report)
					control.change(report.Observation.Usage, second)
				})
				_, categories, err := evidencestate.InspectCategories(t.Context(), backend, input.Evidence)
				require.NoError(t, err)
				var checked bool
				for _, category := range categories {
					if category.Category == evidencestate.Usage {
						require.Equal(t, evidencestate.Present, category.State)
						require.NotNil(t, category.Usage)
						require.LessOrEqual(t, int64(category.Usage.InputTokens), maxJSONInteger)
						require.LessOrEqual(t, category.Usage.PremiumRequests, float64(maxJSONInteger))
						require.NotNil(t, category.Usage.AICredits)
						require.LessOrEqual(t, *category.Usage.AICredits, float64(maxJSONInteger))
						checked = true
					}
				}
				require.True(t, checked, "every original public record must actually admit present usage")
				inputs = append(inputs, input)
			}
			report, err := Build(t.Context(), sources, inputs, DefaultLimits())
			require.NoError(t, err, "usage unavailability must not erase otherwise valid input admissions")
			require.Equal(t, "complete", report.State)
			require.Equal(t, 2, report.Counts.VerifiedExecutions)
			if control.available {
				require.Equal(t, "complete", report.Usage.State)
				require.NotNil(t, report.Usage.Recorded)
				require.Equal(t, 0.25, report.Usage.Recorded.PremiumRequests)
			} else {
				require.Equal(t, "unavailable", report.Usage.State)
				require.Nil(t, report.Usage.Recorded)
				require.Contains(t, report.Usage.Diagnostics, "recorded_usage_numeric_precision_unavailable")
			}
			wire, err := Encode(*report)
			require.NoError(t, err)
			_, err = Parse(wire)
			require.NoError(t, err, "the actual emitted report must honor the UI-safe numeric wire schema")
		})
	}
}

func TestZeroEligibleRecordsAndUnknownUsageDoNotCreateZeroTotal(t *testing.T) {
	backend, sources, _ := setup(t)
	zero := publish(t, backend, manifestfixture.RecordedZero)
	absent := publish(t, backend, nil)
	for _, control := range []struct {
		name    string
		inputs  []Input
		records int
	}{
		{"metadata has no eligible record", []Input{{Manifest: zero.Manifest}}, 0},
		{"known zero cannot exclude unknown usage", []Input{zero, absent}, 2},
		{"known zero cannot exclude unknown identity view", []Input{zero, {Manifest: absent.Manifest}}, 1},
	} {
		t.Run(control.name, func(t *testing.T) {
			report, err := Build(t.Context(), sources, control.inputs, DefaultLimits())
			require.NoError(t, err)
			require.Equal(t, len(control.inputs), report.Counts.Members)
			require.Equal(t, control.records, report.Counts.VerifiedExecutions)
			require.Equal(t, "unavailable", report.Usage.State)
			require.Nil(t, report.Usage.Recorded)
			require.Contains(t, report.Usage.Diagnostics, "complete_historical_usage_unavailable")
		})
	}
}

func TestStrictInputAndReportWirePresence(t *testing.T) {
	digest := strings.Repeat("a", 64)
	for _, input := range []string{
		`null`, `[]`, `[null]`, `[{}]`,
		`[{"manifest_sha256":"` + digest + `","evidence_sha256":null}]`,
		`[{"manifest_sha256":"` + digest + `","evidence_sha256":""}]`,
		`[{"manifest_sha256":"` + digest + `","extra":false}]`,
		`[{"manifest_sha256":"` + digest + `","manifest_sha256":"` + digest + `"}]`,
	} {
		_, err := ParseInputs([]byte(input))
		require.ErrorIs(t, err, ErrInvalid, input)
	}
	backend, sources, _ := setup(t)
	good := publish(t, backend, nil)
	report, err := Build(t.Context(), sources, []Input{good}, DefaultLimits())
	require.NoError(t, err)
	wire, err := Encode(*report)
	require.NoError(t, err)
	object := func(value map[string]any, key string) map[string]any {
		result, ok := value[key].(map[string]any)
		require.True(t, ok)
		return result
	}
	for _, mutation := range []func(map[string]any){
		func(value map[string]any) { value["schema_version"] = "2.0" },
		func(value map[string]any) { object(value, "counts")["input_members"] = 0 },
		func(value map[string]any) {
			object(value, "usage")["recorded_historical_usage"] = map[string]any{}
		},
		func(value map[string]any) { object(value, "usage")["recorded_historical_usage"] = false },
		func(value map[string]any) { delete(object(value, "usage"), "recorded_historical_usage") },
	} {
		var value map[string]any
		require.NoError(t, json.Unmarshal(wire, &value))
		mutation(value)
		changed, err := json.Marshal(value)
		require.NoError(t, err)
		require.NotEqual(t, wire, changed)
		_, err = Parse(changed)
		require.Error(t, err)
	}
}

type readFailure struct{}

func (readFailure) Get(context.Context, string, int64) ([]byte, error) {
	return nil, errors.New("/private/operator/path token=synthetic-secret")
}

func TestPrivateReadErrorsAreNotPublicDiagnostics(t *testing.T) {
	report, err := Build(t.Context(), Sources{Objects: readFailure{}},
		[]Input{{Manifest: strings.Repeat("a", 64)}}, DefaultLimits())
	require.Error(t, err)
	require.ErrorContains(t, err, "/private/operator/path")
	wire, encodeErr := Encode(*report)
	require.NoError(t, encodeErr)
	require.NotContains(t, string(wire), "/private")
	require.NotContains(t, string(wire), "synthetic-secret")
	require.Contains(t, report.Members[0].Diagnostics, "source_read_error")
}
