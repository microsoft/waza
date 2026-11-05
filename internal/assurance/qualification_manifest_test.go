package assurance

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func qualificationProtocolFixture(t *testing.T) (qualificationManifest, qualificationSourceFixture) {
	t.Helper()
	fixture := qualificationSourceFixtureForTest(t)
	fixture.request.Spec.Graders[0].Kind = models.GraderKindPrompt
	fixture.request.Spec.Graders[0].Parameters = models.PromptGraderParameters{
		Rubric: "rubric.md", Mode: models.PromptGraderModeIndependent,
	}
	rubric := []byte(calibrationTestRubric)
	require.NoError(t, os.WriteFile(filepath.Join(fixture.root, "rubric.md"), rubric, 0o600))
	evalBytes, err := yaml.Marshal(fixture.request.Spec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(fixture.root, "eval.yaml"), evalBytes, 0o600))
	fixture.request.Spec, err = models.LoadEvalSpecOffline(filepath.Join(fixture.root, "eval.yaml"))
	require.NoError(t, err)
	fixture.document.EvalSourceSHA256 = byteSHA256(evalBytes)
	fixture.document.Calibration = &CalibrationPlan{Protocol: AgreementProtocol, Model: calibrationTestModel, MaxJudgeExecutions: 4}
	for i := range fixture.document.Cases {
		fixture.document.Cases[i].Checks[0].RubricContentSHA256 = new(byteSHA256(rubric))
		selector := *fixture.reads[5+i].Selector
		fixture.reads = append(fixture.reads, qualificationSourceRead{ID: "rubric-" + selector.CaseID,
			Role: "rubric", TaskID: selector.TaskID, Selector: &selector, Root: fixture.request.SnapshotRoot, Path: "rubric.md"})
	}
	calibrationRebind(t, &fixture.request, &fixture.document)
	labels := marshalReferenceTest(t, fixture.document)
	review := marshalReferenceTest(t, ReviewDocument{SchemaVersion: ReferenceVersion, Kind: ReviewKind,
		SourceID: fixture.request.Review.SourceID, SubjectID: fixture.document.ID, SubjectVersion: fixture.document.Version,
		LabelsSHA256: byteSHA256(labels), State: ReviewReviewed, Reviewer: "synthetic-test-reviewer",
		ReviewedAt: fixture.request.Review.Decision.ReviewedAt})
	require.NoError(t, os.WriteFile(filepath.Join(fixture.root, "labels.json"), labels, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(fixture.root, "review.json"), append(review, '\n'), 0o600))
	sources, err := qualificationAcquireSources(t.Context(), fixture.reads)
	require.NoError(t, err)
	association, arm := qualificationTestArm(t, sources, fixture)
	currentness, backend := qualificationTestPolicies(t)
	input, err := qualificationBuildInputs(sources, marshalReferenceTest(t, association), marshalReferenceTest(t, arm),
		fixture.request.Acceptance, fixture.request.Now, currentness, backend)
	require.NoError(t, err)
	admitted, err := qualificationAdmitInputContext(t.Context(), input.bytes(), sources)
	require.NoError(t, err)
	inputWire, err := qualificationDecode[qualificationInputs](input.bytes(), qualificationDocumentLimit)
	require.NoError(t, err)
	jobs, err := qualificationDecode[[]qualificationJobWire](admitted.jobs.bytes(), qualificationDocumentLimit)
	require.NoError(t, err)
	doc, err := qualificationSeal(qualificationManifestWire{Kind: "waza.qualification-manifest", Version: qualificationVersion,
		Profile: qualificationProfile, InvocationID: "synthetic-invocation-é", InputsSHA256: input.sha256(),
		Inputs: inputWire, Plan: *fixture.document.Calibration, Jobs: jobs})
	require.NoError(t, err)
	manifest, err := qualificationParseManifest(doc.bytes(), admitted)
	require.NoError(t, err)
	return manifest, fixture
}

