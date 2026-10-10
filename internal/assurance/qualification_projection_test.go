package assurance

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/execution"
	"github.com/stretchr/testify/require"
)

func qualificationTestPolicies(t *testing.T) ([]byte, []byte) {
	t.Helper()
	sha := strings.Repeat("a", 64)
	return marshalReferenceTest(t, qualificationCurrentnessPolicy{
			Kind: "waza.caller-currentness-profile", Version: qualificationVersion,
			Authorities: []qualificationAuthority{{"synthetic-authority", sha}}, MaxReceiptAgeSeconds: 60,
			MaxValiditySeconds: 60, MaxClockSkewSeconds: 1, CheckTimeoutSeconds: 30, VerifierBindingSHA256: sha,
		}), marshalReferenceTest(t, qualificationBackendPolicy{
			Kind: "waza.caller-journal-profile", Version: qualificationVersion, BackendID: "synthetic-backend",
			DurabilityContractSHA256: sha, ReservationPolicySHA256: sha, ReservationAuthorityID: "synthetic-reservation",
			Namespace: "synthetic", CallbackTimeoutSeconds: 30,
		})
}

func qualificationTestArm(t *testing.T, sources qualificationSources, fixture qualificationSourceFixture) (qualificationAssociation, qualificationArm) {
	t.Helper()
	set, err := qualificationDecode[qualificationSourceSet](sources.document.bytes(), qualificationEncodedLimit)
	require.NoError(t, err)
	native, err := qualificationDecode[qualificationNativeSelection](sources.native.bytes(), qualificationDocumentLimit)
	require.NoError(t, err)
	inventory := qualificationArmInventory{Tasks: []qualificationArmTask{}}
	for _, source := range set.Sources {
		switch source.Role {
		case "eval":
			inventory.EvalSourceID, inventory.EvalSourceSHA256 = source.ID, source.SHA256
		case "references":
			inventory.ReferenceSourceID, inventory.ReferenceSourceSHA256 = source.ID, source.SHA256
		case "review":
			inventory.ReviewSourceID, inventory.ReviewSourceSHA256 = source.ID, source.SHA256
		case "task":
			declaration, err := qualificationCanonical(native.Tasks[0].Declaration)
			require.NoError(t, err)
			selectors := []qualificationSelector{}
			for _, candidate := range fixture.document.Cases {
				for _, check := range candidate.Checks {
					selectors = append(selectors, qualificationSelector{
						ArmID: "arm", TaskID: candidate.TaskID, RequirementID: check.RequirementID,
						Check: qualificationCheck{check.Check.Scope, check.Check.AfterTurn, check.Check.Grader}, CaseID: candidate.ID,
					})
				}
			}
			slices.SortFunc(selectors, qualificationCompareSelectors)
			inventory.Tasks = append(inventory.Tasks, qualificationArmTask{
				TaskID: source.TaskID, SourceID: source.ID, TaskSourceSHA256: source.SHA256,
				TaskDeclarationSHA256: byteSHA256(declaration), Selectors: selectors,
			})
		}
	}
	spec, err := qualificationCanonical(native.ResolvedSpec)
	require.NoError(t, err)
	inventory.ResolvedSpecSHA256 = byteSHA256(spec)
	inventoryDocument, err := qualificationSeal(inventory)
	require.NoError(t, err)
	association := qualificationAssociation{
		Kind: "waza.qualification-association", Version: qualificationVersion,
		ContractSHA256: strings.Repeat("a", 64), CID: "synthetic-cid", ArmID: "arm",
		ArmSourceInventorySHA256: inventoryDocument.sha256(), Selectors: inventory.Tasks[0].Selectors,
	}
	arm := qualificationArm{"waza.qualification-arm-input", qualificationVersion, association.ContractSHA256, association.CID, association.ArmID, inventory}
	return association, arm
}

