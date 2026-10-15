package graders

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func acquiredRubricFixture(t *testing.T) (models.GraderConfig, []byte, string, string) {
	t.Helper()
	data := []byte("---\nname: acquired-test\nversion: 1.0.0\nscale: pass-fail\ndescription: Test rubric.\n---\nOriginal rubric body.\n")
	name := filepath.Join(t.TempDir(), "rubric.md")
	require.NoError(t, os.WriteFile(name, data, 0o600))
	config := models.GraderConfig{Identifier: "native", Kind: models.GraderKindPrompt, Weight: 2,
		Parameters: models.PromptGraderParameters{Rubric: name}}
	declaration, err := evidence.JSONDigest(config)
	require.NoError(t, err)
	sum := sha256.Sum256(data)
	return config, data, declaration.SHA256, hex.EncodeToString(sum[:])
}

func TestAcquiredRubricCanonicalParityAndOwnedBytes(t *testing.T) {
	for _, mutation := range []string{"delete", "replace"} {
		t.Run(mutation, func(t *testing.T) {
			config, data, declaration, content := acquiredRubricFixture(t)
			parameters, ok := config.Parameters.(models.PromptGraderParameters)
			require.True(t, ok)
			ordinary, err := NewPromptGrader(config.Identifier, parameters)
			require.NoError(t, err)
			acquired, err := AcquireRubric(config, declaration, content, data)
			require.NoError(t, err)
			before := bytes.Clone(data)
			for index := range data {
				data[index] = 'x'
			}
			if mutation == "delete" {
				require.NoError(t, os.Remove(parameters.Rubric))
			} else {
				require.NoError(t, os.WriteFile(parameters.Rubric, []byte("not a valid rubric"), 0o600))
			}
			var messages []string
			executor := &fakePromptExecutor{execute: func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
				messages = append(messages, req.Message)
				require.Equal(t, "selected-model", req.ModelID)
				require.Equal(t, "low", req.ReasoningEffort)
				_, err := req.Tools[0].Handler(copilot.ToolInvocation{Arguments: map[string]any{"description": "native", "reason": "pass"}})
				require.NoError(t, err)
				return &execution.ExecutionResponse{Success: true}, nil
			}}
			context := &Context{Output: " \nexact\n ", OutputPresent: true, TestCase: &models.TestCase{}, Executor: executor}
			context.TestCase.Stimulus.Message = " input "
			ordinary.args.Model, ordinary.args.ReasoningEffort = "selected-model", "low"
			control, err := ordinary.Grade(t.Context(), context)
			require.NoError(t, err)
			results, err := RunAllWithAcquiredRubrics(t.Context(), []models.GraderConfig{config}, &models.TestCase{}, context,
				"selected-model", "low", false, []*AcquiredRubric{acquired})
			require.NoError(t, err)
			require.Len(t, messages, 2)
			require.Equal(t, messages[0], messages[1])
			require.True(t, results[config.Identifier].Passed)
			require.Equal(t, control.Score, results[config.Identifier].Score)
			require.Equal(t, float64(2), results[config.Identifier].Weight)
			controlRubric, ok := control.Details["rubric"].(map[string]any)
			require.True(t, ok)
			acquiredRubric, ok := results[config.Identifier].Details["rubric"].(map[string]any)
			require.True(t, ok)
			require.Equal(t, "file:"+parameters.Rubric, controlRubric["source"])
			require.Equal(t, "acquired:sha256:"+content, acquiredRubric["source"])
			require.Equal(t, before, acquired.data)
			_, err = NewPromptGrader(config.Identifier, parameters)
			require.Error(t, err, "ordinary path API must still read the current pathname")
			unchanged, err := evidence.JSONDigest(config)
			require.NoError(t, err)
			require.Equal(t, declaration, unchanged.SHA256)
		})
	}
}

func TestAcquiredRubricAdmission(t *testing.T) {
	config, data, declaration, content := acquiredRubricFixture(t)
	for _, mode := range []string{"empty", "oversize", "missing-declaration", "wrong-declaration", "missing-content",
		"wrong-content", "missing-identifier", "wrong-type", "missing-reference", "invalid-rubric"} {
		t.Run(mode, func(t *testing.T) {
			cfg, input, decl, sha := config, bytes.Clone(data), declaration, content
			switch mode {
			case "empty":
				input = nil
			case "oversize":
				input = make([]byte, maxAcquiredRubricBytes+1)
			case "missing-declaration":
				decl = ""
			case "wrong-declaration":
				decl = strings.Repeat("0", 64)
			case "missing-content":
				sha = ""
			case "wrong-content":
				sha = strings.Repeat("0", 64)
			case "missing-identifier":
				cfg.Identifier = ""
			case "wrong-type":
				cfg.Parameters = models.TextGraderParameters{}
			case "missing-reference":
				cfg.Parameters = models.PromptGraderParameters{Prompt: "inline"}
			case "invalid-rubric":
				input = []byte("invalid rubric")
				sum := sha256.Sum256(input)
				sha = hex.EncodeToString(sum[:])
			}
			value, err := AcquireRubric(cfg, decl, sha, input)
			require.Error(t, err)
			require.Nil(t, value)
		})
	}
}

func TestAcquiredRubricSelectionFailsBeforeNativeCreation(t *testing.T) {
	for _, mode := range []string{"missing", "nil", "duplicate", "extra", "wrong-id", "wrong-ref", "wrong-declaration",
		"duplicate-config", "mutated-owned-bytes", "validators", "nil-task"} {
		t.Run(mode, func(t *testing.T) {
			config, data, declaration, content := acquiredRubricFixture(t)
			item, err := AcquireRubric(config, declaration, content, data)
			require.NoError(t, err)
			items := []*AcquiredRubric{item}
			configs := []models.GraderConfig{config}
			task := &models.TestCase{}
			switch mode {
			case "missing":
				items = nil
			case "nil":
				items = []*AcquiredRubric{nil}
			case "duplicate":
				items = append(items, item)
			case "extra":
				copy := *item
				copy.identifier = "extra"
				items = append(items, &copy)
			case "wrong-id":
				configs[0].Identifier = "other"
			case "wrong-ref":
				params, ok := config.Parameters.(models.PromptGraderParameters)
				require.True(t, ok)
				params.Rubric = "missing.md"
				configs[0].Parameters = params
			case "wrong-declaration":
				configs[0].Weight++
			case "duplicate-config":
				configs = append(configs, config)
			case "mutated-owned-bytes":
				item.data[0] = 'x'
			case "validators":
				task.Validators = []models.ValidatorInline{{}}
			case "nil-task":
				task = nil
			}
			parameters, ok := config.Parameters.(models.PromptGraderParameters)
			require.True(t, ok)
			require.NoError(t, os.Remove(parameters.Rubric))
			calls := 0
			executor := &fakePromptExecutor{execute: func(*execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
				calls++
				return nil, errors.New("must not execute")
			}}
			results, err := RunAllWithAcquiredRubrics(t.Context(), configs, task, &Context{Executor: executor}, "", "", false, items)
			require.Error(t, err)
			require.Nil(t, results)
			require.Zero(t, calls)
			require.NotContains(t, err.Error(), "read rubric file", "no pathname fallback or native construction")
		})
	}
}
