package execution

import (
	"context"
	"errors"
	"testing"
	"time"

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

func TestUserInputScriptAnswersInOrder(t *testing.T) {
	script := newTestScript(t, []models.UserInputResponseConfig{
		{Question: "Which region\\?", Answer: strPtr("west")},
		{Answer: strPtr("demo-app")}, // no question: answers whatever is asked next
	})
	handler := script.Handler()
	require.NotNil(t, handler)

	resp, err := handler(copilot.UserInputRequest{Question: "Which region?", Choices: []string{"east", "west"}, AllowFreeform: boolPtr(false)},
		copilot.UserInputInvocation{SessionID: "s-1"})
	require.NoError(t, err)
	require.Equal(t, copilot.UserInputResponse{Answer: "west"}, resp, "an answer equal to an offered choice selects it")

	resp, err = handler(copilot.UserInputRequest{Question: "What name should I use?"}, copilot.UserInputInvocation{SessionID: "s-1"})
	require.NoError(t, err)
	require.Equal(t, copilot.UserInputResponse{Answer: "demo-app", WasFreeform: true}, resp)

	require.NoError(t, script.Finish())
	require.Equal(t, []models.UserInputRecord{
		{Sequence: 1, SessionID: "s-1", Question: "Which region?", Choices: []string{"east", "west"},
			Answer: strPtr("west"), WasFreeform: boolPtr(false), Source: models.UserInputSourceScript},
		{Sequence: 2, SessionID: "s-1", Question: "What name should I use?",
			Answer: strPtr("demo-app"), WasFreeform: boolPtr(true), Source: models.UserInputSourceScript},
	}, script.Records())
}

func TestUserInputScriptFailsClosed(t *testing.T) {
	cases := []struct {
		name      string
		responses []models.UserInputResponseConfig
		request   copilot.UserInputRequest
		reason    string
		source    string
	}{
		{"empty script", []models.UserInputResponseConfig{}, copilot.UserInputRequest{Question: "Continue?"},
			"no scripted user_input_responses entry remains", ""},
		{"question mismatch", []models.UserInputResponseConfig{{Question: "Continue\\?", Answer: strPtr("yes")}},
			copilot.UserInputRequest{Question: "Continue now?"}, "question does not match user_input_responses[0].question", ""},
		{"question is anchored", []models.UserInputResponseConfig{{Question: "Continue", Answer: strPtr("yes")}},
			copilot.UserInputRequest{Question: "Continue?"}, "question does not match", ""},
		{"typed answer not allowed", []models.UserInputResponseConfig{{Answer: strPtr("c")}},
			copilot.UserInputRequest{Question: "Pick", Choices: []string{"a", "b"}, AllowFreeform: boolPtr(false)},
			`answer "c" is not an offered choice and the request does not allow typed answers`, models.UserInputSourceScript},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			script := newTestScript(t, tc.responses)
			resp, err := script.Handler()(tc.request, copilot.UserInputInvocation{SessionID: "s-9"})
			require.ErrorContains(t, err, tc.reason)
			require.Equal(t, copilot.UserInputResponse{}, resp, "a rejection must not supply a default answer")
			records := script.Records()
			require.Len(t, records, 1)
			require.Equal(t, "s-9", records[0].SessionID)
			require.Equal(t, tc.source, records[0].Source)
			require.Nil(t, records[0].Answer)
			require.Nil(t, records[0].WasFreeform)
			require.Contains(t, records[0].Error, tc.reason)
			require.ErrorContains(t, script.Finish(), tc.reason)
		})
	}
}

func TestUserInputScriptTypedAnswerAllowedWhenUnspecifiedOrTrue(t *testing.T) {
	for _, allow := range []*bool{nil, boolPtr(true)} {
		script := newTestScript(t, []models.UserInputResponseConfig{{Answer: strPtr("")}})
		resp, err := script.Handler()(copilot.UserInputRequest{Question: "Name?", Choices: []string{"x"}, AllowFreeform: allow}, copilot.UserInputInvocation{})
		require.NoError(t, err)
		require.Equal(t, copilot.UserInputResponse{Answer: "", WasFreeform: true}, resp)
	}
}

