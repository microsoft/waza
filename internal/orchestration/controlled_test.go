package orchestration

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/snapshot"
)

func controlledFixture(t *testing.T) (*EvalRunner, *models.TestCase) {
	t.Helper()
	spec := &models.EvalSpec{SchemaVersion: "2.0", Scenario: "controlled-offline",
		Config: models.Config{EngineType: "mock", ModelID: "mock-model", TimeoutSec: 60, MaxAttempts: 2}}
	task := &models.TestCase{TestID: "controlled-task", DisplayName: "Controlled task",
		Stimulus:    models.TaskStimulus{Message: "hello"},
		Expectation: models.TaskExpectation{MustInclude: []string{"hello"}}}
	engine := execution.NewMockEngine(spec.Config.ModelID)
	if err := engine.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := engine.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	r := NewEvalRunner(config.NewEvalConfig(spec), engine)
	return r, task
}

func TestControlledOriginBeforeCapture(t *testing.T) {
	r, task := controlledFixture(t)
	r.snapshotWriter = snapshot.NewWriter(t.TempDir())
	r.evalRunID = "legacy-unmodified"
	origin := models.EvidenceOrigin{EvalID: "allocated-before-start", TaskID: task.TestID, RunNumber: 3, AttemptCount: 2}
	observed, err := r.ExecuteControlledAttempt(t.Context(), task, origin)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Origin != origin || observed.Run.RunNumber != 3 || observed.Run.Attempts != 2 ||
		observed.Run.Status != models.StatusPassed || len(observed.Checks) != 1 ||
		observed.Checks[0].Scope != "expectation" || r.evalRunID != "legacy-unmodified" {
		t.Fatalf("wrong attributable observation: %+v", observed)
	}
	data, err := os.ReadFile(observed.Run.SnapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	var snap snapshot.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatal(err)
	}
	expectedCaptureOrigin := origin
	expectedCaptureOrigin.PriorAttempts = "not_preserved"
	if snap.Evidence == nil || snap.Evidence.Origin != expectedCaptureOrigin {
		t.Fatalf("capture replaced or missed preallocated attempt origin: %+v", snap.Evidence)
	}
}

func TestControlledUnsupportedPaths(t *testing.T) {
	for _, name := range []string{"program", "parameter_mismatch", "collision", "hooks", "parallel", "skills", "responder", "no_checks", "nil_task"} {
		t.Run(name, func(t *testing.T) {
			r, task := controlledFixture(t)
			switch name {
			case "program":
				task.Validators = []models.ValidatorInline{{Identifier: "unsafe", Kind: models.GraderKindProgram}}
			case "parameter_mismatch":
				task.Validators = []models.ValidatorInline{{Identifier: "unsafe", Kind: models.GraderKindText,
					Parameters: models.ProgramGraderParameters{}}}
			case "collision":
				task.Validators = []models.ValidatorInline{{Identifier: "_output_contains", Kind: models.GraderKindText,
					Parameters: models.TextGraderParameters{}}}
			case "hooks":
				r.cfg.Spec().Baseline = true
			case "parallel":
				r.cfg.Spec().Config.Concurrent = true
			case "skills":
				r.cfg.Spec().SkillName = "ambient"
			case "responder":
				task.Stimulus.Responder = &models.ResponderConfig{}
			case "no_checks":
				task.Expectation = models.TaskExpectation{}
			case "nil_task":
				task = nil
			}
			if err := r.ValidateControlledExecution(task); err == nil {
				t.Fatal("unsupported/ambiguous strict path must not be certified")
			}
		})
	}
}

func TestControlledOriginRejectsHistoricalAndPartial(t *testing.T) {
	r, task := controlledFixture(t)
	for _, origin := range []models.EvidenceOrigin{
		{}, {EvalID: "new", TaskID: "other", RunNumber: 1, AttemptCount: 1},
		{EvalID: "new", TaskID: task.TestID, RunNumber: 0, AttemptCount: 1},
		{EvalID: "new", TaskID: task.TestID, RunNumber: 1, AttemptCount: 0},
		{EvalID: "new", TaskID: task.TestID, RunNumber: 1, AttemptCount: 1, PriorAttempts: "historical"},
	} {
		if _, err := r.ExecuteControlledAttempt(t.Context(), task, origin); err == nil {
			t.Fatalf("invalid origin accepted: %+v", origin)
		}

	}
}
