package assurance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBenchmarkCorpusStructure(t *testing.T) {
	scenarios := DefaultBenchmarkScenarios()
	require.Len(t, scenarios, 12, "corpus must have exactly 12 scenarios")

	domainCounts := map[string]int{}
	classificationCounts := map[CaseClassification]int{}
	totalCases := 0

	for _, s := range scenarios {
		domainCounts[s.Domain]++
		require.NotEmpty(t, s.ID)
		require.NotEmpty(t, s.Path)
		require.NotEmpty(t, s.Field)
		require.NotEmpty(t, s.Want)
		require.NotEmpty(t, s.Wrong)
		require.NotEmpty(t, s.PrimaryTool)
		require.NotEmpty(t, s.AlternateTool)
		require.NotEmpty(t, s.ForbiddenTool)

		cases := s.Cases()
		require.Len(t, cases, 4, "each scenario must have exactly 4 cases")

		for _, c := range cases {
			totalCases++
			classificationCounts[c.Classification]++

			switch c.Kind {
			case CaseKindGood:
				require.Equal(t, CaseGood, c.Classification)
				require.True(t, c.OutcomeExpected, "good case must expect outcome pass")
				require.True(t, c.BoundaryExpected, "good case must expect boundary pass")
				require.True(t, c.OverallValid, "good case must be overall valid")
			case CaseKindAlternativeValid:
				require.Equal(t, CaseAlternative, c.Classification)
				require.True(t, c.OutcomeExpected, "alternative-valid case must expect outcome pass")
				require.True(t, c.BoundaryExpected, "alternative-valid case must expect boundary pass")
				require.True(t, c.OverallValid, "alternative-valid case must be overall valid")
			case CaseKindBadWrongState:
				require.Equal(t, CaseCriticalBad, c.Classification)
				require.False(t, c.OutcomeExpected, "bad-wrong-state must expect outcome failure")
				require.True(t, c.BoundaryExpected, "bad-wrong-state must expect boundary pass")
				require.False(t, c.OverallValid, "bad-wrong-state must be overall invalid")
			case CaseKindBadForbiddenAction:
				require.Equal(t, CaseCriticalBad, c.Classification)
				require.True(t, c.OutcomeExpected, "bad-forbidden-action must expect outcome pass")
				require.False(t, c.BoundaryExpected, "bad-forbidden-action must expect boundary failure")
				require.False(t, c.OverallValid, "bad-forbidden-action must be overall invalid")
			default:
				t.Fatalf("unexpected case kind: %s", c.Kind)
			}
		}
	}

	require.Equal(t, map[string]int{"mcp": 4, "cli": 4, "repository": 4}, domainCounts)
	require.Equal(t, 48, totalCases, "total cases must be exactly 48")
	require.Equal(t, 12, classificationCounts[CaseGood])
	require.Equal(t, 12, classificationCounts[CaseAlternative])
	require.Equal(t, 24, classificationCounts[CaseCriticalBad])
}

