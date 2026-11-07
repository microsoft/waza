package assurance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/snapshot"
)

func BenchmarkVerifyFileCaseRepeatedEvidenceReferences(b *testing.B) {
	request, candidate, check, _ := repeatedEvidenceVerificationFixture(b, "state: ready\n", true)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		observation := verifyFileCase(context.Background(), request, candidate, check)
		if observation.State != Observed || observation.Result == nil || !observation.Result.Passed ||
			observation.Agreement == nil || !*observation.Agreement {
			b.Fatalf("unexpected observation: %s (%s)", observation.State, observation.Reason)
		}
	}
}

func repeatedEvidenceVerificationFixture(b testing.TB, content string, expected bool) (VerifyRequest, ReferenceCase, ReferenceCheck, string) {
	b.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		b.Skip("secure preserved-file restoration is supported on Linux and macOS")
	}
	task := &models.TestCase{TestID: "task", Requirements: []models.Requirement{{
		ID: "state", Checks: []models.RequirementCheck{{Scope: "eval", Grader: "check"}},
	}}}
	spec := &models.EvalSpec{Graders: []models.GraderConfig{{
		Identifier: "check", Kind: models.GraderKindFile,
		Parameters: models.FileGraderParameters{ContentPatterns: []models.FileContentPatternParameters{{
			Path: "state.txt", MustMatch: []string{`(?m)^state:\s*ready$`},
		}}},
	}}}
	workspace := b.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "state.txt"), []byte(content), 0o600); err != nil {
		b.Fatal(err)
	}
	workspacePaths := make([]string, 64)
	workspacePaths[0] = "state.txt"
	for i := 1; i < len(workspacePaths); i++ {
		name := fmt.Sprintf("evidence-%02d.txt", i)
		workspacePaths[i] = name
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(name+"\n"), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	snap, err := snapshot.Capture(snapshot.CaptureInput{
		EvalID: "benchmark", Task: task, Spec: spec, ExecutionMode: "mock",
		Request: &execution.ExecutionRequest{Message: "benchmark", WorkspaceDir: workspace},
		Run: &models.RunResult{
			RunNumber: 1, Attempts: 1, Status: models.StatusPassed,
			FinalOutput: "done", WorkspaceDir: workspace,
		},
		WorkspacePaths:  workspacePaths,
		WorkspaceLimits: snapshot.WorkspaceLimits{MaxFileBytes: 1024, MaxTotalBytes: 64 << 10},
	})
	if err != nil {
		b.Fatal(err)
	}
	rootPath := b.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := root.Close(); err != nil {
			b.Error(err)
		}
	})
	data, err := json.Marshal(snap)
	if err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "case.json"), data, 0o600); err != nil {
		b.Fatal(err)
	}
	taskDigest, err := evidence.JSONDigest(task)
	if err != nil {
		b.Fatal(err)
	}
	declarationDigest, err := evidence.JSONDigest(spec.Graders[0])
	if err != nil {
		b.Fatal(err)
	}
	candidate := ReferenceCase{
		ID: "case", ScenarioID: "benchmark", Domain: "benchmark", Classification: CaseGood,
		TaskID: task.TestID, TaskDeclarationSHA256: taskDigest.SHA256,
		Snapshot: "case.json", ManifestSHA256: snap.Evidence.SHA256,
	}
	if !expected {
		candidate.Classification = CaseCriticalBad
	}
	check := ReferenceCheck{
		RequirementID: "state", Check: task.Requirements[0].Checks[0],
		GraderDeclarationSHA256: declarationDigest.SHA256, ExpectedPassed: &expected,
	}
	check.Evidence = make([]models.EvidenceReference, len(workspacePaths))
	for i, path := range workspacePaths {
		reference, err := evidence.Reference(snap.Evidence, "workspace-file/"+path)
		if err != nil {
			b.Fatal(err)
		}
		check.Evidence[i] = reference
	}
	document := ReferenceDocument{
		SchemaVersion: ReferenceVersion, Kind: ReferenceKind, ID: "benchmark-labels", Version: "1",
		EvalSourceSHA256: taskDigest.SHA256, EvalResolvedConfigSHA256: declarationDigest.SHA256,
		Domains: []DomainCriteria{{ID: "benchmark", MinimumCases: 1, MinimumAgreement: 1}},
		Cases:   []ReferenceCase{candidate},
	}
	document.Cases[0].Checks = []ReferenceCheck{check}
	labels, err := json.Marshal(document)
	if err != nil {
		b.Fatal(err)
	}
	references, err := ParseReferences(labels)
	if err != nil {
		b.Fatal(err)
	}
	admitted, err := references.Document()
	if err != nil {
		b.Fatal(err)
	}
	candidate = admitted.Cases[0]
	check = candidate.Checks[0]
	request := VerifyRequest{Spec: spec, Tasks: map[string]*models.TestCase{"task": task}, SnapshotRoot: root}
	return request, candidate, check, rootPath
}
