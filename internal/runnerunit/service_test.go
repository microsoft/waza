package runnerunit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runneridentity"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

func fixture(t *testing.T) runnercontract.Request {
	t.Helper()
	data, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	req, err := runnercontract.Parse(data)
	require.NoError(t, err)
	return *req
}

func wire(t *testing.T, request runnercontract.Request) []byte {
	t.Helper()
	data, err := json.Marshal(request)
	require.NoError(t, err)
	return data
}

type instrumented struct {
	execution.AgentEngine
	mode                          string
	initializes, calls, shutdowns int
	request                       *execution.ExecutionRequest
	workspace                     string
}

func (e *instrumented) Initialize(ctx context.Context) error {
	e.initializes++
	if e.mode == "initialize" {
		return errors.New("injected initialize")
	}
	return e.AgentEngine.Initialize(ctx)
}

func (e *instrumented) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.calls++
	e.request = req
	switch e.mode {
	case "execution":
		return nil, errors.New("injected execution")
	case "nil":
		return nil, nil
	case "unsuccessful":
		return &execution.ExecutionResponse{Success: false, FinalOutput: "hello"}, nil
	case "timeout", "canceled":
		<-ctx.Done()
		return nil, ctx.Err()
	}
	resp, err := e.AgentEngine.Execute(ctx, req)
	if resp != nil {
		e.workspace = resp.WorkspaceDir
	}
	return resp, err
}

func (e *instrumented) Shutdown(ctx context.Context) error {
	e.shutdowns++
	err := e.AgentEngine.Shutdown(ctx)
	if e.mode == "shutdown" {
		return errors.Join(err, errors.New("injected shutdown"))
	}
	return err
}

func TestAdmittedAttemptInstrumentationAndRepeatedDispatch(t *testing.T) {
	request := fixture(t)
	var engines []*instrumented
	for range 3 {
		report, input := admit(wire(t, request))
		require.NotNil(t, input)
		report.Mode = "mock"
		e := &instrumented{AgentEngine: execution.NewMockEngine(input.spec.Config.ModelID)}
		engines = append(engines, e)
		report = execute(t.Context(), report, input, e)
		require.Equal(t, "completed", report.Status, "%+v", report.Diagnostics)
		require.Equal(t, request.Selection, *report.Requested)
		require.Equal(t, request.Selection, *report.Resolved)
		require.False(t, report.AdjacentWork)
		require.Equal(t, 1, report.Calls)
		require.Equal(t, 2, report.Observation.RunNumber)
		require.Equal(t, 2, report.Observation.Attempts)
		require.Equal(t, models.StatusPassed, report.Observation.Status)
		require.NotEmpty(t, report.Observation.Validations)
		require.Contains(t, report.Observation.FinalOutput, "hello")
		require.Nil(t, report.Observation.Usage, "mock evidence must not invent billing")
		require.Equal(t, 1, e.initializes)
		require.Equal(t, 1, e.calls)
		require.Equal(t, 1, e.shutdowns)
		require.Equal(t, "hello", e.request.Message)
		require.Equal(t, "Selected task", e.request.TaskName)
		require.True(t, e.request.NoSkills)
		require.Empty(t, e.request.SkillPaths)
		require.Empty(t, e.request.MCPServers)
		require.Empty(t, e.request.CommandMocks)
		require.Empty(t, e.request.SessionID)
		require.Empty(t, e.request.WorkspaceDir)
		require.NotEmpty(t, e.workspace)
		_, err := os.Stat(e.workspace)
		require.ErrorIs(t, err, os.ErrNotExist, "invocation-owned workspace must be removed")
	}
	require.Len(t, engines, 3, "no cache replay or shared engine")
	require.NotEqual(t, engines[0].workspace, engines[1].workspace)
}