func TestBenchmarkCorpusObserveMechanical(t *testing.T) {
	scenarios := DefaultBenchmarkScenarios()
	validCasesTested := 0
	badCasesTested := 0

	for _, scenario := range scenarios {
		t.Run(scenario.ID, func(t *testing.T) {
			outcomeParams := scenario.OutcomeParameters()
			boundaryParams := scenario.BoundaryParameters()

			for _, candidate := range scenario.Cases() {
				t.Run(string(candidate.Kind), func(t *testing.T) {
					workspace := t.TempDir()
					require.NoError(t, candidate.PrepareWorkspace(workspace, scenario))

					gCtx := candidate.Context(workspace)

					// 1. Observe outcome grader (FileGrader) with scenario-unique identifier
					outcomeInput := ReferenceInput{
						TaskID:     scenario.Task().TestID,
						Check:      scenario.Task().Requirements[0].Checks[0],
						Parameters: outcomeParams,
						Context:    gCtx,
					}
					outcomeObs := ObserveMechanical(t.Context(), outcomeInput)
					require.Equal(t, Observed, outcomeObs.State)
					require.NoError(t, outcomeObs.Err)
					require.NotNil(t, outcomeObs.Result)
					require.Equal(t, candidate.OutcomeExpected, outcomeObs.Result.Passed)

					if candidate.OutcomeExpected {
						require.Equal(t, 1.0, outcomeObs.Result.Score)
						require.Equal(t, "All file checks passed", outcomeObs.Result.Feedback)
					} else {
						require.InDelta(t, 1.0/3, outcomeObs.Result.Score, 1e-12)
						require.Contains(t, outcomeObs.Result.Feedback, "missing expected pattern")
						require.Contains(t, outcomeObs.Result.Feedback, "contains forbidden pattern")
					}

					// 2. Observe boundary grader (ToolCallsGrader) with scenario-unique identifier
					boundaryInput := ReferenceInput{
						TaskID:     scenario.Task().TestID,
						Check:      scenario.Task().Requirements[1].Checks[0],
						Parameters: boundaryParams,
						Context:    gCtx,
					}
					boundaryObs := ObserveMechanical(t.Context(), boundaryInput)
					require.Equal(t, Observed, boundaryObs.State)
					require.NoError(t, boundaryObs.Err)
					require.NotNil(t, boundaryObs.Result)
					require.Equal(t, candidate.BoundaryExpected, boundaryObs.Result.Passed)

					if candidate.BoundaryExpected {
						require.Equal(t, 1.0, boundaryObs.Result.Score)
						require.Equal(t, "all tool_calls checks passed", boundaryObs.Result.Feedback)
					} else {
						require.Zero(t, boundaryObs.Result.Score)
						require.Contains(t, boundaryObs.Result.Feedback, fmt.Sprintf("forbidden tool %q was called", scenario.ForbiddenTool))
					}

					// 3. Composite verification
					validOverall := outcomeObs.Result.Passed && boundaryObs.Result.Passed
					require.Equal(t, candidate.OverallValid, validOverall)

					if candidate.OverallValid {
						validCasesTested++
					} else {
						badCasesTested++
					}
				})
			}
		})
	}

	require.Equal(t, 24, validCasesTested, "100% of declared valid cases (24/24) must be accepted")
	require.Equal(t, 24, badCasesTested, "100% of critical bad cases (24/24) must be rejected")
}

func TestBenchmarkCorpusReferenceAdmission(t *testing.T) {
	pkg, err := BuildBenchmarkCorpus()
	require.NoError(t, err)
	require.NotNil(t, pkg)
	require.Len(t, pkg.Document.Cases, 48)
	require.Len(t, pkg.Snapshots, 48)

	data, err := json.Marshal(pkg.Document)
	require.NoError(t, err)

	refSet, err := ParseReferences(data)
	require.NoError(t, err, "generated benchmark reference document must pass ParseReferences admission")

	doc, err := refSet.Document()
	require.NoError(t, err)
	require.Equal(t, "waza-grader-assurance-benchmark", doc.ID)
	require.Equal(t, "1.0.0", doc.Version)
	require.Len(t, doc.Domains, 3)
	require.Len(t, doc.Cases, 48)

	seenIDs := make(map[string]bool)
	seenManifests := make(map[string]bool)

	for _, c := range doc.Cases {
		require.False(t, seenIDs[c.ID], "case IDs must be unique: %s", c.ID)
		seenIDs[c.ID] = true

		require.False(t, seenManifests[c.ManifestSHA256], "manifest digests must be unique: %s", c.ManifestSHA256)
		seenManifests[c.ManifestSHA256] = true

		require.NotEmpty(t, c.Snapshot)
		require.Contains(t, pkg.Snapshots, c.Snapshot, "snapshot must exist in package")

		require.Len(t, c.Checks, 2)
		require.Equal(t, "outcome", c.Checks[0].RequirementID)
		require.Equal(t, "boundary", c.Checks[1].RequirementID)

		if c.Classification == CaseGood || c.Classification == CaseAlternative {
			require.True(t, *c.Checks[0].ExpectedPassed)
			require.True(t, *c.Checks[1].ExpectedPassed)
		} else {
			require.True(t, !*c.Checks[0].ExpectedPassed || !*c.Checks[1].ExpectedPassed,
				"critical bad cases must reject at least one check")
		}
	}
}

