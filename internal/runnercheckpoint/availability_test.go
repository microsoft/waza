package runnercheckpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executioncheckpoint"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
)

func storeFiles(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		files[path] = data
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return files
}

func availability(t *testing.T, service *Service, wire []byte, store artifactmanifest.Backend, digest string) Availability {
	t.Helper()
	result, err := service.Availability(context.Background(), wire, store, digest)
	if (result.Status == "available") != (err == nil) {
		t.Fatalf("availability status %q disagrees with error %v", result.Status, err)
	}
	if _, encodeErr := EncodeAvailability(result); encodeErr != nil {
		t.Fatalf("availability does not match its schema: %+v: %v", result, encodeErr)
	}
	return result
}

func diagnosticCode(result Availability) string {
	if len(result.Diagnostics) != 1 {
		return ""
	}
	return result.Diagnostics[0].Code
}

func TestAvailabilityReportsResumeActionWithoutWriting(t *testing.T) {
	root := t.TempDir()
	store, err := artifactmanifest.OpenLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	factories := 0
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		factories++
		return execution.NewMockEngine(config.ModelID), nil
	})
	pause := errors.New("stop after before publication")
	interrupted, err := service.Submit(context.Background(), fixture(t), store, Hooks{
		Published: func(context.Context, Reference) error { return pause },
	})
	if !errors.Is(err, pause) || interrupted.BeforeDigest == "" {
		t.Fatalf("control did not publish an interrupted before checkpoint: %+v, %v", interrupted, err)
	}

	before := storeFiles(t, root)
	fresh := availability(t, service, fixture(t), store, interrupted.BeforeDigest)
	if fresh.Status != "available" || fresh.ResumeAction != ResumeFreshRetryChild || fresh.Source == nil ||
		fresh.Source.Boundary != "before-unit" || fresh.Source.ExecutionID != interrupted.Submission.Reservation.ExecutionID ||
		*fresh.RestoredUnits != 0 || *fresh.RemainingWork != 1 || fresh.CompatibilityResult != "compatible" {
		t.Fatalf("before availability: %+v", fresh)
	}
	after := storeFiles(t, root)
	if len(after) != len(before) || factories != 0 {
		t.Fatalf("availability wrote records or constructed an engine: %d -> %d files, %d factories", len(before), len(after), factories)
	}
	for path, data := range before {
		if !bytes.Equal(data, after[path]) {
			t.Fatalf("availability changed %s", path)
		}
	}

	reuse := runnerunit.SubmissionRequest{Kind: "waza.execution-submission", SchemaVersion: "1.0",
		Request: mustRequest(t), Policy: &runnerunit.SubmissionPolicy{Mode: "reuse", Source: &executionattempt.Source{
			ExecutionID: interrupted.Submission.Reservation.ExecutionID, ManifestDigest: strings.Repeat("0", 64)}}}
	if conflict := availability(t, service, mustWire(t, reuse), store, interrupted.BeforeDigest); conflict.Status != "unavailable" ||
		diagnosticCode(conflict) != "checkpoint_policy_conflict" || conflict.Source != nil {
		t.Fatalf("policy conflict: %+v", conflict)
	}

	completed, err := service.Submit(context.Background(), fixture(t), store, Hooks{})
	if err != nil || completed.FinalizedDigest == "" {
		t.Fatalf("completed control: %+v, %v", completed, err)
	}
	stale := availability(t, service, fixture(t), store, completed.BeforeDigest)
	if stale.Status != "unavailable" || diagnosticCode(stale) != "checkpoint_stale" || stale.Source != nil ||
		stale.RestoredUnits != nil || stale.ExitCode != 6 {
		t.Fatalf("completed before checkpoint must be stale: %+v", stale)
	}
	finalized := availability(t, service, fixture(t), store, completed.FinalizedDigest)
	if finalized.Status != "available" || finalized.ResumeAction != ResumeReuseOriginalTerminal ||
		finalized.Source.Boundary != "finalized-unit" || *finalized.RestoredUnits != 1 || *finalized.RemainingWork != 0 {
		t.Fatalf("finalized availability: %+v", finalized)
	}
	resumed, err := service.Resume(context.Background(), fixture(t), store, completed.FinalizedDigest, Hooks{})
	if err != nil || resumed.Status != "reused" {
		t.Fatalf("available finalized checkpoint did not resume: %+v, %v", resumed, err)
	}
}