func TestQualificationInputsBindAcquiredReviewNativeTasksAndArm(t *testing.T) {
	fixture := qualificationSourceFixtureForTest(t)
	sources, err := qualificationAcquireSources(t.Context(), fixture.reads)
	require.NoError(t, err)
	association, arm := qualificationTestArm(t, sources, fixture)
	currentness, backend := qualificationTestPolicies(t)
	input, err := qualificationBuildInputs(sources, marshalReferenceTest(t, association), marshalReferenceTest(t, arm),
		fixture.request.Acceptance, fixture.request.Now, currentness, backend)
	require.NoError(t, err)
	admitted, err := qualificationParseInputs(input.bytes(), sources)
	require.NoError(t, err)
	require.Equal(t, input.canonical, admitted.canonical)
	digest, err := evidence.JSONDigest(json.RawMessage(input.bytes()))
	require.NoError(t, err)
	require.Equal(t, digest.SHA256, input.sha256())
	var supplied qualificationInputs
	require.NoError(t, json.Unmarshal(input.bytes(), &supplied))
	for _, mutate := range []func(*qualificationInputs){
		func(v *qualificationInputs) { v.Sources[0].SHA256 = strings.Repeat("0", 64) },
		func(v *qualificationInputs) { v.SuppliedReview = json.RawMessage(`{}`) },
		func(v *qualificationInputs) { v.ResolvedSpec = json.RawMessage(`{}`) },
		func(v *qualificationInputs) { v.Tasks[0].Declaration = json.RawMessage(`{}`) },
		func(v *qualificationInputs) {
			v.MechanicalObservations = []qualificationMechanicalObservation{{association.Selectors[0], json.RawMessage(`{}`)}}
		},
		func(v *qualificationInputs) { v.AdmissionTime = "2026-10-09T00:00:00+00:00" },
		func(v *qualificationInputs) { v.Association.ArmID = "other" },
	} {
		copy, err := calibrationJSONCopy(supplied)
		require.NoError(t, err)
		mutate(&copy)
		_, err = qualificationParseInputs(marshalReferenceTest(t, copy), sources)
		require.Error(t, err)
	}
	parsedOnly, err := qualificationParseSources(sources.document.bytes())
	require.NoError(t, err)
	_, err = qualificationParseInputs(input.bytes(), parsedOnly)
	require.ErrorContains(t, err, "actual rooted")
}

func TestQualificationInputsRejectInventoryMetadataAndSelectorSubstitution(t *testing.T) {
	fixture := qualificationSourceFixtureForTest(t)
	sources, err := qualificationAcquireSources(t.Context(), fixture.reads)
	require.NoError(t, err)
	originalAssociation, originalArm := qualificationTestArm(t, sources, fixture)
	currentness, backend := qualificationTestPolicies(t)
	for _, failure := range []string{"contract", "kind", "review bytes", "task source", "task declaration", "spec",
		"selector omitted", "selector renamed", "selector duplicate", "cross arm", "requirement", "grader", "afterturn", "inventory digest", "unaccepted review", "zero time"} {
		t.Run(failure, func(t *testing.T) {
			association, err := calibrationJSONCopy(originalAssociation)
			require.NoError(t, err)
			arm, err := calibrationJSONCopy(originalArm)
			require.NoError(t, err)
			acceptance, now := fixture.request.Acceptance, fixture.request.Now
			switch failure {
			case "contract":
				arm.ContractSHA256 = strings.Repeat("b", 64)
			case "kind":
				arm.Kind = "other"
			case "review bytes":
				arm.SourceInventory.ReviewSourceSHA256 = strings.Repeat("b", 64)
			case "task source":
				arm.SourceInventory.Tasks[0].TaskSourceSHA256 = strings.Repeat("b", 64)
			case "task declaration":
				arm.SourceInventory.Tasks[0].TaskDeclarationSHA256 = strings.Repeat("b", 64)
			case "spec":
				arm.SourceInventory.ResolvedSpecSHA256 = strings.Repeat("b", 64)
			case "selector omitted":
				association.Selectors = association.Selectors[:1]
			case "selector renamed":
				association.Selectors[0].CaseID = "renamed"
			case "selector duplicate":
				association.Selectors[1] = association.Selectors[0]
			case "cross arm":
				arm.SourceInventory.Tasks[0].Selectors[0].ArmID = "other"
			case "requirement":
				arm.SourceInventory.Tasks[0].Selectors[0].RequirementID = "other"
			case "grader":
				arm.SourceInventory.Tasks[0].Selectors[0].Check.Grader = "other"
			case "afterturn":
				arm.SourceInventory.Tasks[0].Selectors[0].Check.AfterTurn = 1
			case "inventory digest":
				association.ArmSourceInventorySHA256 = strings.Repeat("0", 64)
			case "unaccepted review":
				acceptance.AcceptCurrentDecision = false
			case "zero time":
				now = time.Time{}
			}
			_, err = qualificationBuildInputs(sources, marshalReferenceTest(t, association), marshalReferenceTest(t, arm),
				acceptance, now, currentness, backend)
			require.Error(t, err)
		})
	}
}