func TestBenchmarkCorpusDeclaredMechanical(t *testing.T) {
	scenarios := DefaultBenchmarkScenarios()

	for _, scenario := range scenarios {
		task := scenario.Task()
		spec := scenario.Spec()

		for _, candidate := range scenario.Cases() {
			workspace := t.TempDir()
			require.NoError(t, candidate.PrepareWorkspace(workspace, scenario))
			gCtx := candidate.Context(workspace)

			// ObserveDeclaredMechanical for outcome
			outcomeObs := ObserveDeclaredMechanical(t.Context(), task, spec, task.Requirements[0].Checks[0], gCtx)
			require.Equal(t, Observed, outcomeObs.State)
			require.NoError(t, outcomeObs.Err)
			require.NotNil(t, outcomeObs.Result)
			require.Equal(t, candidate.OutcomeExpected, outcomeObs.Result.Passed)

			// ObserveDeclaredMechanical for boundary
			boundaryObs := ObserveDeclaredMechanical(t.Context(), task, spec, task.Requirements[1].Checks[0], gCtx)
			require.Equal(t, Observed, boundaryObs.State)
			require.NoError(t, boundaryObs.Err)
			require.NotNil(t, boundaryObs.Result)
			require.Equal(t, candidate.BoundaryExpected, boundaryObs.Result.Passed)
		}
	}
}

func TestBenchmarkCorpusObservePreservedMechanical(t *testing.T) {
	if !preservedWorkspaceSupported() {
		t.Skip("preserved mechanical grading requires supported secure workspace materialization (Linux/macOS)")
	}
	pkg, err := BuildBenchmarkCorpus()
	require.NoError(t, err)

	validCasesTested := 0
	badCasesTested := 0

	for _, c := range pkg.Document.Cases {
		snapshotBytes, ok := pkg.Snapshots[c.Snapshot]
		require.True(t, ok, "snapshot must exist for case %s", c.ID)

		task, ok := pkg.Tasks[c.TaskID]
		require.True(t, ok, "task must exist for case %s", c.ID)

		// 1. Grade outcome check directly from serialized snapshot
		outcomeObs := ObservePreservedMechanical(t.Context(), PreservedMechanicalInput{
			Task:           task,
			Spec:           pkg.CombinedSpec,
			Check:          c.Checks[0].Check,
			SnapshotBytes:  snapshotBytes,
			ManifestSHA256: c.ManifestSHA256,
			Evidence:       c.Checks[0].Evidence,
		})
		require.Equal(t, Observed, outcomeObs.State)
		require.NoError(t, outcomeObs.Err)
		require.NotNil(t, outcomeObs.Result)
		require.Equal(t, *c.Checks[0].ExpectedPassed, outcomeObs.Result.Passed)

		// 2. Grade boundary check directly from serialized snapshot
		boundaryObs := ObservePreservedMechanical(t.Context(), PreservedMechanicalInput{
			Task:           task,
			Spec:           pkg.CombinedSpec,
			Check:          c.Checks[1].Check,
			SnapshotBytes:  snapshotBytes,
			ManifestSHA256: c.ManifestSHA256,
			Evidence:       c.Checks[1].Evidence,
		})
		require.Equal(t, Observed, boundaryObs.State)
		require.NoError(t, boundaryObs.Err)
		require.NotNil(t, boundaryObs.Result)
		require.Equal(t, *c.Checks[1].ExpectedPassed, boundaryObs.Result.Passed)

		overallPassed := outcomeObs.Result.Passed && boundaryObs.Result.Passed
		if c.Classification == CaseGood || c.Classification == CaseAlternative {
			require.True(t, overallPassed, "valid case %s must pass", c.ID)
			validCasesTested++
		} else {
			require.False(t, overallPassed, "critical bad case %s must fail", c.ID)
			badCasesTested++
		}
	}

	require.Equal(t, 24, validCasesTested, "24 valid cases must pass in preserved mechanical mode")
	require.Equal(t, 24, badCasesTested, "24 critical bad cases must fail in preserved mechanical mode")
}

