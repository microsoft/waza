package execution

import (
	"context"
	"errors"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func strPtr(s string) *string { return &s }

func boolPtr(b bool) *bool { return &b }

func newTestScript(t *testing.T, responses []models.UserInputResponseConfig) *UserInputScript {
	t.Helper()
	script, err := NewUserInputScript(responses)
	require.NoError(t, err)
	return script
}

func TestUserInputScriptAnswersChoiceAndFreeformInOrder(t *testing.T) {
	script := newTestScript(t, []models.UserInputResponseConfig{
		{Question: "Which region?", Choices: &[]string{"east", "west"}, Choice: strPtr("west")},
		{QuestionRegex: `What name .*\?`, Freeform: strPtr("demo-app")},
	})
	handler := script.Handler()
	require.NotNil(t, handler)

	resp, err := handler(copilot.UserInputRequest{Question: "Which region?", Choices: []string{"east", "west"}, AllowFreeform: boolPtr(false)},
		copilot.UserInputInvocation{SessionID: "s-1"})
	require.NoError(t, err)
	require.Equal(t, copilot.UserInputResponse{Answer: "west", WasFreeform: false}, resp)

	resp, err = handler(copilot.UserInputRequest{Question: "What name should I use?"}, copilot.UserInputInvocation{SessionID: "s-1"})
	require.NoError(t, err)
	require.Equal(t, copilot.UserInputResponse{Answer: "demo-app", WasFreeform: true}, resp)

	require.NoError(t, script.Finish())
	zero, one := 0, 1
	require.Equal(t, []models.UserInputRecord{
		{Sequence: 1, SessionID: "s-1", Question: "Which region?", Choices: []string{"east", "west"}, AllowFreeform: boolPtr(false),
			Outcome: models.UserInputOutcomeAnswered, Source: models.UserInputSourceScript, ResponseIndex: &zero, Answer: strPtr("west"), WasFreeform: boolPtr(false)},
		{Sequence: 2, SessionID: "s-1", Question: "What name should I use?",
			Outcome: models.UserInputOutcomeAnswered, Source: models.UserInputSourceScript, ResponseIndex: &one, Answer: strPtr("demo-app"), WasFreeform: boolPtr(true)},
	}, script.Records())
}

func TestUserInputScriptFailsClosed(t *testing.T) {
	cases := []struct {
		name      string
		responses []models.UserInputResponseConfig
		request   copilot.UserInputRequest
		reason    string
		index     *int
	}{
		{"empty script", []models.UserInputResponseConfig{}, copilot.UserInputRequest{Question: "Continue?"},
			"no scripted user_input_responses entry remains", nil},
		{"question mismatch", []models.UserInputResponseConfig{{Question: "Continue?", Freeform: strPtr("yes")}},
			copilot.UserInputRequest{Question: "Continue now?"}, "question does not match user_input_responses[0].question", new(int)},
		{"regex is anchored", []models.UserInputResponseConfig{{QuestionRegex: "Continue", Freeform: strPtr("yes")}},
			copilot.UserInputRequest{Question: "Continue?"}, "question_regex", new(int)},
		{"choices mismatch", []models.UserInputResponseConfig{{Question: "Pick", Choices: &[]string{"a", "b"}, Choice: strPtr("a")}},
			copilot.UserInputRequest{Question: "Pick", Choices: []string{"b", "a"}}, "offered choices do not match", new(int)},
		{"empty expected choices but some offered", []models.UserInputResponseConfig{{Question: "Pick", Choices: &[]string{}, Freeform: strPtr("x")}},
			copilot.UserInputRequest{Question: "Pick", Choices: []string{"a"}}, "offered choices do not match", new(int)},
		{"choice not offered", []models.UserInputResponseConfig{{Question: "Pick", Choice: strPtr("c")}},
			copilot.UserInputRequest{Question: "Pick", Choices: []string{"a", "b"}}, "choice was not offered", new(int)},
		{"freeform disallowed", []models.UserInputResponseConfig{{Question: "Pick", Freeform: strPtr("c")}},
			copilot.UserInputRequest{Question: "Pick", Choices: []string{"a"}, AllowFreeform: boolPtr(false)}, "freeform is not allowed", new(int)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			script := newTestScript(t, tc.responses)
			resp, err := script.Handler()(tc.request, copilot.UserInputInvocation{SessionID: "s-9"})
			require.Error(t, err)
			require.ErrorContains(t, err, tc.reason)
			require.Equal(t, copilot.UserInputResponse{}, resp, "a rejection must not supply a default answer")
			records := script.Records()
			require.Len(t, records, 1)
			require.Equal(t, models.UserInputOutcomeRejected, records[0].Outcome)
			require.Equal(t, "s-9", records[0].SessionID)
			require.Equal(t, tc.index, records[0].ResponseIndex)
			require.Nil(t, records[0].Answer)
			require.Nil(t, records[0].WasFreeform)
			require.Contains(t, records[0].Error, tc.reason)
			require.ErrorContains(t, script.Finish(), tc.reason)
		})
	}
}