func TestUserInputScriptPreservesOfferedChoicesPresence(t *testing.T) {
	script := newTestScript(t, []models.UserInputResponseConfig{{Answer: strPtr("a")}, {Answer: strPtr("b")}})
	_, err := script.Handler()(copilot.UserInputRequest{Question: "Q", Choices: []string{}}, copilot.UserInputInvocation{})
	require.NoError(t, err)
	_, err = script.Handler()(copilot.UserInputRequest{Question: "Q"}, copilot.UserInputInvocation{})
	require.NoError(t, err)
	records := script.Records()
	require.NotNil(t, records[0].Choices)
	require.Empty(t, records[0].Choices)
	require.Nil(t, records[1].Choices)
}

func TestUserInputScriptReportsUnusedAnswers(t *testing.T) {
	script := newTestScript(t, []models.UserInputResponseConfig{{Answer: strPtr("1")}, {Answer: strPtr("2")}})
	require.ErrorContains(t, script.Finish(), "2 scripted answer(s) were not used, starting at user_input_responses[0]")
	_, err := script.Handler()(copilot.UserInputRequest{Question: "A"}, copilot.UserInputInvocation{})
	require.NoError(t, err)
	require.ErrorContains(t, script.Finish(), "1 scripted answer(s) were not used, starting at user_input_responses[1]")
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
	require.ErrorContains(t, err, "answer is required")
}

