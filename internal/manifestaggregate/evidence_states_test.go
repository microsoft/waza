package manifestaggregate

import (
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/testutil/manifestfixture"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

const unitRequest = "../../examples/runner/unit.json"

var evidenceFixtureOutput = flag.String("aggregate-evidence-fixture-output", "", "Exclusively save a producer-backed evidence state and lineage caller fixture")

var allWithheld = []evidencestate.Category{
	evidencestate.Conversation, evidencestate.Tool, evidencestate.Grader, evidencestate.Reliability, evidencestate.Usage,
}

func publishWith(t *testing.T, backend *artifactmanifest.Local, options manifestfixture.Options) (Input, string) {
	t.Helper()
	source := manifestfixture.PublishWith(t, backend, unitRequest, options)
	return Input{Manifest: source.Manifest, Evidence: source.Evidence}, source.Execution
}

func snapshotStore(t *testing.T, directory string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	require.NoError(t, filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		files[path] = data
		return err
	}))
	return files
}

func memberOf(t *testing.T, report *Report, input Input) Member {
	t.Helper()
	for _, member := range report.Members {
		if member.Input == input {
			return member
		}
	}
	t.Fatalf("member %+v missing from report", input)
	return Member{}
}

func categoryOf(member Member, category evidencestate.Category) Category {
	for _, record := range member.Categories {
		if record.Category == category {
			return record
		}
	}
	return Category{}
}

type schemaDirectory struct{}

func (schemaDirectory) Load(url string) (any, error) {
	const prefix = "https://microsoft.github.io/waza/schemas/"
	if !strings.HasPrefix(url, prefix) {
		return nil, fs.ErrNotExist
	}
	file, err := os.Open(filepath.Join("..", "..", "schemas", filepath.Base(strings.TrimPrefix(url, prefix))))
	if err != nil {
		return nil, err
	}
	document, err := jsonschema.UnmarshalJSON(file)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return document, err
}

func aggregateSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(jsonschema.SchemeURLLoader{"https": schemaDirectory{}})
	document, err := jsonschema.UnmarshalJSON(strings.NewReader(schemas.ManifestAggregateSchemaJSON))
	require.NoError(t, err)
	const id = "https://microsoft.github.io/waza/schemas/manifest-aggregate-1.0.schema.json"
	require.NoError(t, compiler.AddResource(id, document))
	compiled, err := compiler.Compile(id)
	require.NoError(t, err)
	return compiled
}