func TestUserInputScriptAllowsFreeformWhenUnspecifiedOrTrue(t *testing.T) {
	for _, allow := range []*bool{nil, boolPtr(true)} {
		script := newTestScript(t, []models.UserInputResponseConfig{{Question: "Name?", Freeform: strPtr("")}})
		resp, err := script.Handler()(copilot.UserInputRequest{Question: "Name?", AllowFreeform: allow}, copilot.UserInputInvocation{})
		require.NoError(t, err)
		require.Equal(t, copilot.UserInputResponse{Answer: "", WasFreeform: true}, resp)
		record := script.Records()[0]
		require.Equal(t, allow, record.AllowFreeform)
		require.Nil(t, record.Choices)
	}
}

func TestUserInputScriptPreservesEmptyOfferedChoices(t *testing.T) {
	script := newTestScript(t, []models.UserInputResponseConfig{{Question: "Q", Choices: &[]string{}, Freeform: strPtr("a")}})
	_, err := script.Handler()(copilot.UserInputRequest{Question: "Q", Choices: []string{}}, copilot.UserInputInvocation{})
	require.NoError(t, err)
	record := script.Records()[0]
	require.NotNil(t, record.Choices)
	require.Empty(t, record.Choices)
}

func TestUserInputScriptReportsUnconsumedAnswers(t *testing.T) {
	script := newTestScript(t, []models.UserInputResponseConfig{
		{Question: "A", Freeform: strPtr("1")},
		{Question: "B", Freeform: strPtr("2")},
	})
	require.ErrorContains(t, script.Finish(), "2 scripted answer(s) were not consumed, starting at user_input_responses[0]")
	_, err := script.Handler()(copilot.UserInputRequest{Question: "A"}, copilot.UserInputInvocation{})
	require.NoError(t, err)
	require.ErrorContains(t, script.Finish(), "1 scripted answer(s) were not consumed, starting at user_input_responses[1]")
}

func TestNilUserInputScriptIsInert(t *testing.T) {
	var script *UserInputScript
	require.Nil(t, script.Handler())
	require.Nil(t, script.Records())
	require.NoError(t, script.Finish())
	require.Zero(t, script.rejectionCount())
}

func TestNewUserInputScriptRejectsInvalidConfig(t *testing.T) {
	_, err := NewUserInputScript([]models.UserInputResponseConfig{{Question: "Q"}})
	require.ErrorContains(t, err, "exactly one of choice or freeform")
}

func TestUserInputScriptFallbackAnswersUnscriptedQuestions(t *testing.T) {
	script := newTestScript(t, []models.UserInputResponseConfig{{Question: "Deploy where?", Choice: strPtr("staging")}})
	var asked []string
	script.SetFallback(func(request copilot.UserInputRequest) (copilot.UserInputResponse, error) {
		asked = append(asked, request.Question)
		if request.Question == "Pick one" {
			return copilot.UserInputResponse{Answer: "b"}, nil
		}
		return copilot.UserInputResponse{Answer: "typed", WasFreeform: true}, nil
	})
	handler := script.Handler()

	resp, err := handler(copilot.UserInputRequest{Question: "Your name?"}, copilot.UserInputInvocation{SessionID: "s"})
	require.NoError(t, err)
	require.Equal(t, copilot.UserInputResponse{Answer: "typed", WasFreeform: true}, resp)
	resp, err = handler(copilot.UserInputRequest{Question: "Deploy where?", Choices: []string{"staging", "prod"}}, copilot.UserInputInvocation{SessionID: "s"})
	require.NoError(t, err)
	require.Equal(t, copilot.UserInputResponse{Answer: "staging"}, resp, "a pending scripted entry still answers its matching question")
	resp, err = handler(copilot.UserInputRequest{Question: "Pick one", Choices: []string{"a", "b"}}, copilot.UserInputInvocation{SessionID: "s"})
	require.NoError(t, err)
	require.Equal(t, copilot.UserInputResponse{Answer: "b"}, resp)

	require.Equal(t, []string{"Your name?", "Pick one"}, asked, "matching scripted questions never reach the model")
	records := script.Records()
	require.Equal(t, []string{models.UserInputSourceResponder, models.UserInputSourceScript, models.UserInputSourceResponder},
		[]string{records[0].Source, records[1].Source, records[2].Source})
	require.Nil(t, records[0].ResponseIndex)
	require.Equal(t, 0, *records[1].ResponseIndex)
	require.NoError(t, script.Finish())
}

