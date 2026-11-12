package runnercheckpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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

// deniedBackend fails every read except the checkpoint declaration itself.
type deniedBackend struct {
	artifactmanifest.Backend
	allow string
}

func (d deniedBackend) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	if strings.Contains(key, d.allow) {
		return d.Backend.Get(ctx, key, limit)
	}
	return nil, &fs.PathError{Op: "open", Path: key, Err: fs.ErrPermission}
}

func TestAvailabilitySeparatesStorageFailuresFromSourceEvidence(t *testing.T) {
	store := backend(t)
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		return execution.NewMockEngine(config.ModelID), nil
	})
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
	for name, digest := range map[string]string{"before": interrupted.BeforeDigest, "finalized": completed.FinalizedDigest} {
		t.Run(name, func(t *testing.T) {
			result := availability(t, service, fixture(t), deniedBackend{store, digest}, digest)
			if result.Status != "operation_error" || result.ExitCode != 2 || diagnosticCode(result) != ErrorCodeStorageUnavailable {
				t.Fatalf("permission denial must be an operation error: %+v", result)
			}
		})
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
