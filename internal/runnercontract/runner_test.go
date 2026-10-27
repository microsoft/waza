package runnercontract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func fixture(t *testing.T) *Request {
	t.Helper()
	data, err := os.ReadFile("testdata/request.json")
	require.NoError(t, err)
	req, err := Parse(data)
	require.NoError(t, err)
	return req
}

func wire(t *testing.T, req *Request) []byte {
	t.Helper()
	data, err := json.Marshal(req)
	require.NoError(t, err)
	return data
}

func TestRunnerExactUnitAndRetry(t *testing.T) {
	req := fixture(t)
	for range 2 {
		result := NewService().Run(t.Context(), wire(t, req))
		require.Equal(t, Success, result.Outcome, "%+v", result.Diagnostics)
		require.NoError(t, ValidateResult(&result))
		require.Equal(t, req.Selection, *result.Resolved)
		require.False(t, result.AdjacentWork)
		require.Equal(t, 2, result.Observation.RunNumber)
		require.Equal(t, 2, result.Observation.Attempts)
		require.Contains(t, result.Observation.FinalOutput, "hello")
		require.NoDirExists(t, result.Observation.WorkspaceDir, "invocation-owned workspace must be removed")
	}
}

func TestRunnerNegotiationConformance(t *testing.T) {
	cases := []struct {
		name     string
		versions []string
		required []string
		optional []string
		outcome  Outcome
	}{
		{"supported", []string{"1.0"}, []string{"exact-unit"}, []string{}, Success},
		{"downgraded", []string{"2.0", "1.0"}, []string{"exact-unit"}, []string{"future"}, Success},
		{"unsupported-version", []string{"2.0"}, []string{}, []string{}, UnsupportedCapability},
		{"unsupported-required", []string{"1.0"}, []string{"future"}, []string{}, UnsupportedCapability},
		{"conflicting-capabilities", []string{"1.0"}, []string{"exact-unit"}, []string{"exact-unit"}, UnsupportedCapability},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := fixture(t)
			req.Versions, req.RequiredCapabilities, req.OptionalCapabilities = tc.versions, tc.required, tc.optional
			calls := 0
			service := NewService()
			service.execute = func(_ context.Context, input *admitted) (*models.RunResult, error) {
				calls++
				require.Equal(t, req.Selection, input.request.Selection)
				return passed(), nil
			}
			result := service.Run(t.Context(), wire(t, req))
			require.Equal(t, tc.outcome, result.Outcome, "%+v", result.Diagnostics)
			require.NoError(t, ValidateResult(&result))
			if tc.outcome == Success {
				require.Equal(t, 1, calls)
				require.Equal(t, "1.0", result.Negotiation.Version)
				if tc.name == "downgraded" {
					require.Equal(t, []string{"future"}, result.Negotiation.UnavailableOptional)
				}
			} else {
				require.Zero(t, calls)
				require.Nil(t, result.Identity)
			}
		})
	}
}

func TestRunnerNegotiationFrozenFixtures(t *testing.T) {
	data, err := os.ReadFile("testdata/negotiation.json")
	require.NoError(t, err)
	var cases []struct {
		Versions            []string `json:"versions"`
		Required            []string `json:"required"`
		Optional            []string `json:"optional"`
		Outcome             Outcome  `json:"outcome"`
		SelectedVersion     string   `json:"selected_version"`
		UnavailableOptional []string `json:"unavailable_optional"`
	}
	require.NoError(t, json.Unmarshal(data, &cases))
	require.Len(t, cases, 4)
	for _, tc := range cases {
		request := fixture(t)
		request.Versions, request.RequiredCapabilities, request.OptionalCapabilities = tc.Versions, tc.Required, tc.Optional
		result := NewService().Run(t.Context(), wire(t, request))
		require.Equal(t, tc.Outcome, result.Outcome)
		require.Equal(t, tc.SelectedVersion, result.Negotiation.Version)
		require.Equal(t, tc.UnavailableOptional, result.Negotiation.UnavailableOptional)
	}
}

func TestRunnerPublishedExampleConformance(t *testing.T) {
	data, err := os.ReadFile("../../examples/runner/request.json")
	require.NoError(t, err)
	request, err := Parse(data)
	require.NoError(t, err)
	require.Equal(t, fixture(t), request)
}

func passed() *models.RunResult {
	return &models.RunResult{RunNumber: 2, Attempts: 2, Status: models.StatusPassed,
		Validations: map[string]models.GraderResults{"check": {Passed: true}}}
}