func TestUserInputScriptFallbackFailsClosed(t *testing.T) {
	cases := []struct {
		name     string
		request  copilot.UserInputRequest
		answer   copilot.UserInputResponse
		err      error
		reason   string
		scripted []models.UserInputResponseConfig
	}{
		{"responder error", copilot.UserInputRequest{Question: "Q"}, copilot.UserInputResponse{}, errors.New("responder abstained: unknown"),
			"responder could not answer: responder abstained: unknown", nil},
		{"choice not offered", copilot.UserInputRequest{Question: "Q", Choices: []string{"a"}}, copilot.UserInputResponse{Answer: "z"}, nil,
			"responder selected a choice that was not offered", nil},
		{"freeform disallowed", copilot.UserInputRequest{Question: "Q", Choices: []string{"a"}, AllowFreeform: boolPtr(false)},
			copilot.UserInputResponse{Answer: "z", WasFreeform: true}, nil, "freeform answer, which the request does not allow", nil},
		{"matched scripted entry is never handed to the model", copilot.UserInputRequest{Question: "Q", Choices: []string{"a"}},
			copilot.UserInputResponse{Answer: "a"}, nil, "user_input_responses[0].choice was not offered",
			[]models.UserInputResponseConfig{{Question: "Q", Choice: strPtr("missing")}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			script := newTestScript(t, tc.scripted)
			calls := 0
			script.SetFallback(func(copilot.UserInputRequest) (copilot.UserInputResponse, error) {
				calls++
				return tc.answer, tc.err
			})
			resp, err := script.Handler()(tc.request, copilot.UserInputInvocation{})
			require.ErrorContains(t, err, tc.reason)
			require.Equal(t, copilot.UserInputResponse{}, resp)
			require.Equal(t, models.UserInputOutcomeRejected, script.Records()[0].Outcome)
			require.Nil(t, script.Records()[0].Answer)
			require.ErrorContains(t, script.Finish(), tc.reason)
			if tc.scripted != nil {
				require.Zero(t, calls)
			}
		})
	}
}

func TestUserInputScriptFallbackKeepsUnconsumedEntriesFailing(t *testing.T) {
	script := newTestScript(t, []models.UserInputResponseConfig{{Question: "Never asked", Freeform: strPtr("x")}})
	script.SetFallback(func(copilot.UserInputRequest) (copilot.UserInputResponse, error) {
		return copilot.UserInputResponse{Answer: "y", WasFreeform: true}, nil
	})
	_, err := script.Handler()(copilot.UserInputRequest{Question: "Other"}, copilot.UserInputInvocation{})
	require.NoError(t, err)
	require.ErrorContains(t, script.Finish(), "1 scripted answer(s) were not consumed")
}

