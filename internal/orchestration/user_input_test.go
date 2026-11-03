package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/responder"
	"github.com/microsoft/waza/internal/testutil/copilotfake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// questionEngine simulates an agent that asks native SDK questions during a
// turn by invoking the request's registered user-input callback, exactly as
// the SDK would. It never calls a model.
type questionEngine struct {
	mu        sync.Mutex
	questions map[string][]copilot.UserInputRequest
	failOn    string
	scripts   []*execution.UserInputScript
	sessions  int
}

func (e *questionEngine) Initialize(context.Context) error       { return nil }
func (e *questionEngine) Shutdown(context.Context) error         { return nil }
func (e *questionEngine) SessionUsage(string) *models.UsageStats { return nil }

func (e *questionEngine) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.scripts = append(e.scripts, req.UserInput)
	sessionID := req.SessionID
	if sessionID == "" {
		e.sessions++
		sessionID = fmt.Sprintf("session-%d", e.sessions)
	}
	resp := &execution.ExecutionResponse{
		FinalOutput: "response to: " + req.Message, SessionID: sessionID,
		WorkspaceDir: "/workspace/q", DurationMs: 1, Success: true,
	}
	defer req.UserInput.BindTurn(ctx)()
	if handler := req.UserInput.Handler(); handler != nil {
		for _, question := range e.questions[req.Message] {
			if _, err := handler(question, copilot.UserInputInvocation{SessionID: sessionID}); err != nil {
				resp.Success = false
				resp.ErrorMsg = err.Error()
			}
		}
	}
	resp.UserInputRequests = req.UserInput.Records()
	if req.Message == e.failOn {
		return nil, fmt.Errorf("engine failure after questions")
	}
	return resp, nil
}

func writeUserInputEval(t *testing.T, task string, version string) (*config.EvalConfig, string) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(task), 0o600))
	spec := &models.EvalSpec{
		SchemaVersion: version,
		SpecIdentity:  models.SpecIdentity{Name: "native-user-input"},
		Config:        models.Config{EngineType: "copilot-sdk", ModelID: "test-model", TrialsPerTask: 1, TimeoutSec: 30},
		Tasks:         []string{"task.yaml"},
	}
	if version == models.ScenarioSchemaVersion {
		spec.Scenario = "native-questions"
		spec.Config.NativeUserInput = true
	}
	return config.NewEvalConfig(spec, config.WithSpecDir(dir)), dir
}

const userInputTaskYAML = `id: native-questions
name: Native questions
inputs:
  prompt: start
  follow_up_prompts:
    - continue
user_input_responses:
  - question: Deploy where\?
    answer: staging
  - question: "Release note.*"
    answer: fixes login
`

func TestRunBenchmarkAnswersNativeQuestionsAcrossInitialAndResumedTurns(t *testing.T) {
	engine := &questionEngine{questions: map[string][]copilot.UserInputRequest{
		"start":    {{Question: "Deploy where?", Choices: []string{"staging", "prod"}, AllowFreeform: new(bool)}},
		"continue": {{Question: "Release note for this change?"}},
	}}
	cfg, _ := writeUserInputEval(t, userInputTaskYAML, models.ScenarioSchemaVersion)
	outcome, err := NewEvalRunner(cfg, engine, WithSkipGraders()).RunBenchmark(context.Background())
	require.NoError(t, err)
	require.Len(t, outcome.TestOutcomes, 1)
	run := outcome.TestOutcomes[0].Runs[0]
	require.Equal(t, models.StatusSkipped, run.Status, run.ErrorMsg)

	require.Len(t, engine.scripts, 2)
	require.NotNil(t, engine.scripts[0])
	require.Same(t, engine.scripts[0], engine.scripts[1], "initial and resumed turns must share one ordered script")

	require.Len(t, models.UserInputRecordsOf(run.UserInputRequests), 2)
	first, second := models.UserInputRecordsOf(run.UserInputRequests)[0], models.UserInputRecordsOf(run.UserInputRequests)[1]
	require.Equal(t, 1, first.Sequence)
	require.Equal(t, "session-1", first.SessionID)
	require.Equal(t, []string{"staging", "prod"}, first.Choices)
	require.Equal(t, "staging", *first.Answer)
	require.False(t, *first.WasFreeform)
	require.Equal(t, 2, second.Sequence)
	require.Equal(t, "session-1", second.SessionID)
	require.Nil(t, second.Choices)
	require.Equal(t, "fixes login", *second.Answer)
	require.True(t, *second.WasFreeform)
	require.Equal(t, models.UserInputSourceScript, second.Source)

	data, err := json.Marshal(outcome)
	require.NoError(t, err)
	var wire struct {
		SchemaVersion string `json:"schemaVersion"`
		Tasks         []struct {
			Runs []struct {
				UserInputRequests []map[string]any `json:"user_input_requests"`
			} `json:"runs"`
		} `json:"tasks"`
	}
	require.NoError(t, json.Unmarshal(data, &wire))
	require.Equal(t, models.CurrentResultSchemaVersion, wire.SchemaVersion)
	records := wire.Tasks[0].Runs[0].UserInputRequests
	require.Len(t, records, 2)
	require.Equal(t, map[string]any{
		"sequence": float64(1), "session_id": "session-1", "question": "Deploy where?",
		"choices": []any{"staging", "prod"}, "source": "script", "answer": "staging", "was_freeform": false,
	}, records[0])
	require.Contains(t, records[1], "choices")
	require.Nil(t, records[1]["choices"], "absent offered choices are recorded as null, not []")

	restored, err := models.ParseEvaluationOutcome(data, "results.json")
	require.NoError(t, err)
	require.Equal(t, models.UserInputRecordsOf(run.UserInputRequests), models.UserInputRecordsOf(restored.TestOutcomes[0].Runs[0].UserInputRequests))
}