func qualificationTestEvent(t *testing.T, manifest qualificationManifest, sequence uint64, previous, kind string, ordinal *uint64) qualificationEvent {
	t.Helper()
	m, err := qualificationManifestValue(manifest)
	require.NoError(t, err)
	event := qualificationEventWire{Kind: "waza.qualification-event", Version: qualificationVersion, InvocationID: m.InvocationID,
		ManifestSHA256: manifest.document.sha256(), ContractSHA256: m.Inputs.Association.ContractSHA256, CID: m.Inputs.Association.CID,
		ArmID: m.Inputs.Association.ArmID, Sequence: sequence, PreviousSHA256: previous, Type: kind}
	if ordinal != nil {
		job, jobDoc, err := qualificationJobAt(manifest, *ordinal)
		require.NoError(t, err)
		event.Ordinal, event.JobSHA256, event.Selector = new(*ordinal), new(jobDoc.sha256()), new(job.Selector)
	}
	if kind == "run_admission" || kind == "job_admission" {
		event.Admission = &qualificationAdmissionWire{true, "qualification_passed"}
	}
	doc, err := qualificationSeal(event)
	require.NoError(t, err)
	parsed, err := qualificationParseEvent(doc.bytes(), manifest)
	require.NoError(t, err)
	return parsed
}

func qualificationTestCurrentness(t *testing.T, manifest qualificationManifest, sequence uint64, previous, stage string, ordinal *uint64, challenge string) qualificationEvent {
	t.Helper()
	m, err := qualificationManifestValue(manifest)
	require.NoError(t, err)
	policy, err := qualificationSeal(m.Inputs.CurrentnessProfile)
	require.NoError(t, err)
	request := qualificationCurrentnessRequest{Kind: "waza.qualification-currentness-request", Version: qualificationVersion,
		InvocationID: m.InvocationID, ContractSHA256: m.Inputs.Association.ContractSHA256, CID: m.Inputs.Association.CID,
		ArmID: m.Inputs.Association.ArmID, ManifestSHA256: manifest.document.sha256(), InputsSHA256: m.InputsSHA256,
		CurrentnessProfileSHA256: policy.sha256(), Stage: stage, Challenge: challenge}
	event := qualificationEventWire{Kind: "waza.qualification-event", Version: qualificationVersion,
		InvocationID: m.InvocationID, ContractSHA256: request.ContractSHA256, CID: request.CID, ArmID: request.ArmID,
		ManifestSHA256: manifest.document.sha256(), Sequence: sequence, PreviousSHA256: previous, Type: "currentness"}
	if ordinal != nil {
		job, digest, err := qualificationJobAt(manifest, *ordinal)
		require.NoError(t, err)
		request.Ordinal, request.JobSHA256 = new(*ordinal), new(digest.sha256())
		event.Ordinal, event.JobSHA256, event.Selector = new(*ordinal), request.JobSHA256, new(job.Selector)
	}
	requestDoc, err := qualificationSeal(request)
	require.NoError(t, err)
	now := time.Now().UTC()
	ack := qualificationCurrentnessAcknowledgment{Kind: "waza.qualification-currentness-acknowledgment", Version: qualificationVersion,
		RequestSHA256: requestDoc.sha256(), CurrentnessProfileSHA256: policy.sha256(), Stage: stage, Current: true,
		AssociationValid: true, CheckedAt: now.Format(time.RFC3339Nano), ValidUntil: now.Add(time.Minute).Format(time.RFC3339Nano),
		AuthorityID: "synthetic-authority", ReceiptToken: "synthetic", AttestationEvidenceBase64: ""}
	event.CurrentnessRequest, event.CurrentnessAcknowledgment = &request, &ack
	doc, err := qualificationSeal(event)
	require.NoError(t, err)
	parsed, err := qualificationParseEvent(doc.bytes(), manifest)
	require.NoError(t, err)
	return parsed
}

func qualificationTestOperationalTerminal(t *testing.T, manifest qualificationManifest, ordinal uint64) qualificationTerminal {
	t.Helper()
	m, err := qualificationManifestValue(manifest)
	require.NoError(t, err)
	job, digest, err := qualificationJobAt(manifest, ordinal)
	require.NoError(t, err)
	entry := JudgeExecution{CaseID: job.Selector.CaseID, TaskID: job.Selector.TaskID, RequirementID: job.Selector.RequirementID,
		Check:          models.RequirementCheck{Scope: job.Selector.Check.Scope, AfterTurn: job.Selector.Check.AfterTurn, Grader: job.Selector.Check.Grader},
		RequestedModel: job.RequestedModel, State: AssessmentError, Reason: "factory_failed",
		EventModels: []string{}, AccountingModels: []string{}, Diagnostics: []JudgeDiagnostic{}}
	execution, err := qualificationMarshalBounded(entry, qualificationDocumentLimit, nil)
	require.NoError(t, err)
	doc, err := qualificationSeal(qualificationTerminalWire{Kind: "waza.qualification-terminal-payload", Version: qualificationVersion,
		InvocationID: m.InvocationID, ManifestSHA256: manifest.document.sha256(), ContractSHA256: m.Inputs.Association.ContractSHA256,
		CID: m.Inputs.Association.CID, Selector: job.Selector, JobSHA256: digest.sha256(), Ordinal: ordinal,
		Execution: execution, ObservationState: "operational_error", FailureCodes: []string{"factory_failed"}})
	require.NoError(t, err)
	terminal, err := qualificationParseTerminal(doc.bytes(), manifest)
	require.NoError(t, err)
	return terminal
}