// TestCopilotUserInputCallbackOnCreateAndResume proves the SDK-native
// callback is registered on both the initial and the resumed session, that
// the SDK-invoked handler answers from one shared ordered script, and that a
// rejected request fails only the turn in which it occurred.
func TestCopilotUserInputCallbackOnCreateAndResume(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := newClientMock(ctrl)
	sessionMock := newSessionMock(ctrl)

	var createCfg *copilot.SessionConfig
	var resumeCfg *copilot.ResumeSessionConfig
	clientMock.EXPECT().CreateSession(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, cfg *copilot.SessionConfig) (CopilotSession, error) {
			createCfg = cfg
			return sessionMock, nil
		})
	clientMock.EXPECT().ResumeSessionWithOptions(gomock.Any(), "session-q", gomock.Any()).Times(2).DoAndReturn(
		func(_ context.Context, _ string, cfg *copilot.ResumeSessionConfig) (CopilotSession, error) {
			resumeCfg = cfg
			return sessionMock, nil
		})
	sessionMock.EXPECT().Disconnect().AnyTimes()
	clientMock.EXPECT().DeleteSession(gomock.Any(), "session-q").AnyTimes()
	sessionMock.EXPECT().On(gomock.Any()).AnyTimes().Return(func() {})
	sessionMock.EXPECT().SessionID().AnyTimes().Return("session-q")

	turn := 0
	sessionMock.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Times(3).DoAndReturn(
		func(context.Context, copilot.MessageOptions) (*copilot.SessionEvent, error) {
			turn++
			var (
				resp copilot.UserInputResponse
				err  error
			)
			switch turn {
			case 1:
				resp, err = createCfg.OnUserInputRequest(copilot.UserInputRequest{Question: "Deploy where?", Choices: []string{"staging", "prod"}},
					copilot.UserInputInvocation{SessionID: "session-q"})
				require.NoError(t, err)
				require.Equal(t, copilot.UserInputResponse{Answer: "staging"}, resp)
			case 2:
				resp, err = resumeCfg.OnUserInputRequest(copilot.UserInputRequest{Question: "Release note?"},
					copilot.UserInputInvocation{SessionID: "session-q"})
				require.NoError(t, err)
				require.Equal(t, copilot.UserInputResponse{Answer: "fixes login", WasFreeform: true}, resp)
			case 3:
				_, err = resumeCfg.OnUserInputRequest(copilot.UserInputRequest{Question: "Anything else?"},
					copilot.UserInputInvocation{SessionID: "session-q"})
				require.Error(t, err)
			}
			return &copilot.SessionEvent{}, nil
		})

	engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(*copilot.ClientOptions) CopilotClient { return clientMock },
	}).Build()
	defer func() { require.NoError(t, engine.Shutdown(context.Background())) }()
	require.NoError(t, engine.Initialize(context.Background()))

	script := newTestScript(t, []models.UserInputResponseConfig{
		{Question: "Deploy where?", Choices: &[]string{"staging", "prod"}, Choice: strPtr("staging")},
		{Question: "Release note?", Freeform: strPtr("fixes login")},
	})

	first, err := engine.Execute(context.Background(), &ExecutionRequest{Message: "start", UserInput: script})
	require.NoError(t, err)
	require.True(t, first.Success, first.ErrorMsg)
	require.NotNil(t, createCfg.OnUserInputRequest)
	require.Len(t, first.UserInputRequests, 1)

	second, err := engine.Execute(context.Background(), &ExecutionRequest{
		Message: "continue", SessionID: "session-q", WorkspaceDir: first.WorkspaceDir, UserInput: script,
	})
	require.NoError(t, err)
	require.True(t, second.Success, second.ErrorMsg)
	require.NotNil(t, resumeCfg.OnUserInputRequest)
	require.Len(t, second.UserInputRequests, 2)
	require.Equal(t, 1, second.UserInputRequests[0].Sequence)
	require.Equal(t, 2, second.UserInputRequests[1].Sequence)
	require.True(t, *second.UserInputRequests[1].WasFreeform)
	require.NoError(t, script.Finish())

	third, err := engine.Execute(context.Background(), &ExecutionRequest{
		Message: "again", SessionID: "session-q", WorkspaceDir: first.WorkspaceDir, UserInput: script,
	})
	require.NoError(t, err)
	require.False(t, third.Success)
	require.Contains(t, third.ErrorMsg, "native user input request 3 rejected: no scripted user_input_responses entry remains")
	require.Len(t, third.UserInputRequests, 3)
	require.Equal(t, models.UserInputOutcomeRejected, third.UserInputRequests[2].Outcome)
}

func TestCopilotLeavesUserInputCallbackUnsetWithoutScript(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := newClientMock(ctrl)
	sessionMock := newSessionMock(ctrl)

	createCalled, resumeCalled := false, false
	clientMock.EXPECT().CreateSession(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, cfg *copilot.SessionConfig) (CopilotSession, error) {
			createCalled = true
			require.Nil(t, cfg.OnUserInputRequest, "absent user_input_responses must not enable the SDK ask_user tool")
			return sessionMock, nil
		})
	clientMock.EXPECT().ResumeSessionWithOptions(gomock.Any(), "session-n", gomock.Any()).DoAndReturn(
		func(_ context.Context, _ string, cfg *copilot.ResumeSessionConfig) (CopilotSession, error) {
			resumeCalled = true
			require.Nil(t, cfg.OnUserInputRequest)
			return sessionMock, nil
		})
	sessionMock.EXPECT().Disconnect().AnyTimes()
	clientMock.EXPECT().DeleteSession(gomock.Any(), "session-n").AnyTimes()
	sessionMock.EXPECT().On(gomock.Any()).AnyTimes().Return(func() {})
	sessionMock.EXPECT().SessionID().AnyTimes().Return("session-n")
	sessionMock.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Times(2).Return(&copilot.SessionEvent{}, nil)

	engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(*copilot.ClientOptions) CopilotClient { return clientMock },
	}).Build()
	defer func() { require.NoError(t, engine.Shutdown(context.Background())) }()
	require.NoError(t, engine.Initialize(context.Background()))

	first, err := engine.Execute(context.Background(), &ExecutionRequest{Message: "start"})
	require.NoError(t, err)
	require.Nil(t, first.UserInputRequests)
	_, err = engine.Execute(context.Background(), &ExecutionRequest{Message: "next", SessionID: "session-n", WorkspaceDir: first.WorkspaceDir})
	require.NoError(t, err)
	require.True(t, createCalled)
	require.True(t, resumeCalled)
}