func TestRunBenchmarkFailsClosedOnNativeQuestionProblems(t *testing.T) {
	cases := []struct {
		name      string
		questions map[string][]copilot.UserInputRequest
		failOn    string
		message   string
		records   int
		turns     int
	}{
		{
			name: "unconsumed scripted answer",
			questions: map[string][]copilot.UserInputRequest{
				"start": {{Question: "Deploy where?", Choices: []string{"staging", "prod"}}},
			},
			message: "1 scripted answer(s) were not used, starting at user_input_responses[1]",
			records: 1, turns: 2,
		},
		{
			name: "unexpected question in resumed turn",
			questions: map[string][]copilot.UserInputRequest{
				"start":    {{Question: "Deploy where?", Choices: []string{"staging", "prod"}}},
				"continue": {{Question: "Delete production?", Choices: []string{"yes"}}},
			},
			message: "native user input request 2 rejected: question does not match user_input_responses[1].question",
			records: 2, turns: 2,
		},
		{
			name: "records survive an engine error",
			questions: map[string][]copilot.UserInputRequest{
				"start": {{Question: "Deploy where?", Choices: []string{"staging", "prod"}}},
			},
			failOn:  "start",
			message: "engine failure after questions",
			records: 1, turns: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine := &questionEngine{questions: tc.questions, failOn: tc.failOn}
			cfg, _ := writeUserInputEval(t, userInputTaskYAML, models.ScenarioSchemaVersion)
			outcome, err := NewEvalRunner(cfg, engine, WithSkipGraders()).RunBenchmark(context.Background())
			require.NoError(t, err)
			run := outcome.TestOutcomes[0].Runs[0]
			require.Equal(t, models.StatusError, run.Status)
			require.Contains(t, run.ErrorMsg, tc.message)
			require.Len(t, models.UserInputRecordsOf(run.UserInputRequests), tc.records)
			require.Len(t, engine.scripts, tc.turns)
		})
	}
}

func TestRunBenchmarkUsesFreshNativeQuestionScriptPerTrial(t *testing.T) {
	engine := &questionEngine{questions: map[string][]copilot.UserInputRequest{
		"start":    {{Question: "Deploy where?", Choices: []string{"staging", "prod"}}},
		"continue": {{Question: "Release note?"}},
	}}
	cfg, _ := writeUserInputEval(t, userInputTaskYAML, models.ScenarioSchemaVersion)
	cfg.Spec().Config.TrialsPerTask = 2
	outcome, err := NewEvalRunner(cfg, engine, WithSkipGraders()).RunBenchmark(context.Background())
	require.NoError(t, err)
	runs := outcome.TestOutcomes[0].Runs
	require.Len(t, runs, 2)
	for _, run := range runs {
		require.Equal(t, models.StatusSkipped, run.Status, run.ErrorMsg)
		require.Len(t, models.UserInputRecordsOf(run.UserInputRequests), 2)
		require.Equal(t, 1, models.UserInputRecordsOf(run.UserInputRequests)[0].Sequence)
	}
	require.Len(t, engine.scripts, 4)
	require.Same(t, engine.scripts[0], engine.scripts[1])
	require.Same(t, engine.scripts[2], engine.scripts[3])
	require.NotSame(t, engine.scripts[0], engine.scripts[2])
}

