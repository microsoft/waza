package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/stretchr/testify/require"
)

func TestEvidenceSavedGradeEffectiveModelAndRealExecutorAdmission(t *testing.T) {
	for _, tc := range []struct{ name, actor, judge, grader, override, want string }{
		{"eval fallback", "actor-model", "", "", "", "actor-model"},
		{"auto fallback", "", "", "", "", "auto"},
		{"judge default", "actor-model", "judge-model", "", "", "judge-model"},
		{"CLI default", "actor-model", "judge-model", "", "cli-model", "cli-model"},
		{"grader override", "actor-model", "judge-model", "grader-model", "cli-model", "grader-model"},
	} {
		for _, taskLevel := range []bool{false, true} {
			name := tc.name + "/eval"
			if taskLevel {
				name = tc.name + "/task"
			}
			t.Run(name, func(t *testing.T) {
				spec, source, original := savedEvidenceFixture(t)
				body, err := os.ReadFile(spec)
				require.NoError(t, err)
				text := strings.Replace(string(body), "model: actor-model, judge_model: judge-model, ", "", 1)
				settings := ""
				if tc.actor != "" {
					settings += "model: " + tc.actor + ", "
				}
				if tc.judge != "" {
					settings += "judge_model: " + tc.judge + ", "
				}
				text = strings.Replace(text, "config: {", "config: {"+settings, 1)
				if tc.grader != "" {
					text = strings.Replace(text, "      evidence_context:", "      model: "+tc.grader+"\n      evidence_context:", 1)
				}
				if taskLevel {
					head, graders, found := strings.Cut(text, "graders:\n")
					require.True(t, found)
					task := filepath.Join(filepath.Dir(spec), "task.yaml")
					body, err := os.ReadFile(task)
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(task, append(body, []byte("graders:\n"+graders)...), 0600))
					text = head
				}
				require.NoError(t, os.WriteFile(spec, []byte(text), 0600))
				require.NoError(t, saveOutcome(original, source))
				calls := 0
				success := successfulSavedEvidenceJudge(t, &calls)
				judge := savedEvidenceJudge(func(request *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
					require.Equal(t, tc.want, request.ModelID)
					require.Empty(t, request.SessionID)
					require.Empty(t, request.WorkspaceDir)
					return success(request)
				})
				require.NoError(t, runGradeWithJudge(t.Context(), io.Discard, io.Discard, spec, "task", source, "", tc.override, "", false, "", judge))
				require.Equal(t, 1, calls)
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				err = runGradeWithJudge(ctx, io.Discard, io.Discard, spec, "task", source, "", tc.override, "", false, "", execution.NewEvidenceJudgeExecutor())
				require.ErrorIs(t, err, context.Canceled, "the real executor must admit the effective model before observing cancellation")
				require.NotContains(t, err.Error(), "request profile")
			})
		}
	}
}
