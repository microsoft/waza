package orchestration

import (
	"testing"

	"github.com/microsoft/waza/internal/models"
)

func TestControlledInputOwnsFrozenBytes(t *testing.T) {
	r, task := controlledFixture(t)
	task.Stimulus.Resources = []models.ResourceRef{{Location: "hello.txt", Body: "original"}}
	task.Stimulus.Metadata = map[string]any{"nested": map[string]any{"value": "original"}}
	input, err := r.PrepareControlledInput(task)
	if err != nil {
		t.Fatal(err)
	}
	view, err := input.View()
	if err != nil {
		t.Fatal(err)
	}
	view.Resources[0].Content[0] = 'X'
	nested, ok := view.Context["nested"].(map[string]any)
	if !ok {
		t.Fatal("missing nested frozen context")
	}
	nested["value"] = "changed"
	next, err := input.View()
	if err != nil {
		t.Fatal(err)
	}
	nextNested, ok := next.Context["nested"].(map[string]any)
	if !ok || nextNested["value"] != "original" || string(next.Resources[0].Content) != "original" {
		t.Fatal("mutable planning view changed committed execution bytes")
	}
	origin := models.EvidenceOrigin{EvalID: "fresh", TaskID: task.TestID, RunNumber: 1, AttemptCount: 1}
	if _, err := r.ExecutePreparedControlledAttempt(t.Context(), task, input, origin); err != nil {
		t.Fatal(err)
	}
}