func TestExactUnitProductionAttemptAndRepeatedDispatch(t *testing.T) {
	request := fixture(t)
	var engines []*execution.MockEngine
	service := NewService(func(cfg models.Config) (execution.AgentEngine, error) {
		engine := execution.NewMockEngine(cfg.ModelID)
		engines = append(engines, engine)
		return engine, nil
	})
	for range 3 {
		report := service.Run(t.Context(), wire(t, request))
		require.Equal(t, "completed", report.Status, "%+v", report.Diagnostics)
		require.Equal(t, "mock", report.Mode)
		expected, err := runneridentity.Derive(&request)
		require.NoError(t, err)
		require.Equal(t, expected, report.Identity)
		require.Equal(t, request.Selection, *report.Resolved)
		require.False(t, report.AdjacentWork)
		require.Equal(t, 1, report.Calls)
		require.Equal(t, models.StatusPassed, report.Observation.Status)
		require.Equal(t, 2, report.Observation.RunNumber)
		require.Equal(t, 2, report.Observation.Attempts)
		require.Contains(t, report.Observation.FinalOutput, "hello")
		require.Nil(t, report.Observation.Usage)
	}
	require.Len(t, engines, 3)
	require.NotSame(t, engines[0], engines[1])
}

func TestCapturedPromptPresenceReachesFrozenExecutionRequest(t *testing.T) {
	for _, prompt := range []string{"", "hello"} {
		t.Run("prompt="+prompt, func(t *testing.T) {
			request := fixture(t)
			task, err := json.Marshal(map[string]any{
				"id": request.Selection.Task, "name": "Prompt presence",
				"inputs":   map[string]string{"prompt": prompt},
				"expected": map[string]any{"output_contains": []string{"Mock response for:"}},
			})
			require.NoError(t, err)
			request.Input.Task = string(task)
			report, input := admit(wire(t, request))
			require.Equal(t, "ready", report.Status, "%+v", report.Diagnostics)
			require.NotNil(t, input)
			engine := &instrumented{AgentEngine: execution.NewMockEngine("mock")}
			report.Mode = "mock"
			report = execute(t.Context(), report, input, engine)
			require.Equal(t, "completed", report.Status)
			require.Equal(t, 1, engine.calls)
			require.Equal(t, prompt, engine.request.Message, "explicit empty prompt must survive request preparation")
			require.Equal(t, models.StatusPassed, report.Observation.Status)
			requireReceiptSchema(t, report)
		})
	}
}

func TestRuntimeAdmissionRejectsNonMockBeforeLifecycle(t *testing.T) {
	for _, control := range []string{"native-like", "mock-wrapper", "copilot", "nil", "typed-nil"} {
		t.Run(control, func(t *testing.T) {
			e := &instrumented{}
			var engine execution.AgentEngine = e
			switch control {
			case "mock-wrapper":
				e.AgentEngine = execution.NewMockEngine("mock")
			case "copilot":
				engine = execution.NewCopilotEngineBuilder("must-not-launch", nil).Build()
			case "nil":
				engine = nil
			case "typed-nil":
				engine = (*execution.MockEngine)(nil)
			}
			req := fixture(t)
			req.AllowPaidExecution = true
			report := NewService(func(models.Config) (execution.AgentEngine, error) {
				return engine, nil
			}).Run(t.Context(), wire(t, req))
			require.Equal(t, "execution_error", report.Status)
			require.Equal(t, "valid", report.Validation)
			require.Equal(t, req.Selection, *report.Resolved)
			require.Empty(t, report.Mode)
			require.Zero(t, report.Calls)
			require.Nil(t, report.Observation)
			require.Equal(t, 2, report.ExitCode())
			require.Contains(t, report.Diagnostics[0].Message, "requires a non-nil concrete *execution.MockEngine")
			require.Zero(t, e.initializes)
			require.Zero(t, e.calls)
			require.Zero(t, e.shutdowns, "rejected runtime shutdown could itself launch native work")
			requireReceiptSchema(t, report)
		})
	}
}