func TestRetryAndResumeLineageRetainsEveryExecutionWithoutWinner(t *testing.T) {
	backend, sources, directory := setup(t)
	original, originalExecution := publishWith(t, backend, manifestfixture.Options{
		ChangeReport: func(report *runnerunit.Report) { report.Status = "execution_error" },
	})
	before := snapshotStore(t, directory)

	retry, retryExecution := publishWith(t, backend, manifestfixture.Options{
		ChangeReport: manifestfixture.RecordedNonzero(1),
		Parent:       &executionattempt.Parent{ExecutionID: originalExecution, Relationship: "retry"},
	})
	inspection, err := evidencestate.Inspect(t.Context(), backend, retry.Evidence)
	require.NoError(t, err)
	view := *inspection.Document
	view.Lineage = []evidencestate.Lineage{{Manifest: original.Manifest, Relationship: evidencestate.Resume}}
	published, err := evidencestate.Publish(t.Context(), backend, view)
	require.NoError(t, err)
	resumed := Input{Manifest: retry.Manifest, Evidence: published.EvidenceSHA256}

	after := snapshotStore(t, directory)
	for path, data := range before {
		require.Equal(t, data, after[path], "retry and resume must append, never overwrite, original sources: %s", path)
	}

	report, err := Build(t.Context(), sources, []Input{resumed, retry, original}, DefaultLimits())
	require.NoError(t, err)
	require.Equal(t, "complete", report.State)
	require.Equal(t, Counts{Members: 3, VerifiedManifests: 2, VerifiedExecutions: 2,
		MemberStates:   map[string]int{"admitted": 3},
		MemberOutcomes: map[string]int{"infrastructure_failure": 1, "success": 2}}, report.Counts,
		"the failed original stays visible beside its retry; no attempt is selected as a winner")

	first := memberOf(t, report, original)
	require.Equal(t, runnerterminal.InfrastructureFailure, first.Outcome)
	require.Equal(t, originalExecution, first.Execution)
	require.Nil(t, first.Parent)
	require.Empty(t, first.Lineage)

	parent := &executionattempt.Parent{ExecutionID: originalExecution, Relationship: "retry"}
	second := memberOf(t, report, retry)
	require.Equal(t, retryExecution, second.Execution)
	require.Equal(t, parent, second.Parent, "parentage comes from the verified original reservation")
	require.Empty(t, second.Lineage)

	third := memberOf(t, report, resumed)
	require.Equal(t, retryExecution, third.Execution)
	require.Equal(t, parent, third.Parent)
	require.Equal(t, []evidencestate.Lineage{{Manifest: original.Manifest, Relationship: evidencestate.Resume}}, third.Lineage)

	wire, err := Encode(*report)
	require.NoError(t, err)
	parsed, err := Parse(wire)
	require.NoError(t, err)
	require.Equal(t, report, parsed)
	reordered, err := Build(t.Context(), sources, []Input{original, resumed, retry}, DefaultLimits())
	require.NoError(t, err)
	again, err := Encode(*reordered)
	require.NoError(t, err)
	require.Equal(t, wire, again)

	conflictingView := view
	conflictingView.Categories = slices.Clone(view.Categories)
	for index := range conflictingView.Categories {
		if record := &conflictingView.Categories[index]; record.Category == evidencestate.Grader {
			record.State, record.Reason, record.ObservedCount, record.Artifacts, record.Limit =
				evidencestate.Unavailable, evidencestate.NotRecorded, nil, []string{}, nil
		}
	}
	conflictingPublication, err := evidencestate.Publish(t.Context(), backend, conflictingView)
	require.NoError(t, err)
	conflicted := Input{Manifest: retry.Manifest, Evidence: conflictingPublication.EvidenceSHA256}
	conflictReport, err := Build(t.Context(), sources, []Input{conflicted}, DefaultLimits())
	require.ErrorIs(t, err, ErrConflicting)
	require.Equal(t, "conflicting", memberOf(t, conflictReport, conflicted).State)
	require.Equal(t, view.Lineage, memberOf(t, conflictReport, conflicted).Lineage,
		"a conflicting member keeps its declared lineage for investigation")
	conflictWire, err := Encode(*conflictReport)
	require.NoError(t, err)
	_, err = Parse(conflictWire)
	require.NoError(t, err)

	schema := aggregateSchema(t)
	validate := func(data []byte) error {
		t.Helper()
		instance, err := jsonschema.UnmarshalJSON(strings.NewReader(string(data)))
		require.NoError(t, err)
		return schema.Validate(instance)
	}
	require.NoError(t, validate(wire), "the published schema accepts canonical parent and lineage")

	forgeBytes := func(change func(*Member)) []byte {
		t.Helper()
		copied := *report
		copied.Members = append([]Member(nil), report.Members...)
		for index := range copied.Members {
			if copied.Members[index].Input == resumed {
				copied.Members[index].Lineage = slices.Clone(copied.Members[index].Lineage)
				change(&copied.Members[index])
			}
		}
		data, err := json.Marshal(copied)
		require.NoError(t, err)
		return data
	}
	forge := func(change func(*Member)) error {
		_, err := Parse(forgeBytes(change))
		return err
	}
	forgeSchema := func(change func(*Member)) error { return validate(forgeBytes(change)) }
	require.Error(t, forgeSchema(func(m *Member) { m.Execution = "" }), "schema requires an execution beside a parent")
	require.Error(t, forgeSchema(func(m *Member) { m.Evidence, m.Parent, m.Execution = "", nil, "" }), "schema requires a sidecar beside lineage")
	for name, change := range map[string]func(*Member){
		"self parent": func(m *Member) { m.Parent = &executionattempt.Parent{ExecutionID: m.Execution, Relationship: "retry"} },
		"unknown parent kind": func(m *Member) {
			m.Parent = &executionattempt.Parent{ExecutionID: originalExecution, Relationship: "winner"}
		},
		"self lineage": func(m *Member) {
			m.Lineage = []evidencestate.Lineage{{Manifest: m.Manifest, Relationship: evidencestate.Retry}}
		},
		"unknown lineage kind":                  func(m *Member) { m.Lineage[0].Relationship = "winner" },
		"cache lineage without usage exclusion": func(m *Member) { m.Lineage[0].Relationship = evidencestate.Cache },
		"unsorted lineage": func(m *Member) {
			m.Lineage = append(m.Lineage, evidencestate.Lineage{Manifest: original.Manifest, Relationship: evidencestate.Aggregation})
		},
	} {
		require.ErrorIs(t, forge(change), ErrInvalid, name)
	}
	require.Equal(t, []evidencestate.Lineage{{Manifest: original.Manifest, Relationship: evidencestate.Resume}},
		memberOf(t, report, resumed).Lineage, "negative controls never mutate the baseline report")
	require.ErrorIs(t, forge(func(m *Member) { m.Execution = "" }), ErrInvalid, "parent without a verified execution")
	require.ErrorIs(t, forge(func(m *Member) {
		m.Evidence, m.Parent, m.Execution = "", nil, ""
	}), ErrInvalid, "lineage without a sidecar")
}