func TestRunnerRejectsBeforeExecution(t *testing.T) {
	for _, name := range []string{
		"missing-shard", "wrong-shard", "wrong-task", "zero-trial", "large-trial", "zero-attempt", "large-attempt",
		"disabled", "duplicate-task-key", "unknown-source-field", "prompt-file", "context-fixture",
		"external-resource", "traversal-resource", "duplicate-resource", "hooks", "live-engine", "csv",
	} {
		t.Run(name, func(t *testing.T) {
			req := fixture(t)
			switch name {
			case "missing-shard":
				req.Selection.Shard = ""
			case "wrong-shard":
				req.Selection.Shard = "neighbor"
			case "wrong-task":
				req.Selection.Task = "neighbor"
			case "zero-trial":
				req.Selection.Trial = 0
			case "large-trial":
				req.Selection.Trial = 4
			case "zero-attempt":
				req.Selection.Attempt = 0
			case "large-attempt":
				req.Selection.Attempt = 4
			case "disabled":
				req.Input.Task += "enabled: false\n"
			case "duplicate-task-key":
				req.Input.Task += "id: adjacent\n"
			case "unknown-source-field":
				req.Input.Task += "unknown: true\n"
			case "prompt-file":
				req.Input.Task = strings.Replace(req.Input.Task, "prompt: hello", "prompt_file: forbidden.txt", 1)
			case "context-fixture":
				req.Input.Task = strings.Replace(req.Input.Task, "prompt: hello", "prompt: hello\n  context:\n    fixture: forbidden", 1)
			case "external-resource":
				req.Input.Task = strings.Replace(req.Input.Task, "prompt: hello", "prompt: hello\n  files:\n    - path: forbidden.txt", 1)
			case "traversal-resource":
				req.Input.Task = strings.Replace(req.Input.Task, "prompt: hello", "prompt: hello\n  files:\n    - path: ../forbidden\n      content: secret", 1)
			case "duplicate-resource":
				req.Input.Task = strings.Replace(req.Input.Task, "prompt: hello", "prompt: hello\n  files:\n    - path: same\n      content: a\n    - path: same\n      content: b", 1)
			case "hooks":
				req.Input.Eval += "hooks:\n  before_run:\n    - echo forbidden\n"
			case "live-engine":
				req.Input.Eval = strings.Replace(req.Input.Eval, "executor: mock", "executor: copilot-sdk", 1)
			case "csv":
				req.Input.Eval += "tasks_from: forbidden.csv\n"
			}
			service := NewService()
			service.execute = func(context.Context, *admitted) (*models.RunResult, error) {
				t.Fatal("invalid input reached execution")
				return nil, nil
			}
			result := service.Run(t.Context(), wire(t, req))
			require.Equal(t, InvalidRequest, result.Outcome, "%+v", result.Diagnostics)
			require.Nil(t, result.Observation)
			require.False(t, result.SelectionValid)
			require.NoError(t, ValidateResult(&result))
		})
	}
}

func TestRunnerStrictWireAdmission(t *testing.T) {
	valid := string(wire(t, fixture(t)))
	for _, bad := range []string{
		`null`, `{}`, valid + valid,
		strings.Replace(valid, `"schema_version":"1.0"`, `"schema_version":"1.0","schema_version":"1.0"`, 1),
		strings.Replace(valid, `"trial":2`, `"trial":null`, 1),
		strings.Replace(valid, `"trial":2`, `"trial":"2"`, 1),
		strings.Replace(valid, `"trial":2`, `"trial":9007199254740993`, 1),
		strings.Replace(valid, `"run_key":"conformance"`, `"run_key":"\ud800"`, 1),
		strings.Replace(valid, `"schema_version":"1.0"`, `"schema_version":"2.0"`, 1),
		strings.Replace(valid, `"schema_version":"1.0"`, `"extra":true,"schema_version":"1.0"`, 1),
		strings.Repeat("x", MaxRequestBytes+1),
	} {
		result := NewService().Run(t.Context(), []byte(bad))
		require.Equal(t, InvalidRequest, result.Outcome)
		require.NoError(t, ValidateResult(&result))
	}
}

func TestRunnerEveryTerminalOutcome(t *testing.T) {
	for _, outcome := range []Outcome{Success, EvaluationFailure, InfrastructureFailure, Canceled, Timeout} {
		t.Run(string(outcome), func(t *testing.T) {
			req := fixture(t)
			service := NewService()
			calls := 0
			service.execute = func(ctx context.Context, _ *admitted) (*models.RunResult, error) {
				calls++
				switch outcome {
				case Success:
					return passed(), nil
				case EvaluationFailure:
					return &models.RunResult{RunNumber: 2, Attempts: 2, Status: models.StatusFailed}, nil
				case InfrastructureFailure:
					return nil, errors.New("injected infrastructure failure")
				case Canceled:
					return nil, context.Canceled
				default:
					<-ctx.Done()
					return nil, ctx.Err()
				}
			}
			if outcome == Timeout {
				req.TimeoutMS = 1
			}
			result := service.Run(t.Context(), wire(t, req))
			require.Equal(t, outcome, result.Outcome, "%+v", result.Diagnostics)
			require.Equal(t, outcome.ExitCode(), result.ExitCode)
			require.Equal(t, 1, calls)
			var b bytes.Buffer
			require.NoError(t, Write(&b, &result))
			var object any
			decoder := json.NewDecoder(&b)
			require.NoError(t, decoder.Decode(&object))
			require.ErrorIs(t, decoder.Decode(&object), io.EOF, "exactly one envelope")
		})
	}
}