func TestInvalidSelectionsNeverConstructRuntime(t *testing.T) {
	tests := map[string]func(*runnercontract.Request){
		"wrong-shard":          func(r *runnercontract.Request) { r.Selection.Shard = "neighbor" },
		"wrong-task":           func(r *runnercontract.Request) { r.Selection.Task = "neighbor" },
		"zero-trial":           func(r *runnercontract.Request) { r.Selection.Trial = 0 },
		"zero-attempt":         func(r *runnercontract.Request) { r.Selection.Attempt = 0 },
		"trial-out-of-range":   func(r *runnercontract.Request) { r.Selection.Trial = 4 },
		"attempt-out-of-range": func(r *runnercontract.Request) { r.Selection.Attempt = 4 },
		"glob-task":            func(r *runnercontract.Request) { r.Selection.Task = "*" },
		"missing-profile":      func(r *runnercontract.Request) { r.RequiredCapabilities = []string{"exact-unit"} },
		"optional-profile": func(r *runnercontract.Request) {
			r.RequiredCapabilities = []string{"exact-unit"}
			r.OptionalCapabilities = []string{"inline.mock-text"}
		},
		"negotiation-first": func(r *runnercontract.Request) {
			r.RequiredCapabilities = append(r.RequiredCapabilities, "terminal-result")
			r.Input.Eval = "not valid yaml: ["
		},
		"native-denied": func(r *runnercontract.Request) {
			r.Input.Eval = strings.Replace(r.Input.Eval, "executor: mock", "executor: copilot-sdk", 1)
		},
		"native-consent-not-capability": func(r *runnercontract.Request) {
			r.AllowPaidExecution = true
			r.Input.Eval = strings.Replace(r.Input.Eval, "executor: mock", "executor: copilot-sdk", 1)
		},
		"disabled-task":  func(r *runnercontract.Request) { r.Input.Task += "enabled: false\n" },
		"duplicate-id":   func(r *runnercontract.Request) { r.Input.Task += "id: neighbor\n" },
		"multi-document": func(r *runnercontract.Request) { r.Input.Task += "---\nid: neighbor\ninputs: {prompt: neighbor}\n" },
		"prompt-file": func(r *runnercontract.Request) {
			r.Input.Task = strings.Replace(r.Input.Task, "prompt: hello", "prompt_file: ./must-not-be-read.txt", 1)
		},
		"workdir": func(r *runnercontract.Request) {
			r.Input.Task = strings.Replace(r.Input.Task, "prompt: hello", "prompt: hello\n  workdir: neighbor", 1)
		},
		"files": func(r *runnercontract.Request) {
			r.Input.Task = strings.Replace(r.Input.Task, "prompt: hello", "prompt: hello\n  files: [{path: must-not-read}]", 1)
		},
		"environment": func(r *runnercontract.Request) {
			r.Input.Task = strings.Replace(r.Input.Task, "prompt: hello", "prompt: hello\n  environment: {UNSUPPORTED: value}", 1)
		},
		"relative-prompt": func(r *runnercontract.Request) {
			r.Input.Task = strings.Replace(r.Input.Task, "prompt: hello", "prompt: read ./secret", 1)
		},
		"follow-up": func(r *runnercontract.Request) {
			r.Input.Task = strings.Replace(r.Input.Task, "prompt: hello", "prompt: hello\n  follow_up_prompts: [neighbor]", 1)
		},
		"blank-expectation": func(r *runnercontract.Request) {
			r.Input.Task = strings.Replace(r.Input.Task, "- hello", "- ' '", 1)
		},
		"empty-grader": func(r *runnercontract.Request) {
			r.Input.Eval += "graders:\n  - name: empty\n    type: text\n    config: {}\n"
		},
		"invalid-regex": func(r *runnercontract.Request) {
			r.Input.Eval += "graders:\n  - name: bad\n    type: text\n    config:\n      regex_match: ['[']\n"
		},
		"task-empty-grader": func(r *runnercontract.Request) {
			r.Input.Task += "graders:\n  - name: empty\n    type: text\n    config: {}\n"
		},
		"csv-discovery": func(r *runnercontract.Request) { r.Input.Eval += "tasks_from: neighboring.csv\n" },
		"baseline":      func(r *runnercontract.Request) { r.Input.Eval += "baseline: true\n" },
		"parallel": func(r *runnercontract.Request) {
			r.Input.Eval = strings.Replace(r.Input.Eval, "executor: mock", "executor: mock\n  parallel: true", 1)
		},
		"explicit-skills": func(r *runnercontract.Request) {
			r.Input.Task += "skill_directories: [neighbor]\n"
		},
		"overflow-timeout": func(r *runnercontract.Request) {
			r.Input.Eval = strings.Replace(r.Input.Eval, "timeout_seconds: 30", "timeout_seconds: 9223372036854775807", 1)
		},
		"overflow-task-timeout": func(r *runnercontract.Request) {
			r.Input.Task += "timeout_seconds: 9223372036854775807\n"
		},
		"negative-attempt-count": func(r *runnercontract.Request) {
			r.Input.Eval = strings.Replace(r.Input.Eval, "max_attempts: 3", "max_attempts: -1", 1)
		},
		"filtered-skill-paths-not-portable": func(r *runnercontract.Request) {
			r.Input.Eval = strings.Replace(r.Input.Eval, "executor: mock", "executor: mock\n  skill_directories: [neighbor]\n  disabled_skills: ['*']", 1)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			req := fixture(t)
			before := wire(t, req)
			mutate(&req)
			data := wire(t, req)
			require.NotEqual(t, string(before), string(data), "control must mutate real source")
			service := NewService(func(models.Config) (execution.AgentEngine, error) {
				t.Fatal("invalid selection constructed a runtime")
				return nil, nil
			})
			for range 2 {
				report := service.Run(t.Context(), data)
				require.Equal(t, "rejected", report.Status, "%+v", report)
				require.Nil(t, report.Resolved)
				require.Nil(t, report.Observation)
				require.Nil(t, report.Identity)
				require.Zero(t, report.Calls)
				require.NotZero(t, report.ExitCode())
			}
		})
	}
}

