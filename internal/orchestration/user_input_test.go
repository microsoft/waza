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

func (e *questionEngine) Execute(_ context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
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

	require.Len(t, run.UserInputRequests, 2)
	first, second := run.UserInputRequests[0], run.UserInputRequests[1]
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
	require.Equal(t, run.UserInputRequests, restored.TestOutcomes[0].Runs[0].UserInputRequests)
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
			require.Len(t, run.UserInputRequests, tc.records)
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
		require.Len(t, run.UserInputRequests, 2)
		require.Equal(t, 1, run.UserInputRequests[0].Sequence)
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
	require.Nil(t, run.UserInputRequests)
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
	require.Nil(t, run.UserInputRequests)
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
			require.Len(t, run.UserInputRequests, len(tc.sources))
			for i, record := range run.UserInputRequests {
				require.Equal(t, i+1, record.Sequence)
				require.Equal(t, "native-session", record.SessionID)
				require.Equal(t, tc.sources[i], record.Source)
			}
			require.Equal(t, "staging", *run.UserInputRequests[0].Answer)
			if tc.status == models.StatusError {
				require.Len(t, sdkErrors, 1, "the SDK must receive an error, never a default answer")
				require.Nil(t, run.UserInputRequests[1].Answer)
			} else {
				require.Empty(t, sdkErrors)
				require.Equal(t, "fixes login", *run.UserInputRequests[1].Answer)
				require.True(t, *run.UserInputRequests[1].WasFreeform)
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

	require.Len(t, run.UserInputRequests, 3)
	sources := []string{run.UserInputRequests[0].Source, run.UserInputRequests[1].Source, run.UserInputRequests[2].Source}
	require.Equal(t, []string{"responder", "script", "responder"}, sources)
	require.Equal(t, "Ada", *run.UserInputRequests[0].Answer)
	require.True(t, *run.UserInputRequests[0].WasFreeform)
	require.Equal(t, "staging", *run.UserInputRequests[1].Answer)
	require.Equal(t, "west", *run.UserInputRequests[2].Answer)
	require.False(t, *run.UserInputRequests[2].WasFreeform, "a reply equal to an offered choice is a selection")

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
			require.Len(t, run.UserInputRequests, 1)
			require.NotEmpty(t, run.UserInputRequests[0].Error)
			require.Equal(t, "responder", run.UserInputRequests[0].Source)
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
	require.Nil(t, run.UserInputRequests)
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
	require.Len(t, run.UserInputRequests, 1)
	require.Equal(t, "responder", run.UserInputRequests[0].Source)
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