func TestQualificationPolicyParsersAreStrictNotAuthorityProof(t *testing.T) {
	currentness, backend := qualificationTestPolicies(t)
	_, err := qualificationParseCurrentnessPolicy(currentness)
	require.NoError(t, err)
	_, err = qualificationParseBackendPolicy(backend)
	require.NoError(t, err)
	for _, key := range []string{"max_receipt_age_seconds", "max_validity_seconds", "check_timeout_seconds"} {
		var policy map[string]any
		require.NoError(t, json.Unmarshal(currentness, &policy))
		policy[key] = 0
		_, err = qualificationParseCurrentnessPolicy(marshalReferenceTest(t, policy))
		require.Error(t, err)
		policy[key] = 3601
		_, err = qualificationParseCurrentnessPolicy(marshalReferenceTest(t, policy))
		require.Error(t, err)
	}
	for _, mutate := range []func(*qualificationCurrentnessPolicy){
		func(p *qualificationCurrentnessPolicy) { p.Kind = "other" },
		func(p *qualificationCurrentnessPolicy) { p.Version = "2.0" },
		func(p *qualificationCurrentnessPolicy) { p.Authorities = nil },
		func(p *qualificationCurrentnessPolicy) { p.Authorities = append(p.Authorities, p.Authorities[0]) },
		func(p *qualificationCurrentnessPolicy) { p.Authorities[0].AuthorityID = "" },
		func(p *qualificationCurrentnessPolicy) { p.Authorities[0].VerificationBindingSHA256 = "fake" },
		func(p *qualificationCurrentnessPolicy) { p.VerifierBindingSHA256 = "fake" },
		func(p *qualificationCurrentnessPolicy) { p.MaxClockSkewSeconds = 301 },
	} {
		var policy qualificationCurrentnessPolicy
		require.NoError(t, json.Unmarshal(currentness, &policy))
		mutate(&policy)
		_, err := qualificationParseCurrentnessPolicy(marshalReferenceTest(t, policy))
		require.Error(t, err)
	}
	for _, mutate := range []func(*qualificationBackendPolicy){
		func(p *qualificationBackendPolicy) { p.Kind = "other" },
		func(p *qualificationBackendPolicy) { p.BackendID = "" },
		func(p *qualificationBackendPolicy) { p.Namespace = "" },
		func(p *qualificationBackendPolicy) { p.DurabilityContractSHA256 = "fake" },
		func(p *qualificationBackendPolicy) { p.ReservationPolicySHA256 = "fake" },
		func(p *qualificationBackendPolicy) { p.ReservationAuthorityID = "" },
		func(p *qualificationBackendPolicy) { p.CallbackTimeoutSeconds = 0 },
		func(p *qualificationBackendPolicy) { p.CallbackTimeoutSeconds = 121 },
	} {
		var policy qualificationBackendPolicy
		require.NoError(t, json.Unmarshal(backend, &policy))
		mutate(&policy)
		_, err := qualificationParseBackendPolicy(marshalReferenceTest(t, policy))
		require.Error(t, err)
	}
}