func TestSelectionPresenceAndAlternatives(t *testing.T) {
	base := wire(t, fixture(t))
	for _, field := range []string{"shard", "task", "trial", "attempt"} {
		for _, replacement := range []string{"omitted", "null", `""`, "0", "false"} {
			t.Run(field+"/"+replacement, func(t *testing.T) {
				var fields map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(base, &fields))
				var selection map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(fields["selection"], &selection))
				if replacement == "omitted" {
					delete(selection, field)
				} else {
					selection[field] = json.RawMessage(replacement)
				}
				var err error
				fields["selection"], err = json.Marshal(selection)
				require.NoError(t, err)
				data, err := json.Marshal(fields)
				require.NoError(t, err)
				report := NewService(func(models.Config) (execution.AgentEngine, error) {
					t.Fatal("invalid wire constructed runtime")
					return nil, nil
				}).Run(t.Context(), data)
				require.Equal(t, "rejected", report.Status)
				require.Nil(t, report.Identity)
				require.Zero(t, report.Calls)
			})
		}
	}
	for _, alternative := range []string{"first-attempt", "task-disabled-skills", "explicit-deny-all", "crlf", "failed-check"} {
		t.Run(alternative, func(t *testing.T) {
			req := fixture(t)
			switch alternative {
			case "first-attempt":
				req.Selection.Trial, req.Selection.Attempt = 1, 1
				req.Input.Eval = strings.Replace(req.Input.Eval, "  max_attempts: 3\n", "", 1)
			case "task-disabled-skills":
				req.Input.Task += "skill_directories: []\n"
			case "explicit-deny-all":
				req.Input.Eval = strings.Replace(req.Input.Eval, "executor: mock", "executor: mock\n  disabled_skills: ['*']", 1)
			case "crlf":
				req.Input.Eval = strings.ReplaceAll(req.Input.Eval, "\n", "\r\n")
				req.Input.Task = strings.ReplaceAll(req.Input.Task, "\n", "\r\n")
				require.Contains(t, req.Input.Task, "\r\n")
			case "failed-check":
				req.Input.Task = strings.Replace(req.Input.Task, "- hello", "- missing-assertion", 1)
			}
			report := NewService(nil).Run(t.Context(), wire(t, req))
			require.Equal(t, "completed", report.Status, "%+v", report.Diagnostics)
			require.Equal(t, 1, report.Calls)
			require.Equal(t, req.Selection.Trial, report.Observation.RunNumber)
			require.Equal(t, req.Selection.Attempt, report.Observation.Attempts)
			expectedExit := 0
			if alternative == "failed-check" {
				expectedExit = 1
				require.Equal(t, models.StatusFailed, report.Observation.Status)
			}
			require.Equal(t, expectedExit, report.ExitCode())
		})
	}
}

