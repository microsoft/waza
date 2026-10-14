package preflight

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/registry"
	"github.com/stretchr/testify/require"
)

func TestInspectOfflineRequirementIdentity(t *testing.T) {
	refs, requests := offlineFixtureCacheReferences(t)
	for _, scenario := range []bool{false, true} {
		for _, presetName := range []string{"cached", ""} {
			for _, alias := range []string{"", "explicit"} {
				for _, mode := range []string{"missing", "preset external", "merged external", "merged invalid"} {
					for _, matching := range []bool{false, true} {
						t.Run(fmt.Sprintf("scenario=%t/preset=%q/alias=%q/%s/matching=%t", scenario, presetName, alias, mode, matching), func(t *testing.T) {
							const ref = "github.com/example/checks/check.yaml@v1"
							wantName := presetName
							if wantName == "" {
								wantName = "check"
							}
							if alias != "" {
								wantName = alias
							}
							checkName := wantName
							if !matching {
								checkName = "different"
							}
							override := map[string]any{"ref": ref}
							if alias != "" {
								override["name"] = alias
							}
							externalConfig := map[string]any{"expect": []any{map[string]any{
								"tool": "read", "args": map[string]any{"value": map[string]any{
									"json_schema": map[string]any{"$ref": refs[0]},
								}},
							}}}
							if mode == "merged external" {
								override["config"] = externalConfig
							}
							if mode == "merged invalid" {
								override["type"] = "text"
								override["config"] = map[string]any{"contains": []string{"ready"}}
							}
							eval := validEval
							if scenario {
								eval = strings.Replace(eval, `schemaVersion: "1.4"`, "schemaVersion: \"2.0\"\nscenario: requirement-identity", 1)
								eval = strings.Replace(eval, "skill: example\n", "", 1)
							}
							eval += pipelineYAML(t, map[string]any{"graders": []any{override}})
							task := strings.Replace(validTask, "scope: task\n        grader: state", "scope: eval\n        grader: "+checkName, 1)
							evalPath, dir := writeSuite(t, eval, task)
							cache := t.TempDir()
							t.Setenv("WAZA_MODULE_CACHE", cache)
							commit := strings.Repeat("a", 40)
							module := filepath.Join(cache, "github.com", "example", "checks", commit)
							entry := models.LockfileGrader{Ref: ref, Commit: commit, Digest: "sha256:" + strings.Repeat("b", 64), URL: "https://not-contacted.invalid"}
							var presetPath string
							var presetData []byte
							if mode != "missing" {
								require.NoError(t, os.MkdirAll(module, 0o700))
								config := map[string]any{"expect": []any{map[string]any{"tool": "read"}}}
								if mode == "preset external" {
									config = externalConfig
								}
								preset := map[string]any{"type": "tool_calls", "config": config}
								if presetName != "" {
									preset["name"] = presetName
								}
								presetData = []byte(pipelineYAML(t, preset))
								presetPath = filepath.Join(module, "check.yaml")
								require.NoError(t, os.WriteFile(presetPath, presetData, 0o600))
								var err error
								entry.Digest, err = registry.DigestDirectory(module)
								require.NoError(t, err)
							}
							lock := models.NewLockfile()
							lock.UpsertGrader(entry)
							lockPath := filepath.Join(dir, models.LockfileName)
							require.NoError(t, models.WriteLockfile(lockPath, lock))
							lockBytes, err := os.ReadFile(lockPath)
							require.NoError(t, err)
							report := Inspect(evalPath, Options{})
							require.Len(t, report.Tasks, 1)
							require.Len(t, report.Tasks[0].Requirements, 1)
							unknown := alias == "" && (mode == "missing" || mode == "preset external")
							state := Invalid
							switch {
							case unknown:
								state = Unresolved
							case matching:
								state = Verified
							}
							require.Equal(t, state, report.Tasks[0].Requirements[0].State, "%+v", report.Diagnostics)
							require.Equal(t, state == Invalid || mode == "merged invalid", report.Failed(false), "%+v", report.Diagnostics)
							require.True(t, report.Failed(true))
							require.False(t, report.Complete)
							code := "requirement.references"
							switch state {
							case Invalid:
								code = "requirement.invalid"
							case Unresolved:
								code = "requirement.reference_unresolved"
							}
							require.True(t, hasDiagnostic(report, code, state), "%+v", report.Diagnostics)
							if mode == "merged external" || mode == "merged invalid" {
								require.Equal(t, wantName, report.Dependencies[0].Name)
							}
							after, err := os.ReadFile(evalPath)
							require.NoError(t, err)
							require.Equal(t, eval, string(after))
							after, err = os.ReadFile(lockPath)
							require.NoError(t, err)
							require.Equal(t, lockBytes, after)
							if presetPath != "" {
								after, err = os.ReadFile(presetPath)
								require.NoError(t, err)
								require.Equal(t, presetData, after)
							}
							require.Zero(t, requests.Load())
						})
					}
				}
			}
		}
	}
}

func TestRequirementUnknownIdentityKeepsDefiniteErrors(t *testing.T) {
	const ref = "github.com/example/checks/check.yaml@v1"
	for _, tc := range []struct {
		name string
		edit func(*models.TestCase, *models.EvalSpec)
	}{
		{"invalid ref", func(_ *models.TestCase, s *models.EvalSpec) { s.Graders[0].Ref = "invalid" }},
		{"named nonmatch", func(_ *models.TestCase, s *models.EvalSpec) { s.Graders[0].Identifier = "other" }},
		{"empty grader", func(t *models.TestCase, _ *models.EvalSpec) { t.Requirements[0].Checks[0].Grader = "" }},
		{"invalid scope", func(t *models.TestCase, _ *models.EvalSpec) { t.Requirements[0].Checks[0].Scope = "unknown" }},
		{"invalid turn", func(t *models.TestCase, _ *models.EvalSpec) { t.Requirements[0].Checks[0].AfterTurn = 1 }},
		{"task unknown", func(t *models.TestCase, _ *models.EvalSpec) { t.Requirements[0].Checks[0].Scope = "task" }},
		{"duplicate declarations", func(_ *models.TestCase, s *models.EvalSpec) {
			s.Graders = append(s.Graders, models.GraderConfig{Identifier: "check"}, models.GraderConfig{Identifier: "check"})
		}},
		{"duplicate check", func(t *models.TestCase, _ *models.EvalSpec) {
			t.Requirements[0].Checks = append(t.Requirements[0].Checks, t.Requirements[0].Checks[0])
		}},
		{"invalid category", func(t *models.TestCase, _ *models.EvalSpec) { t.Requirements[0].Category = "unsupported" }},
		{"empty description", func(t *models.TestCase, _ *models.EvalSpec) { t.Requirements[0].Description = "" }},
		{"empty ID", func(t *models.TestCase, _ *models.EvalSpec) { t.Requirements[0].ID = "" }},
		{"duplicate ID", func(t *models.TestCase, _ *models.EvalSpec) {
			t.Requirements = append(t.Requirements, t.Requirements[0])
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := &models.EvalSpec{Graders: []models.GraderConfig{{Ref: ref}}}
			task := &models.TestCase{TestID: "task", Requirements: []models.Requirement{{
				ID: "observable", Category: "outcome", Description: "Descriptive intent.",
				Checks: []models.RequirementCheck{{Scope: "eval", Grader: "check"}},
			}}}
			tc.edit(task, spec)
			report := &Report{Complete: true}
			plans := resolveRequirements(report, task, spec, "task.yaml")
			require.Equal(t, Invalid, plans[len(plans)-1].State)
			require.True(t, report.Failed(false))
		})
	}
}