func TestRunBenchmarkLeavesNativeQuestionCallbackUnsetWithoutScript(t *testing.T) {
	engine := &questionEngine{questions: map[string][]copilot.UserInputRequest{
		"start": {{Question: "Deploy where?"}},
	}}
	task := "id: plain\nname: Plain\ninputs:\n  prompt: start\n  follow_up_prompts: [continue]\n"
	cfg, _ := writeUserInputEval(t, task, models.ScenarioSchemaVersion)
	cfg.Spec().Config.NativeUserInput = false
	outcome, err := NewEvalRunner(cfg, engine, WithSkipGraders()).RunBenchmark(context.Background())
	require.NoError(t, err)
	run := outcome.TestOutcomes[0].Runs[0]
	require.Equal(t, models.StatusSkipped, run.Status, run.ErrorMsg)
	require.Nil(t, models.UserInputRecordsOf(run.UserInputRequests))
	require.Len(t, engine.scripts, 2)
	require.Nil(t, engine.scripts[0])
	require.Nil(t, engine.scripts[1])
}

func TestRunBenchmarkRejectsScriptedAnswersWithoutEvalFlag(t *testing.T) {
	for _, version := range []string{"", "1.4", models.ScenarioSchemaVersion} {
		engine := &questionEngine{}
		cfg, _ := writeUserInputEval(t, userInputTaskYAML, version)
		cfg.Spec().Config.NativeUserInput = false
		outcome, err := NewEvalRunner(cfg, engine, WithSkipGraders()).RunBenchmark(context.Background())
		require.ErrorContains(t, err, "user_input_responses requires config.native_user_input: true", version)
		require.Nil(t, outcome)
		require.Empty(t, engine.scripts)
	}
}

func TestRunBenchmarkScenarioResponderWithoutFlagLeavesNativeCallbackUnset(t *testing.T) {
	engine := &questionEngine{questions: map[string][]copilot.UserInputRequest{"start": {{Question: "Your name?"}}}}
	classifier := &recordingClassifier{between: responder.Decision{Kind: responder.DecisionStop}}
	task := "id: a\nname: A\ninputs:\n  prompt: start\n  responder: {instructions: x, max_followups: 1}\n"
	cfg, _ := writeUserInputEval(t, task, models.ScenarioSchemaVersion)
	cfg.Spec().Config.NativeUserInput = false
	runner := NewEvalRunner(cfg, engine, WithSkipGraders())
	runner.newClassifier = func(models.ResponderConfig, string) responderClassifier { return classifier }
	outcome, err := runner.RunBenchmark(context.Background())
	require.NoError(t, err)
	run := outcome.TestOutcomes[0].Runs[0]
	require.Equal(t, models.StatusSkipped, run.Status, run.ErrorMsg)
	require.Nil(t, models.UserInputRecordsOf(run.UserInputRequests))
	require.Nil(t, engine.scripts[0], "the adaptive user answers native questions only when the eval opts in")
	require.Len(t, classifier.messages, 1, "only the between-turn classification runs")
}

func TestRunBenchmarkFlagWithoutScriptOrResponderFailsClosed(t *testing.T) {
	engine := &questionEngine{questions: map[string][]copilot.UserInputRequest{"start": {{Question: "Anything?"}}}}
	cfg, _ := writeUserInputEval(t, "id: a\nname: A\ninputs:\n  prompt: start\n", models.ScenarioSchemaVersion)
	outcome, err := NewEvalRunner(cfg, engine, WithSkipGraders()).RunBenchmark(context.Background())
	require.NoError(t, err)
	run := outcome.TestOutcomes[0].Runs[0]
	require.Equal(t, models.StatusError, run.Status)
	require.Contains(t, run.ErrorMsg, "no scripted user_input_responses entry remains")
	require.NotNil(t, engine.scripts[0])
}