func TestBenchmarkCorpusFixturePersistence(t *testing.T) {
	pkg, err := BuildBenchmarkCorpus()
	require.NoError(t, err)

	generatedData, err := json.MarshalIndent(pkg.Document, "", "  ")
	require.NoError(t, err)

	testdataDir := filepath.Join(".", "testdata")
	fixturePath := filepath.Join(testdataDir, "benchmark_corpus.json")
	if os.Getenv("REGENERATE_CORPUS") == "1" {
		require.NoError(t, os.MkdirAll(testdataDir, 0o700))
		require.NoError(t, os.WriteFile(fixturePath, generatedData, 0o600))
		for relPath, data := range pkg.Snapshots {
			snapshotPath := filepath.Join(testdataDir, relPath)
			require.NoError(t, os.MkdirAll(filepath.Dir(snapshotPath), 0o700))
			require.NoError(t, os.WriteFile(snapshotPath, data, 0o600))
		}
	}

	// Read and compare against the committed reference fixture
	committedData, err := os.ReadFile(fixturePath)
	require.NoError(t, err, "committed reference fixture must exist at %s", fixturePath)
	require.JSONEq(t, string(generatedData), string(committedData), "committed fixture must match generated corpus")

	// Verify all 48 reference snapshots exist on disk and match generated payload
	for relPath, expectedData := range pkg.Snapshots {
		snapshotPath := filepath.Join(testdataDir, relPath)
		committedSnapshot, err := os.ReadFile(snapshotPath)
		require.NoError(t, err, "committed snapshot must exist at %s", snapshotPath)
		require.Equal(t, expectedData, committedSnapshot, "committed snapshot %s must match generated payload", relPath)
	}

	// Verify the committed JSON parses and validates through ParseReferences
	refSet, err := ParseReferences(committedData)
	require.NoError(t, err)
	subject, err := refSet.Subject()
	require.NoError(t, err)
	require.Equal(t, "waza-grader-assurance-benchmark", subject.ID)

	doc, err := refSet.Document()
	require.NoError(t, err)
	require.Len(t, doc.Cases, 48)
	require.Equal(t, 16, doc.Domains[0].MinimumCases)
	require.Equal(t, 1.0, doc.Domains[0].MinimumAgreement)
}

func TestBenchmarkCorpusVerifyPreserved(t *testing.T) {
	pkg, err := BuildBenchmarkCorpus()
	require.NoError(t, err)

	executable, err := fixtureExecutableSHA256()
	require.NoError(t, err)
	inMemoryDoc := pkg.Document
	inMemoryDoc.ImplementationExecutableSHA256 = new(executable)

	// Replay directly against the committed reference fixture and snapshots root
	root, err := os.OpenRoot(filepath.Join(".", "testdata"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	req := VerifyRequest{
		Now:          now,
		EvalSource:   []byte("waza reference benchmark corpus v1.0\n"),
		Spec:         pkg.CombinedSpec,
		Tasks:        pkg.Tasks,
		SnapshotRoot: root,
		Acceptance:   ReviewSourceAcceptance{SourceID: "synthetic-source", AcceptCurrentDecision: true},
	}
	rebindSyntheticReview(t, &req, inMemoryDoc)

	report, err := VerifyPreserved(t.Context(), req)
	require.NoError(t, err)
	require.NotNil(t, report)

	if preservedWorkspaceSupported() {
		require.Equal(t, AssessmentPassed, report.State)
	} else {
		require.Equal(t, AssessmentError, report.State)
		require.Equal(t, PreservedReportVersion, report.SchemaVersion)
	}
}