func TestDegradedEvidenceNeverAggregatesAsPresent(t *testing.T) {
	oversizeTool := func(report *runnerunit.Report) {
		manifestfixture.RecordedNonzero(1)(report)
		report.Observation.ToolEvents = []models.ToolEvent{{
			Turn: 1, Sequence: 1, ToolName: "bounded", Success: true,
			Result: strings.Repeat("x", artifactmanifest.MaxArtifactBytes+1),
		}}
	}
	for _, test := range []struct {
		name     string
		options  manifestfixture.Options
		degraded map[evidencestate.Category]evidencestate.State
		usage    string
	}{
		{"default privacy redaction", manifestfixture.Options{ChangeReport: manifestfixture.RecordedNonzero(1)},
			map[evidencestate.Category]evidencestate.State{
				evidencestate.Conversation: evidencestate.Redacted, evidencestate.Tool: evidencestate.Redacted,
			}, "complete"},
		{"every withholdable category redacted",
			manifestfixture.Options{ChangeReport: manifestfixture.RecordedNonzero(1), Withheld: allWithheld},
			map[evidencestate.Category]evidencestate.State{
				evidencestate.Conversation: evidencestate.Redacted, evidencestate.Tool: evidencestate.Redacted,
				evidencestate.Grader: evidencestate.Redacted, evidencestate.Reliability: evidencestate.Redacted,
				evidencestate.Usage: evidencestate.Redacted,
			}, "incomplete"},
		{"usage not recorded", manifestfixture.Options{},
			map[evidencestate.Category]evidencestate.State{evidencestate.Usage: evidencestate.Unavailable}, "incomplete"},
		{"oversize tool payload", manifestfixture.Options{ChangeReport: oversizeTool, Withheld: []evidencestate.Category{}},
			map[evidencestate.Category]evidencestate.State{evidencestate.Tool: evidencestate.Incomplete}, "complete"},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend, sources, _ := setup(t)
			input, _ := publishWith(t, backend, test.options)
			report, err := Build(t.Context(), sources, []Input{input}, DefaultLimits())
			require.NoError(t, err, "honest degradation is complete membership, not a failure")
			require.Equal(t, "complete", report.State)
			member := report.Members[0]
			require.Equal(t, "admitted", member.State)
			require.Equal(t, runnerterminal.Success, member.Outcome, "degraded evidence never rewrites the outcome")
			require.Len(t, member.Categories, 6)
			for category, state := range test.degraded {
				record := categoryOf(member, category)
				require.Equal(t, state, record.State, category)
				require.Equal(t, state, record.DeclaredState, category)
				require.Nil(t, record.ObservedCount, category)
			}
			require.Equal(t, evidencestate.Present, categoryOf(member, evidencestate.Provenance).State)
			require.NotEqual(t, test.usage == "complete", report.Usage.Recorded == nil)
			if test.usage != "complete" {
				require.NotEqual(t, "complete", report.Usage.State)
			}
		})
	}
}

func TestForgedSidecarDeclarationsNeverBecomePresent(t *testing.T) {
	backend, sources, _ := setup(t)
	input, _ := publishWith(t, backend, manifestfixture.Options{ChangeReport: manifestfixture.RecordedNonzero(1)})
	inspection, err := evidencestate.Inspect(t.Context(), backend, input.Evidence)
	require.NoError(t, err)
	reasons := map[evidencestate.State]evidencestate.Reason{
		evidencestate.Unavailable: evidencestate.NotRecorded, evidencestate.Incomplete: evidencestate.PartialCapture,
		evidencestate.Redacted: evidencestate.Privacy, evidencestate.Conflicting: evidencestate.SourceConflict,
	}
	for index, original := range inspection.Document.Categories {
		for state, reason := range reasons {
			if original.State == state {
				continue
			}
			t.Run(string(original.Category)+"/"+string(state), func(t *testing.T) {
				view := *inspection.Document
				view.Categories = append([]evidencestate.Record(nil), inspection.Document.Categories...)
				record := &view.Categories[index]
				record.State, record.Reason, record.ObservedCount, record.Artifacts, record.Limit = state, reason, nil, []string{}, nil
				published, err := evidencestate.Publish(t.Context(), backend, view)
				require.NoError(t, err)
				forged := Input{Manifest: input.Manifest, Evidence: published.EvidenceSHA256}
				report, err := Build(t.Context(), sources, []Input{input, forged}, DefaultLimits())
				require.Nil(t, report.Usage.Recorded, "a degraded or conflicting population never totals usage")
				member := memberOf(t, report, forged)
				for _, category := range member.Categories {
					require.NotEqual(t, evidencestate.Present, category.State, category.Category)
				}
				if original.Category == evidencestate.Provenance {
					// Without the producer index every declaration stays unqualified.
					require.NoError(t, err)
					require.Equal(t, "admitted", member.State)
					for _, category := range member.Categories {
						require.Equal(t, "declared-state-only", category.Verification, category.Category)
					}
				} else {
					require.ErrorIs(t, err, ErrConflicting)
					require.Equal(t, "conflicting", report.State)
					require.Equal(t, "conflicting", member.State)
					require.Contains(t, member.Diagnostics, "conflicting_source")
				}
				require.Equal(t, "admitted", memberOf(t, report, input).State, "the honest neighbor is retained")
			})
		}
	}
}