func TestQualificationActualNativeCaptureOnlyByteEquivalence(t *testing.T) {
	request, _, _, harness := calibrationFixture(t)
	store, err := newCalibrationRubricStore()
	require.NoError(t, err)
	defer func() { require.NoError(t, store.close()) }()
	for _, output := range []string{"", " \n\t", "é \"exact\""} {
		capture := &qualificationCapture{}
		job := calibrationJob{config: request.Spec.Graders[0], task: request.Tasks["task"], output: output,
			rubric: []byte(calibrationTestRubric), judgeModel: calibrationTestModel, judgeEffort: "low"}
		_, err := runCalibrationGrade(t.Context(), job, store, capture)
		require.ErrorIs(t, err, errCalibrationCapture)
		require.Equal(t, 1, capture.calls)
		require.Contains(t, capture.document.canonical, `"Name":"set_waza_grade_pass"`)
		require.Contains(t, capture.document.canonical, `"Policy":["custom:set_waza_grade_pass","custom:set_waza_grade_fail"]`)
		require.NotContains(t, capture.document.canonical, `"name":`)
		original := &calibrationCapture{}
		_, err = runCalibrationGrade(t.Context(), job, store, original)
		require.ErrorIs(t, err, errCalibrationCapture)
		require.Equal(t, original.identity, capture.document.sha256())
		control := qualificationTestCaptureExecutor{inspect: func(actual *execution.ExecutionRequest) error {
			type originalTool struct {
				Name, Description string
				Parameters        any
			}
			tools := []originalTool{}
			for _, tool := range actual.Tools {
				tools = append(tools, originalTool{tool.Name, tool.Description, tool.Parameters})
			}
			frozenProjection := struct {
				Message, Model, Reasoning string
				Tools                     []originalTool
				Policy                    []string
			}{actual.Message, actual.ModelID, actual.ReasoningEffort, tools,
				[]string{"custom:set_waza_grade_pass", "custom:set_waza_grade_fail"}}
			originalBytes, err := qualificationSeal(frozenProjection)
			require.NoError(t, err)
			detached, err := qualificationCaptureProjection(actual)
			require.NoError(t, err)
			require.Equal(t, originalBytes.bytes(), detached.bytes(), "actual frozen projection byte control")
			reversed := []string{"custom:set_waza_grade_fail", "custom:set_waza_grade_pass"}
			actual.ToolPolicy = execution.NewToolPolicy(&reversed)
			equivalent, err := qualificationCaptureProjection(actual)
			require.NoError(t, err)
			require.Equal(t, detached.bytes(), equivalent.bytes())
			actual.Tools[0].Parameters = map[string]any{"exact": json.Number("9007199254740993")}
			exact, err := qualificationCaptureProjection(actual)
			require.NoError(t, err)
			require.Contains(t, exact.canonical, "9007199254740993")
			denied := []string{"custom:set_waza_grade_pass"}
			actual.ToolPolicy = execution.NewToolPolicy(&denied)
			_, err = qualificationCaptureProjection(actual)
			require.Error(t, err)
			return nil
		}}
		_, err = runCalibrationGrade(t.Context(), job, store, control)
		require.ErrorIs(t, err, errCalibrationCapture)
	}

	require.Zero(t, harness.factories)
	require.Zero(t, harness.notices)
	_, err = qualificationCaptureProjection(nil)
	require.Error(t, err)
	var capture qualificationCapture
	_, err = capture.Execute(context.Background(), nil)
	require.Error(t, err)
	require.NotEqual(t, errCalibrationCapture, err)
}

func TestQualificationCaptureConstructorBudgetBeforeSerialization(t *testing.T) {
	request, _, _, harness := calibrationFixture(t)
	store, err := newCalibrationRubricStore()
	require.NoError(t, err)
	defer func() { require.NoError(t, store.close()) }()
	control := qualificationTestCaptureExecutor{inspect: func(actual *execution.ExecutionRequest) error {
		baseline, err := qualificationCaptureProjection(actual)
		require.NoError(t, err)
		reached := false
		document, err := qualificationCaptureProjectionBounded(actual, len(baseline.bytes()), func() { reached = true })
		require.NoError(t, err)
		require.True(t, reached)
		require.Equal(t, baseline.bytes(), document.bytes())
		reached = false
		document, err = qualificationCaptureProjectionBounded(actual, len(baseline.bytes())-1, func() { reached = true })
		require.Error(t, err)
		require.False(t, reached)
		require.Empty(t, document.canonical)
		actual.Message = strings.Repeat("x", len(baseline.bytes())+1)
		reached = false
		document, err = qualificationCaptureProjectionBounded(actual, len(baseline.bytes()), func() { reached = true })
		require.Error(t, err)
		require.False(t, reached, "oversized actual capture must not reach identity/JSON serialization")
		require.Empty(t, document.canonical)
		return nil
	}}
	job := calibrationJob{config: request.Spec.Graders[0], task: request.Tasks["task"], output: "é",
		rubric: []byte(calibrationTestRubric), judgeModel: calibrationTestModel}
	_, err = runCalibrationGrade(t.Context(), job, store, control)
	require.ErrorIs(t, err, errCalibrationCapture)
	require.Zero(t, harness.factories)
}

