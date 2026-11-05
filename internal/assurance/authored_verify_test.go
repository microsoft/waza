package assurance

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func authoredVerificationFixture(t *testing.T) (VerifyRequest, ReferenceDocument, string) {
	t.Helper()
	request, document, root := fileVerificationFixture(t)
	request.Spec.Graders[0].Kind = models.GraderKindText
	request.Spec.Graders[0].Parameters = models.TextGraderParameters{
		RegexMatch: []string{`(?m)^state:\s*ready$`}, RegexNotMatch: []string{`forbidden:\s*yes`},
	}
	config, err := evidence.JSONDigest(request.Spec)
	require.NoError(t, err)
	declaration, err := evidence.JSONDigest(request.Spec.Graders[0])
	require.NoError(t, err)
	document.EvalResolvedConfigSHA256 = config.SHA256
	for index, output := range []string{
		"state: ready\n", "note: alternate explanation\nstate: ready\n",
		"state: wrong\n", "state: ready\nforbidden: yes\n",
	} {
		candidate := &document.Cases[index]
		input, err := NewAuthoredOutput(document.ID, candidate.ID, candidate.TaskID, index+1, new(output))
		require.NoError(t, err)
		name := candidate.ID + "-authored.json"
		require.NoError(t, os.WriteFile(filepath.Join(root, name), input.Document(), 0o600))
		candidate.Snapshot = ""
		candidate.AuthoredInput = &AuthoredSource{Path: name, DocumentSHA256: byteSHA256(input.Document())}
		candidate.ManifestSHA256 = input.Manifest().SHA256
		reference, err := evidence.Reference(input.Manifest(), "authored-output")
		require.NoError(t, err)
		reference.Pointer = "/output"
		candidate.Checks[0].Evidence = []models.EvidenceReference{reference}
		candidate.Checks[0].GraderDeclarationSHA256 = declaration.SHA256
	}
	rebindSyntheticReview(t, &request, document)
	return request, document, root
}

func portableAuthoredVerificationFixture(t *testing.T) (VerifyRequest, ReferenceDocument, string) {
	t.Helper()
	task := &models.TestCase{TestID: "task", DisplayName: "Finite state", Requirements: []models.Requirement{{
		ID: "state", Category: "outcome", Description: "State is ready without forbidden content.",
		Checks: []models.RequirementCheck{{Scope: "eval", Grader: "check"}},
	}}}
	spec := &models.EvalSpec{Graders: []models.GraderConfig{{
		Identifier: "check", Kind: models.GraderKindText,
		Parameters: models.TextGraderParameters{
			RegexMatch: []string{`(?m)^state:\s*ready$`}, RegexNotMatch: []string{`forbidden:\s*yes`},
		},
	}}}
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	config, err := evidence.JSONDigest(spec)
	require.NoError(t, err)
	taskDigest, err := evidence.JSONDigest(task)
	require.NoError(t, err)
	declaration, err := evidence.JSONDigest(spec.Graders[0])
	require.NoError(t, err)
	executable, err := fixtureExecutableSHA256()
	require.NoError(t, err)
	source := []byte("exact evaluator-owned source bytes")
	document := admissionDocument()
	document.EvalSourceSHA256, document.EvalResolvedConfigSHA256 = byteSHA256(source), config.SHA256
	document.ImplementationExecutableSHA256 = new(executable)
	document.Cases = nil
	for index, candidate := range []struct {
		id             string
		classification CaseClassification
		output         string
		expected       bool
	}{
		{"good", CaseGood, "state: ready\n", true},
		{"alternative", CaseAlternative, "note: alternate explanation\nstate: ready\n", true},
		{"wrong-state", CaseCriticalBad, "state: wrong\n", false},
		{"forbidden-state", CaseCriticalBad, "state: ready\nforbidden: yes\n", false},
	} {
		input, err := NewAuthoredOutput(document.ID, candidate.id, task.TestID, index+1, new(candidate.output))
		require.NoError(t, err)
		name := candidate.id + "-authored.json"
		require.NoError(t, os.WriteFile(filepath.Join(rootPath, name), input.Document(), 0o600))
		reference, err := evidence.Reference(input.Manifest(), "authored-output")
		require.NoError(t, err)
		reference.Pointer = "/output"
		document.Cases = append(document.Cases, ReferenceCase{
			ID: candidate.id, ScenarioID: "target", Domain: "cli", Classification: candidate.classification,
			TaskID: task.TestID, TaskDeclarationSHA256: taskDigest.SHA256,
			AuthoredInput:  &AuthoredSource{Path: name, DocumentSHA256: byteSHA256(input.Document())},
			ManifestSHA256: input.Manifest().SHA256,
			Checks: []ReferenceCheck{{
				RequirementID: "state", Check: task.Requirements[0].Checks[0],
				GraderDeclarationSHA256: declaration.SHA256, ExpectedPassed: new(candidate.expected),
				Evidence: []models.EvidenceReference{reference},
			}},
		})
	}
	request := VerifyRequest{
		Now:        time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC),
		EvalSource: source, Spec: spec, Tasks: map[string]*models.TestCase{task.TestID: task},
		SnapshotRoot: root, Acceptance: ReviewSourceAcceptance{SourceID: "synthetic-source", AcceptCurrentDecision: true},
	}
	rebindSyntheticReview(t, &request, document)
	return request, document, rootPath
}