func qualificationLocalTestJournal(t *testing.T, manifest qualificationManifest) (*qualificationLocalJournal, *os.Root, string) {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("materialized local physical backend supported only on Linux/macOS")
	}
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	m, err := qualificationManifestValue(manifest)
	require.NoError(t, err)
	policy, err := qualificationSeal(m.Inputs.BackendProfile)
	require.NoError(t, err)
	journal, err := qualificationOpenLocalJournal(t.Context(), qualificationLocalRegistryConfig{
		Root: root, BackendPolicy: policy, Scope: qualificationLocalScope{
			Namespace: m.Inputs.BackendProfile.Namespace, OwnerBindingSHA256: strings.Repeat("b", 64),
			FilesystemContractSHA256: m.Inputs.BackendProfile.DurabilityContractSHA256}, IO: qualificationOSJournalIO{},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, journal.Close()) })
	return journal, root, directory
}

func TestQualificationManifestIsSourceAdmittedNotMetadata(t *testing.T) {
	manifest, _ := qualificationProtocolFixture(t)
	_, err := qualificationParseManifest(manifest.document.bytes(), qualificationAdmittedInputContext{})
	require.Error(t, err)
	wire, err := qualificationManifestValue(manifest)
	require.NoError(t, err)
	require.Len(t, wire.Jobs, 4)
	require.Contains(t, wire.Jobs[0].NativeRequest.Tools[0].Name, "set_waza")
	for _, mutation := range []func(*qualificationManifestWire){
		func(w *qualificationManifestWire) { w.Jobs[0].ExpectedPassed = !w.Jobs[0].ExpectedPassed },
		func(w *qualificationManifestWire) { w.Jobs[0].NativeRequest.Message = "fake" },
		func(w *qualificationManifestWire) { w.Jobs[0].Ordinal = 9007199254740992 },
		func(w *qualificationManifestWire) { w.Jobs = w.Jobs[:3] },
		func(w *qualificationManifestWire) { w.Plan.Model = "wrong" },
		func(w *qualificationManifestWire) { w.Inputs.SuppliedReview = json.RawMessage(`{}`) },
	} {
		copy, err := calibrationJSONCopy(wire)
		require.NoError(t, err)
		mutation(&copy)
		_, err = qualificationParseManifest(marshalReferenceTest(t, copy), manifest.context)
		require.Error(t, err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = qualificationAdmitInputContext(canceled, manifest.context.input.bytes(), manifest.context.sources)
	require.Error(t, err)
}

func TestQualificationEventAndTerminalExactGrammar(t *testing.T) {
	manifest, _ := qualificationProtocolFixture(t)
	event := qualificationTestEvent(t, manifest, 1, manifest.document.sha256(), "run_admission", nil)
	wire, err := qualificationDecode[qualificationEventWire](event.document.bytes(), qualificationDocumentLimit)
	require.NoError(t, err)
	for _, mutate := range []func(*qualificationEventWire){
		func(e *qualificationEventWire) { e.Sequence = 0 },
		func(e *qualificationEventWire) { e.CID = "other" },
		func(e *qualificationEventWire) { e.PayloadSHA256 = new(strings.Repeat("a", 64)) },
		func(e *qualificationEventWire) { e.Admission = nil },
		func(e *qualificationEventWire) { e.Type = "unknown" },
	} {
		copy := wire
		mutate(&copy)
		_, err := qualificationParseEvent(marshalReferenceTest(t, copy), manifest)
		require.Error(t, err)
	}

	t.Run("protocol entrypoints preflight before materialization", func(t *testing.T) {
		manifest, _ := qualificationProtocolFixture(t)
		tiny := func(string) uint64 { return 1 }
		calls := 0
		manifestWire, err := qualificationManifestValue(manifest)
		require.NoError(t, err)
		manifestWire.Inputs.SuppliedReview = json.RawMessage(`{"role":"review","bytes_base64":"w6k="}`)
		manifestRaw, err := qualificationSeal(manifestWire)
		require.NoError(t, err)
		_, err = qualificationParseManifestBounded(manifestRaw.bytes(), manifest.context, qualificationDocumentLimit,
			qualificationTotalLimit, tiny, func() { calls++ })
		require.Error(t, err)
		require.Zero(t, calls, "source blobs rejected before manifest decode")
		event := qualificationTestCurrentness(t, manifest, 2, strings.Repeat("a", 64), "before_job", new(uint64(0)), strings.Repeat("b", 64))
		wire, err := qualificationDecode[qualificationEventWire](event.document.bytes(), qualificationDocumentLimit)
		require.NoError(t, err)
		wire.CurrentnessAcknowledgment.AttestationEvidenceBase64 = base64.StdEncoding.EncodeToString([]byte("é"))
		ackRaw, err := qualificationSeal(*wire.CurrentnessAcknowledgment)
		require.NoError(t, err)
		_, err = qualificationParseCurrentnessAcknowledgmentBounded(ackRaw.bytes(), qualificationDocumentLimit, 2, tiny, func() { calls++ })
		require.Error(t, err)
		require.Zero(t, calls, "direct receipt parser preflights evidence before DTO construction")
		raw, err := qualificationSeal(wire)
		require.NoError(t, err)
		_, err = qualificationParseEventBounded(raw.bytes(), manifest, qualificationDocumentLimit, 2,
			func(string) uint64 { return 2 }, func() { calls++ })
		require.NoError(t, err, "two UTF-8 bytes exactly")
		require.Equal(t, 1, calls)
		calls = 0
		_, err = qualificationParseEventBounded(raw.bytes(), manifest, qualificationDocumentLimit, 2, tiny, func() { calls++ })
		require.Error(t, err)
		require.Zero(t, calls, "base64 role boundary+1 before event decode")
		terminal := qualificationTestOperationalTerminal(t, manifest, 0)
		_, err = qualificationParseTerminalBounded(terminal.document.bytes(), manifest, len(terminal.document.bytes())-1,
			qualificationTotalLimit, nil, func() { calls++ })
		require.Error(t, err)
		require.Zero(t, calls, "terminal projection boundary before DTO construction")
		_, err = qualificationParseManifestBounded(manifestRaw.bytes(), manifest.context, qualificationDocumentLimit,
			1, nil, func() { calls++ })
		require.Error(t, err)
		require.Zero(t, calls, "aggregate source limit before materialization")
	})
	terminal := qualificationTestOperationalTerminal(t, manifest, 0)
	value, err := qualificationDecode[qualificationTerminalWire](terminal.document.bytes(), qualificationDocumentLimit)
	require.NoError(t, err)
	value.Agreement = new(true)
	_, err = qualificationParseTerminal(marshalReferenceTest(t, value), manifest)
	require.Error(t, err)
}

func TestQualificationTerminalFrozenNativeAccountingAndOwnerCounters(t *testing.T) {
	manifest, _ := qualificationProtocolFixture(t)
	terminal := qualificationTestObservedTerminal(t, manifest, 0)
	wire, err := qualificationDecode[qualificationTerminalWire](terminal.document.bytes(), qualificationDocumentLimit)
	require.NoError(t, err)
	for _, mutation := range []func(*JudgeExecution){
		func(e *JudgeExecution) { e.Callbacks = int(qualificationSafeCounter + 1) },
		func(e *JudgeExecution) { e.UsageComplete = false },
		func(e *JudgeExecution) { e.Initialized = false },
		func(e *JudgeExecution) {
			e.Usage.ModelMetrics = map[string]models.ModelUsage{"wrong": {AICredits: new(0.0)}}
		},
		func(e *JudgeExecution) { e.Usage.AICredits = new(1.0) },
	} {
		copy := wire
		var entry JudgeExecution
		require.NoError(t, qualificationDecodeNative(wire.Execution, &entry))
		mutation(&entry)
		copy.Execution, err = qualificationMarshalBounded(entry, qualificationDocumentLimit, nil)
		require.NoError(t, err)
		doc, err := qualificationSeal(copy)
		require.NoError(t, err)
		_, err = qualificationParseTerminal(doc.bytes(), manifest)
		require.Error(t, err)
	}
	wire.NativeResult = new(json.RawMessage(`{"identifier":"check","type":"prompt","score":2,"weight":1,"passed":true,"feedback":"","duration_ms":0}`))
	doc, err := qualificationSeal(wire)
	require.NoError(t, err)
	_, err = qualificationParseTerminal(doc.bytes(), manifest)
	require.Error(t, err)
}