func TestAvailabilityFailsClosedWithActionableDiagnostics(t *testing.T) {
	store := backend(t)
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		return execution.NewMockEngine(config.ModelID), nil
	})
	completed, err := service.Submit(context.Background(), fixture(t), store, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	corrupt := []byte("{}")
	corruptDigest := artifactmanifest.Hash(corrupt)
	if err := artifactmanifest.NewStore(store).Put(context.Background(), artifactmanifest.Artifact{
		Name: "checkpoint.json", SHA256: corruptDigest, SizeBytes: int64(len(corrupt)), MediaType: "application/json",
	}, corrupt); err != nil {
		t.Fatal(err)
	}
	otherBuild, err := NewService(runnerunit.NewService(nil), strings.Repeat("d", 40))
	if err != nil {
		t.Fatal(err)
	}
	for _, control := range []struct {
		name, digest, status, code string
		service                    *Service
		wire                       []byte
	}{
		{"invalid digest", "ABC", "rejected", "checkpoint_digest_invalid", service, fixture(t)},
		{"invalid input", completed.FinalizedDigest, "rejected", "checkpoint_input_invalid", service, []byte(`{"secret":"x"}`)},
		{"missing", strings.Repeat("e", 64), "unavailable", "checkpoint_not_found", service, fixture(t)},
		{"corrupt", corruptDigest, "unavailable", "checkpoint_corrupt", service, fixture(t)},
		{"incompatible build", completed.FinalizedDigest, "unavailable", "checkpoint_incompatible", otherBuild, fixture(t)},
	} {
		t.Run(control.name, func(t *testing.T) {
			result := availability(t, control.service, control.wire, store, control.digest)
			if result.Status != control.status || diagnosticCode(result) != control.code || result.Source != nil ||
				result.ResumeAction != "" || result.Diagnostics[0].Message == "" ||
				strings.Contains(result.Diagnostics[0].Message, "secret") {
				t.Fatalf("%+v", result)
			}
			if control.code == "checkpoint_incompatible" && (result.CompatibilityResult != "incompatible" ||
				len(result.CompatibilityDifferences) == 0) {
				t.Fatalf("incompatible result lost changed fields: %+v", result)
			}
		})
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := service.Availability(canceled, fixture(t), store, completed.FinalizedDigest)
	if err == nil || result.Status != "operation_error" || result.ExitCode != 2 || diagnosticCode(result) != "checkpoint_availability_interrupted" {
		t.Fatalf("canceled availability: %+v, %v", result, err)
	}
}

func mustRequest(t *testing.T) []byte {
	t.Helper()
	wrapper, err := runnerunit.ParseSubmission(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	return wrapper.Request
}

func mustWire(t *testing.T, request runnerunit.SubmissionRequest) []byte {
	t.Helper()
	wire, err := encodeSubmissionRequest(&request)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

// withAttempt rewrites only selection.attempt in the captured request.
func withAttempt(t *testing.T, attempt int) []byte {
	t.Helper()
	var request map[string]any
	if err := json.Unmarshal(mustRequest(t), &request); err != nil {
		t.Fatal(err)
	}
	selection, ok := request["selection"].(map[string]any)
	if !ok {
		t.Fatal("request has no selection object")
	}
	selection["attempt"] = attempt
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return mustWire(t, runnerunit.SubmissionRequest{Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: data})
}

func TestAvailabilityAdmitsTheRequestLikeResume(t *testing.T) {
	store := backend(t)
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		return execution.NewMockEngine(config.ModelID), nil
	})
	completed, err := service.Submit(context.Background(), fixture(t), store, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	outOfRange := availability(t, service, withAttempt(t, 4), store, completed.FinalizedDigest)
	if outOfRange.Status != "rejected" || diagnosticCode(outOfRange) != "checkpoint_request_invalid" || outOfRange.Source != nil {
		t.Fatalf("attempt beyond max_attempts must be rejected before compatibility: %+v", outOfRange)
	}
	if resumed, err := service.Resume(context.Background(), withAttempt(t, 4), store, completed.FinalizedDigest, Hooks{}); err == nil {
		t.Fatalf("resume admitted an attempt beyond max_attempts: %+v", resumed)
	}
	alternate := availability(t, service, withAttempt(t, 3), store, completed.FinalizedDigest)
	if alternate.Status != "available" || alternate.ResumeAction != ResumeReuseOriginalTerminal {
		t.Fatalf("valid alternate ordinal must stay available: %+v", alternate)
	}
	if resumed, err := service.Resume(context.Background(), withAttempt(t, 3), store, completed.FinalizedDigest, Hooks{}); err != nil ||
		resumed.Status != "reused" {
		t.Fatalf("available alternate ordinal did not resume: %+v, %v", resumed, err)
	}
}

// azureFake is an in-memory Azure container whose reads can be denied or
// truncated once armed, except for keys containing allow.
type azureFake struct {
	mu      sync.Mutex
	objects map[string][]byte
	armed   bool
	short   bool
	allow   string
	failed  []string
}

func (b *azureFake) Do(request *http.Request) (*http.Response, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := strings.TrimPrefix(request.URL.Path, "/container/")
	header := http.Header{"Etag": {`"test"`}}
	respond := func(status int, body io.Reader, length int) (*http.Response, error) {
		header.Set("Content-Length", strconv.Itoa(length))
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(body),
			ContentLength: int64(length), Request: request}, nil
	}
	switch request.Method {
	case http.MethodPut:
		if _, exists := b.objects[key]; exists {
			header.Set("X-Ms-Error-Code", "ConditionNotMet")
			return respond(http.StatusPreconditionFailed, strings.NewReader(""), 0)
		}
		data, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		b.objects[key] = data
		return respond(http.StatusCreated, strings.NewReader(""), 0)
	case http.MethodGet:
		if request.URL.Query().Get("comp") == "list" {
			var body strings.Builder
			body.WriteString("<EnumerationResults><Blobs>")
			for name := range b.objects {
				if strings.HasPrefix(name, request.URL.Query().Get("prefix")) {
					body.WriteString("<Blob><Name>" + name + "</Name></Blob>")
				}
			}
			body.WriteString("</Blobs><NextMarker></NextMarker></EnumerationResults>")
			return respond(http.StatusOK, strings.NewReader(body.String()), body.Len())
		}
		data, exists := b.objects[key]
		if b.armed && (b.allow == "" || !strings.Contains(key, b.allow)) {
			b.failed = append(b.failed, key)
			if b.short && exists {
				return respond(http.StatusOK, io.MultiReader(bytes.NewReader(data[:len(data)/2]), errReader{io.ErrUnexpectedEOF}), len(data))
			}
			header.Set("X-Ms-Error-Code", "AuthorizationPermissionMismatch")
			return respond(http.StatusForbidden, strings.NewReader(""), 0)
		}
		if !exists {
			header.Set("X-Ms-Error-Code", "BlobNotFound")
			return respond(http.StatusNotFound, strings.NewReader(""), 0)
		}
		header.Set("X-Ms-Blob-Type", "BlockBlob")
		return respond(http.StatusOK, bytes.NewReader(data), len(data))
	default:
		return nil, errors.New("unexpected availability fixture request")
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func azureStore(t *testing.T, fake *azureFake) artifactmanifest.Backend {
	t.Helper()
	fake.objects = map[string][]byte{}
	store, err := artifactmanifest.NewAzure("https://offline.invalid/container", checkpointTestCredential{}, &container.ClientOptions{
		ClientOptions: azcore.ClientOptions{Transport: fake, Retry: policy.RetryOptions{MaxRetries: -1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// replaceReservation overwrites the stored reservation of id in place.
func (b *azureFake) replaceReservation(t *testing.T, id string, data []byte) {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	for key := range b.objects {
		if strings.Contains(key, "reservation") && strings.Contains(key, id) {
			b.objects[key] = data
			return
		}
	}
	t.Fatalf("no stored reservation for %s", id)
}

// sourcePair records one interrupted before-unit and one finalized checkpoint.
func sourcePair(t *testing.T, service *Service, store artifactmanifest.Backend) (Result, Result) {
	t.Helper()
	pause := errors.New("stop after before publication")
	interrupted, err := service.Submit(context.Background(), fixture(t), store, Hooks{
		Published: func(context.Context, Reference) error { return pause },
	})
	if !errors.Is(err, pause) {
		t.Fatal(err)
	}
	completed, err := service.Submit(context.Background(), fixture(t), store, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	return interrupted, completed
}

func TestAvailabilitySeparatesStorageFailuresFromSourceEvidence(t *testing.T) {
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		return execution.NewMockEngine(config.ModelID), nil
	})
	for _, short := range []bool{false, true} {
		for _, boundary := range []string{"before", "finalized", "checkpoint"} {
			t.Run(fmt.Sprintf("short=%t/%s", short, boundary), func(t *testing.T) {
				fake := &azureFake{}
				store := azureStore(t, fake)
				interrupted, completed := sourcePair(t, service, store)
				digest := map[string]string{"before": interrupted.BeforeDigest}[boundary]
				if digest == "" {
					digest = completed.FinalizedDigest
				}
				fake.mu.Lock()
				fake.armed, fake.short = true, short
				if boundary != "checkpoint" {
					fake.allow = digest
				}
				fake.mu.Unlock()
				result := availability(t, service, fixture(t), store, digest)
				fake.mu.Lock()
				failed := slices.Clone(fake.failed)
				fake.mu.Unlock()
				if len(failed) == 0 || (boundary != "checkpoint" && slices.ContainsFunc(failed, func(key string) bool {
					return strings.Contains(key, digest)
				})) {
					t.Fatalf("the intended read was not failed: %v", failed)
				}
				if result.Status != "operation_error" || result.ExitCode != 2 || diagnosticCode(result) != ErrorCodeStorageUnavailable {
					t.Fatalf("provider read failure must be an operation error: %+v", result)
				}
			})
		}
	}
}

func TestAvailabilityRejectsAMalformedSourceReservation(t *testing.T) {
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		return execution.NewMockEngine(config.ModelID), nil
	})
	for _, boundary := range []string{"before", "finalized"} {
		t.Run(boundary, func(t *testing.T) {
			fake := &azureFake{}
			store := azureStore(t, fake)
			interrupted, completed := sourcePair(t, service, store)
			source, digest := interrupted, interrupted.BeforeDigest
			if boundary == "finalized" {
				source, digest = completed, completed.FinalizedDigest
			}
			fake.replaceReservation(t, source.Submission.Reservation.ExecutionID, []byte(`{"kind":"waza.execution-attempt"}`))
			result := availability(t, service, fixture(t), store, digest)
			if result.Status != "unavailable" || result.ExitCode != 6 || diagnosticCode(result) != "checkpoint_"+boundary+"_source_rejected" {
				t.Fatalf("malformed reservation must reject the source, not report storage: %+v", result)
			}
		})
	}
}

func TestAvailabilityAdmitsTheRetryChildAncestryLikeResume(t *testing.T) {
	fake := &azureFake{}
	store := azureStore(t, fake)
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		return execution.NewMockEngine(config.ModelID), nil
	})
	original, _ := sourcePair(t, service, store)
	pause := errors.New("stop after retry before publication")
	request, err := runnerunit.ParseSubmission(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	request.Policy = &runnerunit.SubmissionPolicy{Mode: "distinct", Parent: &executionattempt.Parent{
		ExecutionID: original.Submission.Reservation.ExecutionID, Relationship: "retry",
	}}
	retry, err := service.Submit(context.Background(), mustWire(t, *request), store, Hooks{
		Published: func(context.Context, Reference) error { return pause },
	})
	if !errors.Is(err, pause) || retry.BeforeDigest == "" {
		t.Fatalf("retry did not retain a before checkpoint: %+v, %v", retry, err)
	}
	if result := availability(t, service, fixture(t), store, retry.BeforeDigest); result.Status != "available" {
		t.Fatalf("intact ancestry must be available: %+v", result)
	}
	fake.replaceReservation(t, original.Submission.Reservation.ExecutionID, []byte("not json"))
	result := availability(t, service, fixture(t), store, retry.BeforeDigest)
	if result.Status != "unavailable" || diagnosticCode(result) != "checkpoint_before_source_rejected" {
		t.Fatalf("unreadable ancestor must reject availability: %+v", result)
	}
	if resumed, err := service.Resume(context.Background(), fixture(t), store, retry.BeforeDigest, Hooks{}); err == nil {
		t.Fatalf("resume admitted the same unreadable ancestor: %+v", resumed)
	}
}

func TestAvailabilityRejectsAContradictoryFinalizedReport(t *testing.T) {
	ctx := context.Background()
	store := backend(t)
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		return execution.NewMockEngine(config.ModelID), nil
	})
	control, err := service.Submit(ctx, fixture(t), store, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	binding := control.Submission.Reservation.Binding
	terminal, err := runnerterminal.Encode(*control.Submission.Terminal, binding.TerminalBinding())
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := executionledger.New(store)
	if err != nil {
		t.Fatal(err)
	}
	creator, err := ledger.Reserve(ctx, binding, nil)
	if err != nil {
		t.Fatal(err)
	}
	objects := artifactmanifest.NewStore(store)
	manifest := artifactmanifest.Manifest{Kind: "waza.artifact-manifest", SchemaVersion: artifactmanifest.Version,
		Binding: binding, TerminalArtifact: "terminal.json", Artifacts: []artifactmanifest.Artifact{}}
	for name, data := range map[string][]byte{"terminal.json": terminal, "report.json": []byte(`{}`)} {
		artifact := artifactmanifest.Artifact{Name: name, SHA256: artifactmanifest.Hash(data),
			SizeBytes: int64(len(data)), MediaType: "application/json"}
		if err := objects.Put(ctx, artifact, data); err != nil {
			t.Fatal(err)
		}
		manifest.Artifacts = append(manifest.Artifacts, artifact)
	}
	slices.SortFunc(manifest.Artifacts, func(a, b artifactmanifest.Artifact) int { return strings.Compare(a.Name, b.Name) })
	manifestData, err := artifactmanifest.Encode(manifest)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := objects.Create(ctx, manifestData)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := objects.Finalize(ctx, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Complete(ctx, creator, digest, "terminal.json", "report.json"); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := executioncheckpoint.PublishFinalized(ctx, store, ledger, creator, mustRequest(t), strings.Repeat("c", 40), digest)
	if err != nil {
		t.Fatal(err)
	}
	result := availability(t, service, fixture(t), store, checkpoint)
	if result.Status != "unavailable" || diagnosticCode(result) != "checkpoint_finalized_source_rejected" {
		t.Fatalf("contradictory report.json must not be available: %+v", result)
	}
	if resumed, err := service.Resume(ctx, fixture(t), store, checkpoint, Hooks{}); err == nil {
		t.Fatalf("resume reused a contradictory report: %+v", resumed)
	}
}

func TestAvailabilityAppliesResumeRuntimeAdmission(t *testing.T) {
	for _, name := range []string{"COPILOT_CLI_PATH", "COPILOT_BASE_URL", "COPILOT_PROVIDER_BASE_URL"} {
		t.Setenv(name, "")
	}
	request, err := runnerunit.ParseSubmission(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	unit := strings.NewReplacer(`"inline.mock-text"`, `"inline.copilot-text"`, `executor: mock`, `executor: copilot-sdk`,
		`model: mock-model`, `model: owned-control-model`, `"allow_paid_execution":false`, `"allow_paid_execution":true`,
		`"allow_paid_execution": false`, `"allow_paid_execution": true`).Replace(string(request.Request))
	request.Request = json.RawMessage(unit)
	wire := mustWire(t, *request)
	store := backend(t)
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		t.Fatal("availability or the paused control constructed a runtime")
		return nil, nil
	})
	pause := errors.New("stop after before publication")
	interrupted, err := service.Submit(context.Background(), wire, store, Hooks{
		Published: func(context.Context, Reference) error { return pause },
	})
	if !errors.Is(err, pause) || interrupted.BeforeDigest == "" {
		t.Fatalf("native control did not publish a before checkpoint: %+v, %v", interrupted, err)
	}
	if result := availability(t, service, wire, store, interrupted.BeforeDigest); result.Status != "available" {
		t.Fatalf("admitted runtime must be available: %+v", result)
	}
	t.Setenv("COPILOT_CLI_PATH", "uncontrolled-native")
	result := availability(t, service, wire, store, interrupted.BeforeDigest)
	if result.Status != "unavailable" || diagnosticCode(result) != "checkpoint_runtime_rejected" ||
		strings.Contains(result.Diagnostics[0].Message, "uncontrolled-native") {
		t.Fatalf("runtime that resume rejects must not be available: %+v", result)
	}
	if resumed, err := service.Resume(context.Background(), wire, store, interrupted.BeforeDigest, Hooks{}); err == nil {
		t.Fatalf("resume admitted the same runtime: %+v", resumed)
	}
}

func TestAvailabilityRuntimeAdmissionUsesCallerMode(t *testing.T) {
	for _, name := range []string{"COPILOT_CLI_PATH", "COPILOT_BASE_URL", "COPILOT_PROVIDER_BASE_URL"} {
		t.Setenv(name, "")
	}
	ctx := context.Background()
	request, err := runnerunit.ParseSubmission(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	unit := strings.NewReplacer(`"inline.mock-text"`, `"inline.copilot-text"`, `executor: mock`, `executor: copilot-sdk`,
		`model: mock-model`, `model: owned-control-model`, `"allow_paid_execution":false`, `"allow_paid_execution":true`,
		`"allow_paid_execution": false`, `"allow_paid_execution": true`).Replace(string(request.Request))
	request.Request = json.RawMessage(unit)
	caller, ok := runnerunit.AdmittedBinding(request.Request)
	if !ok || caller.ExecutionMode != "copilot" {
		t.Fatalf("copilot request was not admitted: %+v", caller)
	}
	store := backend(t)
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		t.Fatal("availability constructed a runtime")
		return nil, nil
	})
	// The before source declares mock while the caller resumes as copilot.
	reservation := caller
	reservation.ExecutionMode = "mock"
	ledger, err := executionledger.New(store)
	if err != nil {
		t.Fatal(err)
	}
	creator, err := ledger.Reserve(ctx, reservation, nil)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := executioncheckpoint.PublishBefore(ctx, store, ledger, creator, request.Request, service.revision)
	if err != nil {
		t.Fatal(err)
	}
	wire := mustWire(t, *request)
	t.Setenv("COPILOT_CLI_PATH", "uncontrolled-native")
	if result := availability(t, service, wire, store, digest); diagnosticCode(result) != "checkpoint_runtime_rejected" {
		t.Fatalf("availability must evaluate the caller's runtime: %+v", result)
	}
	if resumed, err := service.Resume(ctx, wire, store, digest, Hooks{}); err == nil {
		t.Fatalf("resume admitted the caller runtime: %+v", resumed)
	}
}