func TestVerifyFiniteAuthoredOutputUsesActualNativeTextVerdicts(t *testing.T) {
	request, document, root := portableAuthoredVerificationFixture(t)
	files, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Len(t, files, 4)
	report, err := Verify(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, AssessmentPassed, report.State)
	require.True(t, report.Review.Eligible)
	require.Len(t, report.Requirements, 1)
	require.Len(t, report.Requirements[0].Observations, 4)
	require.Equal(t, Coverage{Good: 1, AlternativeValid: 1, CriticalBad: 2, IntendedNegative: 2}, report.Requirements[0].Observed)
	for index, observation := range report.Requirements[0].Observations {
		candidate := document.Cases[index]
		require.Empty(t, candidate.Snapshot)
		data, err := os.ReadFile(filepath.Join(root, candidate.AuthoredInput.Path))
		require.NoError(t, err)
		require.Equal(t, candidate.AuthoredInput.DocumentSHA256, byteSHA256(data))
		require.Equal(t, Observed, observation.State)
		require.Equal(t, "authored_finite_output", observation.SourceScope)
		require.NotNil(t, observation.Result)
		require.Equal(t, models.GraderKindText, observation.Result.Type)
		require.Equal(t, *document.Cases[index].Checks[0].ExpectedPassed, observation.Result.Passed)
		require.True(t, *observation.Agreement)
		require.NotEmpty(t, observation.Result.Feedback)
	}
	require.Nil(t, report.Calibration.Usage)
	require.Nil(t, report.Calibration.Credits)
	require.Zero(t, report.Calibration.Executions)
}

func TestVerifyFiniteAuthoredOutputUsesActualNativeInlineSchemaVerdicts(t *testing.T) {
	request, document, root := portableAuthoredVerificationFixture(t)
	request.Spec.Graders[0].Kind = models.GraderKindJSONSchema
	request.Spec.Graders[0].Parameters = models.JSONSchemaGraderParameters{Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"state": map[string]any{"enum": []any{"ready"}},
		},
		"required": []any{"state"},
	}}
	config, err := evidence.JSONDigest(request.Spec)
	require.NoError(t, err)
	declaration, err := evidence.JSONDigest(request.Spec.Graders[0])
	require.NoError(t, err)
	document.EvalResolvedConfigSHA256 = config.SHA256
	for index, output := range []string{
		`{"state":"ready"}`, `{"note":"alternative","state":"ready"}`,
		`{"state":"wrong"}`, `not-json`,
	} {
		candidate := &document.Cases[index]
		input, err := NewAuthoredOutput(document.ID, candidate.ID, candidate.TaskID, index+1, new(output))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(root, candidate.AuthoredInput.Path), input.Document(), 0o600))
		candidate.AuthoredInput.DocumentSHA256 = byteSHA256(input.Document())
		candidate.ManifestSHA256 = input.Manifest().SHA256
		reference, err := evidence.Reference(input.Manifest(), "authored-output")
		require.NoError(t, err)
		reference.Pointer = "/output"
		candidate.Checks[0].Evidence = []models.EvidenceReference{reference}
		candidate.Checks[0].GraderDeclarationSHA256 = declaration.SHA256
	}
	rebindSyntheticReview(t, &request, document)
	report, err := Verify(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, AssessmentPassed, report.State)
	require.True(t, report.Review.Eligible)
	require.Len(t, report.Requirements, 1)
	require.Len(t, report.Requirements[0].Observations, 4)
	require.Equal(t, Coverage{Good: 1, AlternativeValid: 1, CriticalBad: 2, IntendedNegative: 2}, report.Requirements[0].Observed)
	for index, observation := range report.Requirements[0].Observations {
		require.Equal(t, Observed, observation.State)
		require.Equal(t, "authored_finite_output", observation.SourceScope)
		require.NotNil(t, observation.Result)
		require.Equal(t, models.GraderKindJSONSchema, observation.Result.Type)
		require.Equal(t, *document.Cases[index].Checks[0].ExpectedPassed, observation.Result.Passed)
		require.Equal(t, float64(1-index/2), observation.Result.Score)
		require.True(t, *observation.Agreement)
		require.Contains(t, observation.Result.Feedback, []string{
			"Output matches JSON schema", "Output matches JSON schema",
			"Schema validation failed", "Output is not valid JSON",
		}[index])
	}
	require.Nil(t, report.Calibration.Usage)
	require.Nil(t, report.Calibration.Credits)
	require.Zero(t, report.Calibration.Executions)
	require.Zero(t, report.Calibration.Samples)
	require.Nil(t, report.Calibration.ExecutionLedger)
	for _, binding := range report.Bindings {
		require.Equal(t, "verified", binding.State)
	}
	for schema, value := range map[string]any{
		"grader-reference-1.0.schema.json": document,
		"grader-assurance-1.0.schema.json": report,
	} {
		var object map[string]any
		require.NoError(t, json.Unmarshal(marshalReferenceTest(t, value), &object))
		require.NoError(t, assuranceSchema(t, schema).Validate(object))
	}
}

