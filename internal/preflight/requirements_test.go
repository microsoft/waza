package preflight

import (
	"encoding/json"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestRequirementReferenceContract(t *testing.T) {
	eval := &models.EvalSpec{Graders: []models.GraderConfig{{Identifier: "state"}}}
	newTask := func(checks []models.RequirementCheck) *models.TestCase {
		return &models.TestCase{
			TestID: "update", Validators: []models.ValidatorInline{{Identifier: "boundary"}},
			Checkpoints:  []models.Checkpoint{{AfterTurn: 2, Graders: []models.ValidatorInline{{Identifier: "recover"}}}},
			Requirements: []models.Requirement{{ID: "persisted", Category: "outcome", Description: "Observable state exists.", Checks: checks}},
		}
	}
	tests := []struct {
		name  string
		check models.RequirementCheck
		state State
	}{
		{"eval", models.RequirementCheck{Scope: "eval", Grader: "state"}, Verified},
		{"task", models.RequirementCheck{Scope: "task", Grader: "boundary"}, Verified},
		{"checkpoint", models.RequirementCheck{Scope: "checkpoint", Grader: "recover", AfterTurn: 2}, Verified},
		{"unknown scope", models.RequirementCheck{Scope: "global", Grader: "state"}, Invalid},
		{"unknown grader", models.RequirementCheck{Scope: "task", Grader: "absent"}, Invalid},
		{"empty grader", models.RequirementCheck{Scope: "eval"}, Invalid},
		{"missing turn", models.RequirementCheck{Scope: "checkpoint", Grader: "recover"}, Invalid},
		{"wrong turn", models.RequirementCheck{Scope: "checkpoint", Grader: "recover", AfterTurn: 1}, Invalid},
		{"extraneous turn", models.RequirementCheck{Scope: "task", Grader: "boundary", AfterTurn: 2}, Invalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Report{Complete: true}
			plans := resolveRequirements(r, newTask([]models.RequirementCheck{tt.check}), eval, "task.yaml")
			require.Equal(t, tt.state, plans[0].State)
			require.Equal(t, tt.state == Invalid, r.Failed(false))
			require.True(t, r.Complete, "inventory completeness is not satisfaction")
		})
	}
	t.Run("uncovered", func(t *testing.T) {
		r := &Report{Complete: true}
		plans := resolveRequirements(r, newTask(nil), eval, "task.yaml")
		require.Equal(t, Unresolved, plans[0].State)
		require.False(t, r.Failed(false))
		require.True(t, r.Failed(true))
		require.True(t, r.Complete)
	})
	t.Run("shared check across requirements", func(t *testing.T) {
		task := newTask([]models.RequirementCheck{{Scope: "eval", Grader: "state"}})
		second := task.Requirements[0]
		second.ID = "also-persisted"
		task.Requirements = append(task.Requirements, second)
		r := &Report{}
		plans := resolveRequirements(r, task, eval, "task.yaml")
		require.Len(t, plans, 2)
		require.False(t, r.Failed(true))
	})
	for _, mutation := range []struct {
		name string
		edit func(*models.TestCase)
	}{
		{"empty id", func(task *models.TestCase) { task.Requirements[0].ID = "" }},
		{"padded id", func(task *models.TestCase) { task.Requirements[0].ID = " x " }},
		{"unknown category", func(task *models.TestCase) { task.Requirements[0].Category = "assert" }},
		{"empty description", func(task *models.TestCase) { task.Requirements[0].Description = " " }},
		{"duplicate id", func(task *models.TestCase) { task.Requirements = append(task.Requirements, task.Requirements[0]) }},
		{"duplicate check", func(task *models.TestCase) {
			task.Requirements[0].Checks = append(task.Requirements[0].Checks, task.Requirements[0].Checks[0])
		}},
		{"ambiguous reference", func(task *models.TestCase) { task.Validators = append(task.Validators, task.Validators[0]) }},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			task := newTask([]models.RequirementCheck{{Scope: "task", Grader: "boundary"}})
			mutation.edit(task)
			r := &Report{}
			resolveRequirements(r, task, eval, "task.yaml")
			require.True(t, r.Failed(false))
		})
	}
}

func TestReportPolicyAndJSON(t *testing.T) {
	for _, state := range []State{Verified, Unresolved, Unsupported, Invalid} {
		t.Run(string(state), func(t *testing.T) {
			r := &Report{Complete: true}
			r.add("static.check", state, "task.yaml", "task", "req", "Static check.", "Inspect evidence.")
			require.Equal(t, state == Invalid, r.Failed(false))
			require.Equal(t, state != Verified, r.Failed(true))
			require.True(t, r.Complete)
			wantSeverity := "warning"
			if state == Verified {
				wantSeverity = "info"
			} else if state == Invalid {
				wantSeverity = "error"
			}
			require.Equal(t, wantSeverity, r.Diagnostics[0].Severity)
		})
	}
	check := models.RequirementCheck{Scope: "task", Grader: "state"}
	data, err := json.Marshal(check)
	require.NoError(t, err)
	require.JSONEq(t, `{"scope":"task","grader":"state"}`, string(data))
	check.Scope, check.AfterTurn = "checkpoint", 2
	data, err = json.Marshal(check)
	require.NoError(t, err)
	require.JSONEq(t, `{"scope":"checkpoint","grader":"state","after_turn":2}`, string(data))
}