// TestProductionCopilotEngineAnswersNativeQuestionsFromTaskYAML drives the
// real CopilotEngine (with a CLI-free SDK client fake) from task YAML. The
// fake invokes only the OnUserInputRequest callback registered on the actual
// create and resume session configurations, as the SDK does.
func TestProductionCopilotEngineAnswersNativeQuestionsFromTaskYAML(t *testing.T) {
	for _, tc := range []struct {
		name     string
		followUp string
		status   models.Status
		message  string
		sources  []string
	}{
		{"scripted", "Release note for this change?", models.StatusSkipped, "", []string{"script", "script"}},
		{"unscripted resumed question", "Delete production?", models.StatusError,
			"follow-up 1/1: native user input request 2 rejected: question does not match user_input_responses[1].question",
			[]string{"script", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sdkErrors []error
			client := &copilotfake.Client{Send: func(config *copilot.SessionConfig, options copilot.MessageOptions) ([]copilot.SessionEvent, error) {
				require.NotNil(t, config.OnUserInputRequest, "callback must be registered for prompt %q", options.Prompt)
				question := copilot.UserInputRequest{Question: "Deploy where?", Choices: []string{"staging", "prod"}}
				if options.Prompt == "continue" {
					question = copilot.UserInputRequest{Question: tc.followUp}
				}
				answer, err := config.OnUserInputRequest(question, copilot.UserInputInvocation{SessionID: "native-session"})
				if err != nil {
					sdkErrors = append(sdkErrors, err)
				} else {
					text := "selected " + answer.Answer
					return []copilot.SessionEvent{{Data: &copilot.AssistantMessageData{Content: text}}}, nil
				}
				return []copilot.SessionEvent{{Data: &copilot.AssistantMessageData{Content: "continued without answer"}}}, nil
			}}
			engine := client.Engine()
			require.NoError(t, engine.Initialize(t.Context()))
			t.Cleanup(func() { require.NoError(t, engine.Shutdown(context.Background())) })

			cfg, _ := writeUserInputEval(t, userInputTaskYAML, models.ScenarioSchemaVersion)
			outcome, err := NewEvalRunner(cfg, engine, WithSkipGraders()).RunBenchmark(t.Context())
			require.NoError(t, err)
			run := outcome.TestOutcomes[0].Runs[0]
			require.Equal(t, tc.status, run.Status, run.ErrorMsg)
			require.Contains(t, run.ErrorMsg, tc.message)
			require.EqualValues(t, 1, client.Creates.Load())
			require.EqualValues(t, 1, client.Resumes.Load())
			require.Len(t, models.UserInputRecordsOf(run.UserInputRequests), len(tc.sources))
			for i, record := range models.UserInputRecordsOf(run.UserInputRequests) {
				require.Equal(t, i+1, record.Sequence)
				require.Equal(t, "native-session", record.SessionID)
				require.Equal(t, tc.sources[i], record.Source)
			}
			require.Equal(t, "staging", *models.UserInputRecordsOf(run.UserInputRequests)[0].Answer)
			if tc.status == models.StatusError {
				require.Len(t, sdkErrors, 1, "the SDK must receive an error, never a default answer")
				require.Nil(t, models.UserInputRecordsOf(run.UserInputRequests)[1].Answer)
			} else {
				require.Empty(t, sdkErrors)
				require.Equal(t, "fixes login", *models.UserInputRecordsOf(run.UserInputRequests)[1].Answer)
				require.True(t, *models.UserInputRecordsOf(run.UserInputRequests)[1].WasFreeform)
			}
		})
	}
}

func TestBuildGraderContextIncludesNativeQuestionRecords(t *testing.T) {
	answer := "staging"
	records := []models.UserInputRecord{{Sequence: 1, SessionID: "s", Question: "Q", Source: models.UserInputSourceScript, Answer: &answer}}
	runner := NewEvalRunner(config.NewEvalConfig(&models.EvalSpec{}), &questionEngine{})
	ctx := runner.buildGraderContext(&models.TestCase{}, &execution.ExecutionResponse{UserInputRequests: records}, nil)
	require.Equal(t, records, ctx.UserInputRequests)
}

// recordingClassifier is a model-free adaptive responder: it answers native
// questions from a table and returns a fixed decision between turns.
type recordingClassifier struct {
	mu       sync.Mutex
	messages []string
	answers  map[string]responder.Decision
	between  responder.Decision
	closed   int
}