func TestRunnerPartialPanicAndCancellation(t *testing.T) {
	for _, name := range []string{"panic", "nil", "partial", "late-cancel", "pre-cancel"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if name == "pre-cancel" {
				cancel()
			}
			service := NewService()
			service.execute = func(context.Context, *admitted) (*models.RunResult, error) {
				switch name {
				case "pre-cancel":
					t.Fatal("canceled request executed")
				case "panic":
					panic("injected")
				case "partial":
					return &models.RunResult{Status: models.StatusPassed}, nil
				case "late-cancel":
					cancel()
					return passed(), nil
				}
				return nil, nil
			}
			result := service.Run(ctx, wire(t, fixture(t)))
			expected := InfrastructureFailure
			if strings.Contains(name, "cancel") {
				expected = Canceled
			}
			require.Equal(t, expected, result.Outcome)
			require.NoError(t, ValidateResult(&result))
		})
	}
}

func TestRunnerNativeExecutionRequiresExplicitConsent(t *testing.T) {
	for _, mode := range []string{"no-consent", "no-capability", "explicit"} {
		t.Run(mode, func(t *testing.T) {
			req := fixture(t)
			req.Input.Eval = strings.Replace(req.Input.Eval, "executor: mock", "executor: copilot-sdk", 1)
			req.AllowPaidExecution = mode != "no-consent"
			if mode != "no-capability" {
				req.RequiredCapabilities = append(req.RequiredCapabilities, "inline.native-text")
			}
			service := NewService()
			calls := 0
			service.execute = func(_ context.Context, input *admitted) (*models.RunResult, error) {
				calls++
				require.Equal(t, "copilot-sdk", input.spec.Config.EngineType)
				return passed(), nil
			}
			result := service.Run(t.Context(), wire(t, req))
			if mode == "explicit" {
				require.Equal(t, Success, result.Outcome, "%+v", result.Diagnostics)
				require.Equal(t, 1, calls, "instrumented boundary only; no live SDK execution")
			} else {
				require.Equal(t, InvalidRequest, result.Outcome)
				require.Zero(t, calls)
				require.Contains(t, result.Diagnostics[0].Message, "may incur model usage")
			}
		})
	}
}

func TestRunnerIdentityVectors(t *testing.T) {
	req := fixture(t)
	input, err := admit(req)
	require.NoError(t, err)
	data, err := os.ReadFile("testdata/identity.json")
	require.NoError(t, err)
	var expected Identity
	require.NoError(t, json.Unmarshal(data, &expected))
	require.Equal(t, expected, *input.id)
	equivalent := *req
	equivalent.Input.Eval = "# ignored comment\r\n" + strings.ReplaceAll(req.Input.Eval, "\n", "\r\n")
	equivalent.Input.Task = strings.Replace(req.Input.Task, "id: selected", `id: "selected"`, 1)
	other, err := admit(&equivalent)
	require.NoError(t, err)
	require.Equal(t, *input.id, *other.id)
	for _, change := range []string{"run-key", "shard", "task-content", "trial", "attempt", "deadline"} {
		material := *req
		switch change {
		case "run-key":
			material.RunKey += "-new"
		case "shard":
			material.Selection.Shard, material.Input.Shard = "shard-b", "shard-b"
		case "task-content":
			material.Input.Task = strings.Replace(material.Input.Task, "prompt: hello", "prompt: hello changed", 1)
		case "trial":
			material.Selection.Trial++
		case "attempt":
			material.Selection.Attempt++
		case "deadline":
			material.TimeoutMS++
		}

		next, err := admit(&material)
		require.NoError(t, err)
		require.NotEqual(t, input.id.Attempt, next.id.Attempt, change)
		require.NotEqual(t, input.id.CanonicalDigest, next.id.CanonicalDigest, change)
		if slices.Contains([]string{"task-content", "trial", "attempt"}, change) {
			require.Equal(t, input.id.Run, next.id.Run)
		}
		if change == "attempt" {
			require.Equal(t, input.id.Trial, next.id.Trial)
		}
	}
}

func TestRunnerCanonicalUnicodeVector(t *testing.T) {
	value := []any{"<&>\u2028\u2029é", map[string]any{"z": 1, "a": "汉"}}
	actual, err := digest("unicode", value)
	require.NoError(t, err)
	require.Equal(t, "7bcc1f245058d2e534c311e7b8084fab0eac9fdc2c248cde56e017ea9b3b930e", actual)
}