// TestEvidenceStatesLabFixture produces every evidence state plus parent and
// lineage through the actual capture, ledger and aggregate paths for the Lab.
func TestEvidenceStatesLabFixture(t *testing.T) {
	backend, sources, _ := setup(t)
	original, originalExecution := publishWith(t, backend, manifestfixture.Options{
		ChangeReport: func(report *runnerunit.Report) { report.Status = "execution_error" },
	})
	retry, _ := publishWith(t, backend, manifestfixture.Options{
		ChangeReport: manifestfixture.RecordedNonzero(1), Withheld: allWithheld,
		Parent: &executionattempt.Parent{ExecutionID: originalExecution, Relationship: "retry"},
	})
	inspection, err := evidencestate.Inspect(t.Context(), backend, retry.Evidence)
	require.NoError(t, err)
	view := *inspection.Document
	view.Lineage = []evidencestate.Lineage{{Manifest: original.Manifest, Relationship: evidencestate.Resume}}
	published, err := evidencestate.Publish(t.Context(), backend, view)
	require.NoError(t, err)
	resumed := Input{Manifest: retry.Manifest, Evidence: published.EvidenceSHA256}
	oversized, _ := publishWith(t, backend, manifestfixture.Options{Withheld: []evidencestate.Category{},
		ChangeReport: func(report *runnerunit.Report) {
			report.Observation.ToolEvents = []models.ToolEvent{{Turn: 1, Sequence: 1, ToolName: "bounded", Success: true,
				Result: strings.Repeat("x", artifactmanifest.MaxArtifactBytes+1)}}
		}})
	inspection, err = evidencestate.Inspect(t.Context(), backend, oversized.Evidence)
	require.NoError(t, err)
	view = *inspection.Document
	view.Categories = append([]evidencestate.Record(nil), view.Categories...)
	for index := range view.Categories {
		if record := &view.Categories[index]; record.Category == evidencestate.Grader {
			record.State, record.Reason, record.ObservedCount, record.Artifacts = evidencestate.Redacted, evidencestate.Privacy, nil, []string{}
		}
	}
	published, err = evidencestate.Publish(t.Context(), backend, view)
	require.NoError(t, err)
	forged := Input{Manifest: oversized.Manifest, Evidence: published.EvidenceSHA256}

	inputs := []Input{original, resumed, oversized, forged}
	report, err := Build(t.Context(), sources, inputs, DefaultLimits())
	require.ErrorIs(t, err, ErrConflicting)
	states := map[evidencestate.State]bool{}
	for _, member := range report.Members {
		for _, category := range member.Categories {
			states[category.State] = true
		}
	}
	for _, state := range []evidencestate.State{evidencestate.Present, evidencestate.Unavailable,
		evidencestate.Incomplete, evidencestate.Redacted} {
		require.True(t, states[state], "fixture must exercise %s", state)
	}
	require.Equal(t, "conflicting", memberOf(t, report, forged).State)
	require.NotNil(t, memberOf(t, report, resumed).Parent)
	require.NotEmpty(t, memberOf(t, report, resumed).Lineage)
	canonical, err := Encode(*report)
	require.NoError(t, err)
	if *evidenceFixtureOutput == "" {
		return
	}
	data, err := json.Marshal(map[string]any{"inputs": inputs, "canonical": string(canonical)})
	require.NoError(t, err)
	file, err := os.OpenFile(*evidenceFixtureOutput, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	require.NoError(t, err)
	_, err = file.Write(data)
	require.NoError(t, errors.Join(err, file.Close()))
}