func (c *recordingClassifier) Classify(_ context.Context, message string) (responder.Decision, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messages = append(c.messages, message)
	for question, decision := range c.answers {
		if strings.Contains(message, "Question: "+question+"\n") {
			return decision, nil
		}
	}
	return c.between, nil
}

func (c *recordingClassifier) Close(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed++
	return nil
}

const adaptiveTaskYAML = `id: adaptive-questions
name: Adaptive questions
inputs:
  prompt: start
  responder:
    instructions: You deploy to staging and your name is Ada.
    max_followups: 2
user_input_responses:
  - question: Deploy where\?
    answer: staging
`

func TestRunBenchmarkAdaptiveResponderAnswersUnscriptedNativeQuestions(t *testing.T) {
	engine := &questionEngine{questions: map[string][]copilot.UserInputRequest{
		"start": {
			{Question: "Your name?"},
			{Question: "Deploy where?", Choices: []string{"staging", "prod"}},
			{Question: "Region?", Choices: []string{"east", "west"}, AllowFreeform: new(bool)},
		},
	}}
	classifier := &recordingClassifier{
		answers: map[string]responder.Decision{
			"Your name?": {Kind: responder.DecisionReply, Answer: "Ada"},
			"Region?":    {Kind: responder.DecisionReply, Answer: "west"},
		},
		between: responder.Decision{Kind: responder.DecisionStop},
	}
	cfg, _ := writeUserInputEval(t, adaptiveTaskYAML, models.ScenarioSchemaVersion)
	runner := NewEvalRunner(cfg, engine, WithSkipGraders())
	created := 0
	runner.newClassifier = func(models.ResponderConfig, string) responderClassifier { created++; return classifier }
	outcome, err := runner.RunBenchmark(context.Background())
	require.NoError(t, err)
	run := outcome.TestOutcomes[0].Runs[0]
	require.Equal(t, models.StatusSkipped, run.Status, run.ErrorMsg)
	require.Equal(t, models.ResponderOutcomeStopped, run.Responder.Outcome)

	require.Len(t, models.UserInputRecordsOf(run.UserInputRequests), 3)
	sources := []string{models.UserInputRecordsOf(run.UserInputRequests)[0].Source, models.UserInputRecordsOf(run.UserInputRequests)[1].Source, models.UserInputRecordsOf(run.UserInputRequests)[2].Source}
	require.Equal(t, []string{"responder", "script", "responder"}, sources)
	require.Equal(t, "Ada", *models.UserInputRecordsOf(run.UserInputRequests)[0].Answer)
	require.True(t, *models.UserInputRecordsOf(run.UserInputRequests)[0].WasFreeform)
	require.Equal(t, "staging", *models.UserInputRecordsOf(run.UserInputRequests)[1].Answer)
	require.Equal(t, "west", *models.UserInputRecordsOf(run.UserInputRequests)[2].Answer)
	require.False(t, *models.UserInputRecordsOf(run.UserInputRequests)[2].WasFreeform, "a reply equal to an offered choice is a selection")

	require.Equal(t, 1, created, "native questions and between-turn replies share one responder per attempt")
	require.Equal(t, 1, classifier.closed)
	require.Len(t, classifier.messages, 3, "two native questions plus one between-turn classification")
	require.Contains(t, classifier.messages[1], "- east\n- west\n")
	require.Contains(t, classifier.messages[1], "You must reply with exactly one of the offered choices.")
	require.NotContains(t, strings.Join(classifier.messages, "\n"), "Deploy where?", "scripted questions never reach the model")
}

func TestRunBenchmarkAdaptiveResponderNativeAnswerFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		decision responder.Decision
		message  string
	}{
		{"abstain", responder.Decision{Kind: responder.DecisionAbstain, Reason: "unknown name"}, "responder abstained: unknown name"},
		{"stop", responder.Decision{Kind: responder.DecisionStop}, "responder chose to stop instead of answering"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := &questionEngine{questions: map[string][]copilot.UserInputRequest{"start": {{Question: "Your name?"}}}}
			classifier := &recordingClassifier{answers: map[string]responder.Decision{"Your name?": tc.decision}}
			task := "id: a\nname: A\ninputs:\n  prompt: start\n  responder: {instructions: x, max_followups: 1}\n"
			cfg, _ := writeUserInputEval(t, task, models.ScenarioSchemaVersion)
			runner := NewEvalRunner(cfg, engine, WithSkipGraders())
			runner.newClassifier = func(models.ResponderConfig, string) responderClassifier { return classifier }
			outcome, err := runner.RunBenchmark(context.Background())
			require.NoError(t, err)
			run := outcome.TestOutcomes[0].Runs[0]
			require.Equal(t, models.StatusError, run.Status)
			require.Contains(t, run.ErrorMsg, tc.message)
			require.Len(t, models.UserInputRecordsOf(run.UserInputRequests), 1)
			require.NotEmpty(t, models.UserInputRecordsOf(run.UserInputRequests)[0].Error)
			require.Equal(t, "responder", models.UserInputRecordsOf(run.UserInputRequests)[0].Source)
		})
	}
}

