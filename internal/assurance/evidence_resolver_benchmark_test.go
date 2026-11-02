package assurance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/snapshot"
)

func BenchmarkVerifyFileCaseRepeatedEvidenceReferences(b *testing.B) {
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
	if err := os.WriteFile(filepath.Join(workspace, "state.txt"), []byte("state: ready\n"), 0o600); err != nil {
		b.Fatal(err)
	}
	snap, err := snapshot.Capture(snapshot.CaptureInput{
		EvalID: "benchmark", Task: task, Spec: spec, ExecutionMode: "mock",
		Request: &execution.ExecutionRequest{Message: "benchmark", WorkspaceDir: workspace},
		Run: &models.RunResult{
			RunNumber: 1, Attempts: 1, Status: models.StatusPassed,
			FinalOutput: "done", WorkspaceDir: workspace,
		},
		WorkspacePaths:  []string{"state.txt"},
		WorkspaceLimits: snapshot.WorkspaceLimits{MaxFileBytes: 1024, MaxTotalBytes: 4096},
	})
	if err != nil {
		b.Fatal(err)
	}
	rootPath := b.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = root.Close() })
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
	reference, err := evidence.Reference(snap.Evidence, "workspace-file/state.txt")
	if err != nil {
		b.Fatal(err)
	}
	expected := true
	candidate := ReferenceCase{
		ID: "case", ScenarioID: "benchmark", Domain: "benchmark", Classification: CaseGood,
		TaskID: task.TestID, TaskDeclarationSHA256: taskDigest.SHA256,
		Snapshot: "case.json", ManifestSHA256: snap.Evidence.SHA256,
	}
	check := ReferenceCheck{
		RequirementID: "state", Check: task.Requirements[0].Checks[0],
		GraderDeclarationSHA256: declarationDigest.SHA256, ExpectedPassed: &expected,
	}
	check.Evidence = make([]models.EvidenceReference, 64)
	for i := range check.Evidence {
		check.Evidence[i] = reference
	}
	request := VerifyRequest{Spec: spec, Tasks: map[string]*models.TestCase{"task": task}, SnapshotRoot: root}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		observation := verifyFileCase(context.Background(), request, candidate, check)
		if observation.State != Observed {
			b.Fatalf("unexpected observation: %s (%s)", observation.State, observation.Reason)
		}
	}
}