func TestLifecycleFailuresAndCancellation(t *testing.T) {
	for _, mode := range []string{"initialize", "execution", "nil", "unsuccessful", "shutdown", "timeout", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				req := fixture(t)
				req.TimeoutMS = 1
				e := &instrumented{AgentEngine: execution.NewMockEngine("mock"), mode: mode}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if mode == "canceled" {
					go func() { synctest.Wait(); cancel() }()
				}
				report, input := admit(wire(t, req))
				require.NotNil(t, input)
				report.Mode = "mock"
				ctx, stop := context.WithTimeout(ctx, time.Millisecond)
				defer stop()
				report = execute(ctx, report, input, e)
				require.Equal(t, "execution_error", report.Status)
				expected, err := runneridentity.Derive(&req)
				require.NoError(t, err)
				require.Equal(t, expected, report.Identity, "later failures retain input identity, not execution success")
				require.Equal(t, 2, report.ExitCode())
				require.Equal(t, 1, e.initializes)
				require.Equal(t, 1, e.shutdowns)
				calls := 1
				if mode == "initialize" {
					calls = 0
				}
				require.Equal(t, calls, e.calls)
				require.Equal(t, calls, report.Calls)
				require.NotEmpty(t, report.Diagnostics)
				requireReceiptSchema(t, report)
			})
		})
	}
	for _, factory := range []EngineFactory{
		func(models.Config) (execution.AgentEngine, error) { return nil, errors.New("factory failed") },
		func(models.Config) (execution.AgentEngine, error) { return nil, nil },
	} {
		report := NewService(factory).Run(t.Context(), wire(t, fixture(t)))
		require.Equal(t, "execution_error", report.Status)
		require.NotNil(t, report.Identity)
		require.Zero(t, report.Calls)
		require.Empty(t, report.Mode)
		requireReceiptSchema(t, report)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	report := NewService(func(models.Config) (execution.AgentEngine, error) {
		t.Fatal("canceled request constructed runtime")
		return nil, nil
	}).Run(ctx, wire(t, fixture(t)))
	require.Equal(t, "execution_error", report.Status)
	require.NotNil(t, report.Identity)
	require.Zero(t, report.Calls)
	require.Empty(t, report.Mode)
	requireReceiptSchema(t, report)
}

func TestFactoryMutationAndCancellation(t *testing.T) {
	for _, control := range []string{"mutation", "cancellation"} {
		t.Run(control, func(t *testing.T) {
			req := fixture(t)
			data := wire(t, req)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var mutated models.Config
			report := NewService(func(cfg models.Config) (execution.AgentEngine, error) {
				cfg.EngineType = "copilot-sdk"
				cfg.ModelID = "mutated-model"
				cfg.TrialsPerTask = 99
				mutated = cfg
				clear(data)
				if control == "cancellation" {
					cancel()
				}
				return execution.NewMockEngine(cfg.ModelID), nil
			}).Run(ctx, data)
			require.Equal(t, "copilot-sdk", mutated.EngineType)
			require.Equal(t, "mutated-model", mutated.ModelID)
			require.Equal(t, 99, mutated.TrialsPerTask)
			require.Equal(t, req.Selection, *report.Resolved)
			require.Equal(t, "mock", report.Mode)
			if control == "cancellation" {
				require.Equal(t, "execution_error", report.Status)
				require.Zero(t, report.Calls)
				require.Nil(t, report.Observation)
				require.Contains(t, report.Diagnostics[0].Message, "context canceled")
			} else {
				require.Equal(t, "completed", report.Status, "%+v", report.Diagnostics)
				require.Equal(t, 1, report.Calls)
				require.Equal(t, 2, report.Observation.RunNumber)
				require.Equal(t, 2, report.Observation.Attempts)
				require.Contains(t, report.Observation.FinalOutput, "hello")
			}
			requireReceiptSchema(t, report)
		})
	}
}