func TestRunBenchmarkLegacyAdaptiveResponderLeavesNativeCallbackUnset(t *testing.T) {
	engine := &questionEngine{questions: map[string][]copilot.UserInputRequest{"start": {{Question: "Your name?"}}}}
	classifier := &recordingClassifier{between: responder.Decision{Kind: responder.DecisionStop}}
	task := "id: a\nname: A\ninputs:\n  prompt: start\n  responder: {instructions: x, max_followups: 1}\n"
	cfg, _ := writeUserInputEval(t, task, "1.4")
	runner := NewEvalRunner(cfg, engine, WithSkipGraders())
	runner.newClassifier = func(models.ResponderConfig, string) responderClassifier { return classifier }
	outcome, err := runner.RunBenchmark(context.Background())
	require.NoError(t, err)
	run := outcome.TestOutcomes[0].Runs[0]
	require.Equal(t, models.StatusSkipped, run.Status, run.ErrorMsg)
	require.Nil(t, models.UserInputRecordsOf(run.UserInputRequests))
	require.Nil(t, engine.scripts[0], "legacy adaptive tasks keep their existing behavior without the native question tool")
	require.Len(t, classifier.messages, 1)
}

// TestProductionEngineAdaptiveResponderAnswersNativeQuestionMidTurn uses the
// real responder classifier and CopilotEngine (CLI-free fake client). The
// responder session runs while the agent's turn is blocked on its question.
func TestProductionEngineAdaptiveResponderAnswersNativeQuestionMidTurn(t *testing.T) {
	var agentAnswers []copilot.UserInputResponse
	var responderPrompts []string
	client := &copilotfake.Client{Send: func(config *copilot.SessionConfig, options copilot.MessageOptions) ([]copilot.SessionEvent, error) {
		for _, tool := range config.Tools {
			if tool.Name == "responder_reply" || tool.Name == "responder_stop" {
				responderPrompts = append(responderPrompts, options.Prompt)
				name, args := "responder_stop", map[string]any{}
				if strings.Contains(options.Prompt, "Question: Your name?") {
					name, args = "responder_reply", map[string]any{"answer": "Ada"}
				}
				for _, candidate := range config.Tools {
					if candidate.Name == name {
						_, err := candidate.Handler(copilot.ToolInvocation{ToolName: name, Arguments: args})
						require.NoError(t, err)
					}
				}
				return nil, nil
			}
		}
		require.NotNil(t, config.OnUserInputRequest)
		answer, err := config.OnUserInputRequest(copilot.UserInputRequest{Question: "Your name?"}, copilot.UserInputInvocation{SessionID: "agent"})
		require.NoError(t, err)
		agentAnswers = append(agentAnswers, answer)
		return []copilot.SessionEvent{{Data: &copilot.AssistantMessageData{Content: "Hello " + answer.Answer}}}, nil
	}}
	engine := client.Engine()
	require.NoError(t, engine.Initialize(t.Context()))
	t.Cleanup(func() { require.NoError(t, engine.Shutdown(context.Background())) })

	task := "id: a\nname: A\ninputs:\n  prompt: start\n  responder: {instructions: Your name is Ada., max_followups: 1}\n"
	cfg, _ := writeUserInputEval(t, task, models.ScenarioSchemaVersion)
	outcome, err := NewEvalRunner(cfg, engine, WithSkipGraders()).RunBenchmark(t.Context())
	require.NoError(t, err)
	run := outcome.TestOutcomes[0].Runs[0]
	require.Equal(t, models.StatusSkipped, run.Status, run.ErrorMsg)
	require.Equal(t, []copilot.UserInputResponse{{Answer: "Ada", WasFreeform: true}}, agentAnswers)
	require.Len(t, models.UserInputRecordsOf(run.UserInputRequests), 1)
	require.Equal(t, "responder", models.UserInputRecordsOf(run.UserInputRequests)[0].Source)
	require.Equal(t, models.ResponderOutcomeStopped, run.Responder.Outcome)
	require.Len(t, responderPrompts, 2)
	require.Contains(t, responderPrompts[0], "Your name is Ada.", "the first responder call seeds its instructions")
	require.Contains(t, responderPrompts[1], "Hello Ada", "the between-turn call reuses the same responder session")
}