func TestVerifyAuthoredReviewAndNativeBindingsRemainRequired(t *testing.T) {
	for _, mutation := range []string{"review", "source", "config", "task", "grader"} {
		t.Run(mutation, func(t *testing.T) {
			request, document, _ := portableAuthoredVerificationFixture(t)
			switch mutation {
			case "review":
				request.Acceptance.AcceptCurrentDecision = false
			case "source":
				request.EvalSource = []byte("other source bytes")
			case "config":
				request.Spec.Name = "other-config"
			case "task":
				request.Tasks["task"].DisplayName = "Other task"
			case "grader":
				request.Spec.Graders[0].Parameters = models.TextGraderParameters{RegexMatch: []string{"other"}}
			}
			rebindSyntheticReview(t, &request, document)
			report, err := Verify(t.Context(), request)
			require.NoError(t, err)
			if mutation == "review" {
				require.Equal(t, AssessmentNotAssessed, report.State)
				require.False(t, report.Review.Eligible)
			} else {
				require.Equal(t, AssessmentInvalid, report.State)
				require.Equal(t, "binding_mismatch", report.Reason)
			}
			require.Nil(t, report.Calibration.Usage)
			require.Nil(t, report.Calibration.Credits)
			require.Zero(t, report.Calibration.Executions)
		})
	}
}

func TestVerifyAuthoredEmptyWhitespaceAndUnavailableAreDistinct(t *testing.T) {
	for _, output := range []*string{new(""), new(" \n\t"), nil} {
		request, document, root := portableAuthoredVerificationFixture(t)
		candidate := &document.Cases[2]
		input, err := NewAuthoredOutput(document.ID, candidate.ID, candidate.TaskID, 3, output)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(root, candidate.AuthoredInput.Path), input.Document(), 0o600))
		candidate.AuthoredInput.DocumentSHA256 = byteSHA256(input.Document())
		candidate.ManifestSHA256 = input.Manifest().SHA256
		rebindSyntheticReview(t, &request, document)
		report, err := Verify(t.Context(), request)
		require.NoError(t, err)
		observation := report.Requirements[0].Observations[2]
		if output == nil {
			require.Equal(t, InsufficientEvidence, observation.State)
			require.Nil(t, observation.Result)
			require.NotEqual(t, AssessmentPassed, report.State)
		} else {
			require.Equal(t, Observed, observation.State)
			require.False(t, observation.Result.Passed)
			require.True(t, *observation.Agreement)
			require.Equal(t, AssessmentPassed, report.State)
		}
	}
}

func TestVerifyAuthoredSourceRequiresExactEnvelopeAndProfileBinding(t *testing.T) {
	for _, mutation := range []string{"bytes", "case", "pointer", "missing"} {
		t.Run(mutation, func(t *testing.T) {
			request, document, root := portableAuthoredVerificationFixture(t)
			candidate := &document.Cases[0]
			switch mutation {
			case "bytes":
				data, err := os.ReadFile(filepath.Join(root, candidate.AuthoredInput.Path))
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(root, candidate.AuthoredInput.Path), append(data, '\n'), 0o600))
			case "case":
				candidate.ID = "other"
			case "pointer":
				candidate.Checks[0].Evidence[0].Pointer = ""
			case "missing":
				require.NoError(t, os.Remove(filepath.Join(root, candidate.AuthoredInput.Path)))
			}
			rebindSyntheticReview(t, &request, document)
			report, err := Verify(t.Context(), request)
			require.NoError(t, err)
			require.NotEqual(t, AssessmentPassed, report.State)
			require.Nil(t, report.Requirements[0].Observations[0].Result)
		})
	}
}

func TestNativeAuthoredTestsUsePortableFixture(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "authored_verify_test.go", nil, 0)
	require.NoError(t, err)
	tests := 0
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || !strings.HasPrefix(function.Name.Name, "TestVerify") {
			continue
		}
		tests++
		t.Run(function.Name.Name, func(t *testing.T) {
			portable := false
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				name, ok := call.Fun.(*ast.Ident)
				if !ok {
					return true
				}
				require.NotEqual(t, "authoredVerificationFixture", name.Name,
					"native authored tests must not inherit preserved-file platform admission")
				if name.Name == "portableAuthoredVerificationFixture" {
					portable = true
				}
				return true
			})
			require.True(t, portable, "native authored tests must explicitly select the portable fixture")
		})
	}
	require.GreaterOrEqual(t, tests, 5)
}