func TestValidationIsOfflineAndReceiptSchema(t *testing.T) {
	req := fixture(t)
	directory := t.TempDir()
	t.Chdir(directory)
	t.Setenv("COPILOT_CLI_PATH", filepath.Join(directory, "must-not-launch"))
	require.NoError(t, os.WriteFile(".waza.yaml", []byte("invalid: ["), 0o600))
	data, err := json.Marshal(req)
	require.NoError(t, err)
	report := Validate(data)
	require.Equal(t, "ready", report.Status, "%+v", report.Diagnostics)
	require.Zero(t, report.Calls)
	require.Nil(t, report.Observation)
	for _, result := range []Report{report, NewService(nil).Run(t.Context(), data), Validate([]byte("{}"))} {
		requireReceiptSchema(t, result)
	}
}

func requireReceiptSchema(t *testing.T, report Report) {
	t.Helper()
	schema := receiptSchema(t)
	encoded, err := json.Marshal(report)
	require.NoError(t, err)
	var doc any
	require.NoError(t, json.Unmarshal(encoded, &doc))
	require.NoError(t, schema.Validate(doc))
}

func receiptSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	c.UseLoader(schemaloader.Offline{})
	for name, text := range map[string]string{
		"runner-unit-1.0.schema.json":                 schemas.RunnerUnitSchemaJSON,
		"runner-negotiation-response-1.0.schema.json": schemas.RunnerNegotiationResponseSchemaJSON,
	} {
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
		require.NoError(t, err)
		require.NoError(t, c.AddResource("https://microsoft.github.io/waza/schemas/"+name, doc))
	}
	schema, err := c.Compile("https://microsoft.github.io/waza/schemas/runner-unit-1.0.schema.json")
	require.NoError(t, err)
	return schema
}

func TestReceiptRuntimePresence(t *testing.T) {
	schema := receiptSchema(t)
	report := NewService(nil).Run(t.Context(), wire(t, fixture(t)))
	require.Equal(t, "completed", report.Status)
	data, err := json.Marshal(report)
	require.NoError(t, err)
	for _, control := range []string{"completed-no-mode", "unadmitted-error", "unadmitted-observation", "unadmitted-call", "native-mode", "null-mode"} {
		t.Run(control, func(t *testing.T) {
			var fields map[string]any
			require.NoError(t, json.Unmarshal(data, &fields))
			require.NoError(t, schema.Validate(fields))
			delete(fields, "execution_mode")
			switch control {
			case "unadmitted-error", "unadmitted-observation", "unadmitted-call":
				fields["status"] = "execution_error"
				fields["diagnostics"] = []any{map[string]any{"code": "execution_error", "message": "admitting runtime failed"}}
				fields["engine_calls"] = 0
				delete(fields, "observation")
				if control == "unadmitted-observation" {
					fields["observation"] = map[string]any{}
				}
				if control == "unadmitted-call" {
					fields["engine_calls"] = 1
				}
			case "native-mode":
				fields["execution_mode"] = "copilot-sdk"
			case "null-mode":
				fields["execution_mode"] = nil
			}
			if control == "unadmitted-error" {
				require.NoError(t, schema.Validate(fields))
			} else {
				require.Error(t, schema.Validate(fields))
			}
		})
	}
}