// deadlineClassifier records the deadline each Classify call receives.
type deadlineClassifier struct {
	recordingClassifier
	deadlines []time.Duration
}

func (c *deadlineClassifier) Classify(ctx context.Context, message string) (responder.Decision, error) {
	if deadline, ok := ctx.Deadline(); ok {
		c.deadlines = append(c.deadlines, time.Until(deadline))
	} else {
		c.deadlines = append(c.deadlines, 0)
	}
	return c.recordingClassifier.Classify(ctx, message)
}

func TestNativeQuestionResponderCallUsesTaskTimeout(t *testing.T) {
	engine := &questionEngine{questions: map[string][]copilot.UserInputRequest{"start": {{Question: "Your name?"}}}}
	classifier := &deadlineClassifier{recordingClassifier: recordingClassifier{
		answers: map[string]responder.Decision{"Your name?": {Kind: responder.DecisionReply, Answer: "Ada"}},
		between: responder.Decision{Kind: responder.DecisionStop},
	}}
	task := "id: a\nname: A\ntimeout_seconds: 240\ninputs:\n  prompt: start\n  responder: {instructions: x, max_followups: 1}\n"
	cfg, _ := writeUserInputEval(t, task, models.ScenarioSchemaVersion)
	runner := NewEvalRunner(cfg, engine, WithSkipGraders())
	runner.newClassifier = func(models.ResponderConfig, string) responderClassifier { return classifier }
	outcome, err := runner.RunBenchmark(context.Background())
	require.NoError(t, err)
	require.Equal(t, models.StatusSkipped, outcome.TestOutcomes[0].Runs[0].Status, outcome.TestOutcomes[0].Runs[0].ErrorMsg)
	require.NotEmpty(t, classifier.deadlines)
	inTurn := classifier.deadlines[0]
	require.Greater(t, inTurn, 200*time.Second, "the in-turn responder call must not fall back to the SDK's 60 second default")
	require.LessOrEqual(t, inTurn, 240*time.Second)
}

func TestRunBenchmarkKeepsEmptyNativeQuestionRecordsDistinctFromAbsent(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			engine := &questionEngine{}
			cfg, _ := writeUserInputEval(t, "id: a\nname: A\ninputs:\n  prompt: start\n", models.ScenarioSchemaVersion)
			cfg.Spec().Config.NativeUserInput = enabled
			outcome, err := NewEvalRunner(cfg, engine, WithSkipGraders()).RunBenchmark(context.Background())
			require.NoError(t, err)
			run := outcome.TestOutcomes[0].Runs[0]
			require.Equal(t, models.StatusSkipped, run.Status, run.ErrorMsg)
			data, err := json.Marshal(outcome)
			require.NoError(t, err)
			restored, err := models.ParseEvaluationOutcome(data, "results.json")
			require.NoError(t, err)
			got := models.UserInputRecordsOf(restored.TestOutcomes[0].Runs[0].UserInputRequests)
			if enabled {
				require.NotNil(t, models.UserInputRecordsOf(run.UserInputRequests))
				require.Empty(t, models.UserInputRecordsOf(run.UserInputRequests))
				require.Contains(t, string(data), `"user_input_requests":[]`, "zero recorded questions is evidence, not absence")
				require.NotNil(t, got)
				require.Empty(t, got)
			} else {
				require.Nil(t, models.UserInputRecordsOf(run.UserInputRequests))
				require.NotContains(t, string(data), "user_input_requests")
				require.Nil(t, got)
			}
		})
	}
}

// detachedQuestionEngine returns from its turn while the question callback is
// still waiting on the responder, as happens when a turn times out.
type detachedQuestionEngine struct {
	questionEngine
	started chan struct{}
}