func TestQualificationExecutionViewHasOnlyLabelFreeBoundaryFields(t *testing.T) {
	jobSHA := strings.Repeat("a", 64)
	model := "synthetic-é"
	view, err := qualificationNewExecutionView(jobSHA, model)
	require.NoError(t, err)
	expected := `{"boundary_profile":"native-independent-two-callbacks-v1","kind":"waza.qualification-execution-view","requested_model":"synthetic-é","scoped_job_sha256":"` + jobSHA + `","version":"1.0"}`
	require.Equal(t, expected, string(view.bytes()))
	require.Equal(t, jobSHA, view.scopedJobSHA256())
	require.Equal(t, model, view.requestedModel())
	require.Equal(t, qualificationBoundary, view.boundaryProfile())
	bytes := view.bytes()
	bytes[0] = '!'
	copy := view
	copy.canonical, copy.model, copy.jobSHA = "changed", "changed", "changed"
	require.Equal(t, "changed", string(copy.bytes()))
	require.Equal(t, "changed", copy.requestedModel())
	require.Equal(t, "changed", copy.scopedJobSHA256())
	require.Equal(t, expected, string(view.bytes()))
	var wire map[string]any
	require.NoError(t, json.Unmarshal(view.bytes(), &wire))
	require.Len(t, wire, 5)
	for _, field := range []string{"expected_passed", "classification", "selector", "case_id", "reference", "native_request", "canonical_json", "job", "sources"} {
		require.NotContains(t, wire, field)
	}
	for _, values := range [][2]string{{"", model}, {strings.Repeat("g", 64), model}, {jobSHA, ""}, {jobSHA, "bad\nmodel"}, {jobSHA, strings.Repeat("x", 257)}} {
		_, err := qualificationNewExecutionView(values[0], values[1])
		require.Error(t, err)
	}
	require.Empty(t, (qualificationExecutionView{}).boundaryProfile())
}

type qualificationTestCaptureExecutor struct {
	inspect func(*execution.ExecutionRequest) error
}

func (executor qualificationTestCaptureExecutor) Execute(_ context.Context, request *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	if err := executor.inspect(request); err != nil {
		return nil, err
	}
	return nil, errCalibrationCapture
}

func TestQualificationOriginalReportHelperRejectsBeforeMaterialization(t *testing.T) {
	report := calibratedClaimFixture(t) // Explicit synthetic fixture; no engine.
	original, err := json.MarshalIndent(report, "", "  ")
	require.NoError(t, err)
	original = append(original, '\n')
	for _, failure := range []string{"boundary", "role+1", "declared+1", "role", "encoding", "digest", "base64", "ordinal"} {
		t.Run(failure, func(t *testing.T) {
			artifact := qualificationArtifact{"actual_calibration_report", nil, "source-bytes-sha256",
				uint64(len(original)), byteSHA256(original), base64.StdEncoding.EncodeToString(original)}
			limit := uint64(len(original))
			switch failure {
			case "role+1":
				limit--
			case "declared+1":
				artifact.ByteLength++
			case "role":
				artifact.Role = "core_journal"
			case "encoding":
				artifact.Encoding = "json-v1"
			case "digest":
				artifact.SHA256 = "invalid"
			case "base64":
				artifact.BytesBase64 = "AB=="
			case "ordinal":
				artifact.Ordinal = new(uint64(0))
			}
			reached := false
			actual, err := qualificationOriginalReportBounded(artifact, limit, func() { reached = true })
			if failure == "boundary" {
				require.NoError(t, err)
				require.True(t, reached)
				require.Equal(t, report.State, actual.State)
			} else {
				require.Error(t, err)
				require.False(t, reached, "actual helper must not serialize an envelope or decode oversized payloads")
				require.Nil(t, actual)
			}
		})
	}
}

func TestQualificationStandaloneReportUsesOriginalBytesNotEmbedding(t *testing.T) {
	report := calibratedClaimFixture(t) // Explicit synthetic claim, no engine.
	original, err := json.MarshalIndent(report, "", "  ")
	require.NoError(t, err)
	original = append(original, '\n')
	artifact := qualificationArtifact{"actual_calibration_report", nil, "source-bytes-sha256",
		uint64(len(original)), byteSHA256(original), base64.StdEncoding.EncodeToString(original)}
	parsed, err := qualificationOriginalReport(artifact)
	require.NoError(t, err)
	require.Equal(t, report.State, parsed.State)
	embedding, err := json.Marshal(struct {
		Report json.RawMessage `json:"report"`
	}{original})
	require.NoError(t, err)
	require.NotEqual(t, artifact.SHA256, byteSHA256(embedding))
	canonical, err := qualificationCanonical(original)
	require.NoError(t, err)
	artifact.SHA256 = byteSHA256(canonical)
	_, err = qualificationOriginalReport(artifact)
	require.Error(t, err)
	artifact.BytesBase64 = base64.StdEncoding.EncodeToString([]byte(`{}`))
	artifact.ByteLength = 2
	artifact.SHA256 = byteSHA256([]byte(`{}`))
	_, err = qualificationOriginalReport(artifact)
	require.Error(t, err)
	require.False(t, errors.Is(err, errCalibrationCapture))
}