func TestUserInputScriptFallbackAnswersUnscriptedQuestions(t *testing.T) {
	script := newTestScript(t, []models.UserInputResponseConfig{{Question: "Deploy where\\?", Answer: strPtr("staging")}})
	var asked []string
	script.SetFallback(func(_ context.Context, request copilot.UserInputRequest) (string, error) {
		asked = append(asked, request.Question)
		if request.Question == "Pick one" {
			return "b", nil
		}
		return "typed", nil
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
	require.NoError(t, script.Finish())
}

func TestUserInputScriptFallbackFailsClosed(t *testing.T) {
	cases := []struct {
		name     string
		request  copilot.UserInputRequest
		answer   string
		err      error
		reason   string
		scripted []models.UserInputResponseConfig
	}{
		{"responder error", copilot.UserInputRequest{Question: "Q"}, "", errors.New("responder abstained: unknown"),
			"responder could not answer: responder abstained: unknown", nil},
		{"typed answer not allowed", copilot.UserInputRequest{Question: "Q", Choices: []string{"a"}, AllowFreeform: boolPtr(false)},
			"z", nil, "is not an offered choice", nil},
		{"matched scripted entry is never handed to the model", copilot.UserInputRequest{Question: "Q", Choices: []string{"a"}, AllowFreeform: boolPtr(false)},
			"a", nil, `answer "missing" is not an offered choice`, []models.UserInputResponseConfig{{Answer: strPtr("missing")}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			script := newTestScript(t, tc.scripted)
			calls := 0
			script.SetFallback(func(context.Context, copilot.UserInputRequest) (string, error) {
				calls++
				return tc.answer, tc.err
			})
			resp, err := script.Handler()(tc.request, copilot.UserInputInvocation{})
			require.ErrorContains(t, err, tc.reason)
			require.Equal(t, copilot.UserInputResponse{}, resp)
			require.Nil(t, script.Records()[0].Answer)
			require.ErrorContains(t, script.Finish(), tc.reason)
			if tc.scripted != nil {
				require.Zero(t, calls)
			}
		})
	}
}

func TestUserInputScriptFallbackKeepsUnusedEntriesFailing(t *testing.T) {
	script := newTestScript(t, []models.UserInputResponseConfig{{Question: "Never asked", Answer: strPtr("x")}})
	script.SetFallback(func(context.Context, copilot.UserInputRequest) (string, error) { return "y", nil })
	_, err := script.Handler()(copilot.UserInputRequest{Question: "Other"}, copilot.UserInputInvocation{})
	require.NoError(t, err)
	require.ErrorContains(t, script.Finish(), "1 scripted answer(s) were not used")
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
		{Question: `Deploy where\?`, Answer: strPtr("staging")},
		{Answer: strPtr("fixes login")},
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
	require.NotEmpty(t, third.UserInputRequests[2].Error)
	require.Nil(t, third.UserInputRequests[2].Answer)
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

// TestUserInputScriptFallbackDoesNotHoldLockAndUsesTurnContext proves the
// responder runs with the bound agent-turn context and without the script
// lock, so recording can continue while the responder waits.
func TestUserInputScriptFallbackDoesNotHoldLockAndUsesTurnContext(t *testing.T) {
	script := newTestScript(t, nil)
	started := make(chan struct{})
	var gotCtx context.Context
	script.SetFallback(func(ctx context.Context, _ copilot.UserInputRequest) (string, error) {
		gotCtx = ctx
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	})
	turnCtx, cancelTurn := context.WithCancel(context.Background())
	release := script.bindTurn(turnCtx)
	defer release()

	done := make(chan error, 1)
	go func() {
		_, err := script.Handler()(copilot.UserInputRequest{Question: "Name?"}, copilot.UserInputInvocation{SessionID: "s"})
		done <- err
	}()
	<-started

	records := make(chan []models.UserInputRecord, 1)
	go func() { records <- script.Records() }()
	select {
	case pending := <-records:
		require.Len(t, pending, 1)
		require.Nil(t, pending[0].Answer)
		require.Empty(t, pending[0].Error, "a pending request has neither answer nor error")
	case <-time.After(5 * time.Second):
		t.Fatal("Records blocked while the responder was answering")
	}

	cancelTurn()
	select {
	case err := <-done:
		require.ErrorContains(t, err, context.Canceled.Error())
	case <-time.After(5 * time.Second):
		t.Fatal("ending the agent turn did not cancel the responder call")
	}
	require.Equal(t, turnCtx, gotCtx)
	require.Contains(t, script.Records()[0].Error, "responder could not answer")
}

func TestUserInputScriptFallbackWithoutBoundTurnUsesBackground(t *testing.T) {
	script := newTestScript(t, nil)
	script.SetFallback(func(ctx context.Context, _ copilot.UserInputRequest) (string, error) {
		require.NoError(t, ctx.Err())
		return "ok", nil
	})
	release := script.bindTurn(context.Background())
	release()
	_, err := script.Handler()(copilot.UserInputRequest{Question: "Q"}, copilot.UserInputInvocation{})
	require.NoError(t, err)
}

// TestCopilotEngineBindsTurnContextForUserInput checks the production engine
// passes its turn context, including its deadline, to the responder.
func TestCopilotEngineBindsTurnContextForUserInput(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := newClientMock(ctrl)
	sessionMock := newSessionMock(ctrl)
	var cfg *copilot.SessionConfig
	clientMock.EXPECT().CreateSession(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, c *copilot.SessionConfig) (CopilotSession, error) {
			cfg = c
			return sessionMock, nil
		})
	sessionMock.EXPECT().Disconnect().AnyTimes()
	clientMock.EXPECT().DeleteSession(gomock.Any(), gomock.Any()).AnyTimes()
	sessionMock.EXPECT().On(gomock.Any()).AnyTimes().Return(func() {})
	sessionMock.EXPECT().SessionID().AnyTimes().Return("session-t")
	sessionMock.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(
		func(context.Context, copilot.MessageOptions) (*copilot.SessionEvent, error) {
			_, err := cfg.OnUserInputRequest(copilot.UserInputRequest{Question: "Q"}, copilot.UserInputInvocation{SessionID: "session-t"})
			require.NoError(t, err)
			return &copilot.SessionEvent{}, nil
		})
	engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(*copilot.ClientOptions) CopilotClient { return clientMock },
	}).Build()
	defer func() { require.NoError(t, engine.Shutdown(context.Background())) }()
	require.NoError(t, engine.Initialize(context.Background()))

	deadline := time.Now().Add(time.Hour).Truncate(time.Second)
	turnCtx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	var gotDeadline time.Time
	script := newTestScript(t, nil)
	script.SetFallback(func(ctx context.Context, _ copilot.UserInputRequest) (string, error) {
		gotDeadline, _ = ctx.Deadline()
		return "a", nil
	})
	resp, err := engine.Execute(turnCtx, &ExecutionRequest{Message: "go", UserInput: script})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.ErrorMsg)
	require.Equal(t, deadline, gotDeadline)
}