func (e *detachedQuestionEngine) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	defer req.UserInput.BindTurn(ctx)()
	handler := req.UserInput.Handler()
	go func() {
		_, _ = handler(copilot.UserInputRequest{Question: "Your name?"}, copilot.UserInputInvocation{SessionID: "s"})
	}()
	<-e.started
	return &execution.ExecutionResponse{SessionID: "s", WorkspaceDir: "/workspace/q",
		ErrorMsg: "turn timed out", UserInputRequests: req.UserInput.Records()}, nil
}

type slowAbstainClassifier struct {
	started chan struct{}
	once    sync.Once
}

func (c *slowAbstainClassifier) Classify(context.Context, string) (responder.Decision, error) {
	c.once.Do(func() { close(c.started) })
	time.Sleep(100 * time.Millisecond)
	return responder.Decision{Kind: responder.DecisionAbstain, Reason: "unknown"}, nil
}

func (c *slowAbstainClassifier) Close(context.Context) error { return nil }

func TestRunBenchmarkDrainsQuestionStillWithResponder(t *testing.T) {
	started := make(chan struct{})
	engine := &detachedQuestionEngine{started: started}
	task := "id: a\nname: A\ninputs:\n  prompt: start\n  responder: {instructions: x, max_followups: 1}\n"
	cfg, _ := writeUserInputEval(t, task, models.ScenarioSchemaVersion)
	runner := NewEvalRunner(cfg, engine, WithSkipGraders())
	runner.newClassifier = func(models.ResponderConfig, string) responderClassifier {
		return &slowAbstainClassifier{started: started}
	}
	outcome, err := runner.RunBenchmark(context.Background())
	require.NoError(t, err)
	run := outcome.TestOutcomes[0].Runs[0]
	require.Equal(t, models.StatusError, run.Status)
	require.Len(t, models.UserInputRecordsOf(run.UserInputRequests), 1)
	record := models.UserInputRecordsOf(run.UserInputRequests)[0]
	require.Nil(t, record.Answer)
	require.Contains(t, record.Error, "responder could not answer: responder abstained: unknown",
		"the saved record must be the drained outcome, not a pending entry")
	require.Contains(t, run.ErrorMsg, "turn timed out")
}

type stuckClassifier struct {
	started, release, closed chan struct{}
	once                     sync.Once
}

func (c *stuckClassifier) Classify(context.Context, string) (responder.Decision, error) {
	c.once.Do(func() { close(c.started) })
	<-c.release
	return responder.Decision{Kind: responder.DecisionAbstain, Reason: "late"}, nil
}

func (c *stuckClassifier) Close(context.Context) error {
	close(c.closed)
	return nil
}

func TestRunBenchmarkDoesNotBlockOnResponderOutlivingCleanup(t *testing.T) {
	previousDrain, previousCleanup := execution.UserInputDrainTimeout, responderCleanupTimeout
	execution.UserInputDrainTimeout, responderCleanupTimeout = 50*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { execution.UserInputDrainTimeout, responderCleanupTimeout = previousDrain, previousCleanup })
	classifier := &stuckClassifier{started: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	engine := &detachedQuestionEngine{started: classifier.started}
	task := "id: a\nname: A\ninputs:\n  prompt: start\n  responder: {instructions: x, max_followups: 1}\n"
	cfg, _ := writeUserInputEval(t, task, models.ScenarioSchemaVersion)
	runner := NewEvalRunner(cfg, engine, WithSkipGraders())
	runner.newClassifier = func(models.ResponderConfig, string) responderClassifier { return classifier }

	done := make(chan *models.EvaluationOutcome, 1)
	go func() {
		outcome, err := runner.RunBenchmark(context.Background())
		assert.NoError(t, err)
		done <- outcome
	}()
	var outcome *models.EvaluationOutcome
	select {
	case outcome = <-done:
	case <-time.After(10 * time.Second):
		close(classifier.release)
		t.Fatal("RunBenchmark blocked on a responder call that outlived drain and cleanup")
	}
	run := outcome.TestOutcomes[0].Runs[0]
	require.Equal(t, models.StatusError, run.Status)
	records := models.UserInputRecordsOf(run.UserInputRequests)
	require.Len(t, records, 1)
	require.Contains(t, records[0].Error, "did not answer before the run ended")
	select {
	case <-classifier.closed:
		t.Fatal("the session must not be closed while the call still uses it")
	default:
	}
	close(classifier.release)
	select {
	case <-classifier.closed:
	case <-time.After(10 * time.Second):
		t.Fatal("the responder session was never closed after the call returned")
	}
}
