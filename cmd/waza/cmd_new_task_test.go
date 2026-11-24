package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/azure/azure-dev/cli/azd/pkg/ux"
	copilot "github.com/microsoft/copilot-sdk/go/v2"
	"github.com/microsoft/waza/internal/copilotevents"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/utils"
	"github.com/microsoft/waza/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type fakeTaskList struct {
	tasks  []ux.TaskOptions
	runErr error
	runAll bool
}

func (f *fakeTaskList) AddTask(options ux.TaskOptions) taskList {
	f.tasks = append(f.tasks, options)
	return f
}

func (f *fakeTaskList) Run() error {
	if f.runErr != nil {
		return f.runErr
	}
	if f.runAll {
		for _, task := range f.tasks {
			_, err := task.Action(func(string) {})
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func (f *fakeTaskList) titles() []string {
	titles := make([]string, 0, len(f.tasks))

	for _, task := range f.tasks {
		titles = append(titles, task.Title)
	}

	return titles
}

func TestNewTaskCommand_HasFromPromptSubcommand(t *testing.T) {
	cmd := newNewTaskCommand()

	found := false
	for _, c := range cmd.Commands() {
		if c.Name() == "from-prompt" {
			found = true
			break
		}
	}

	assert.True(t, found, "new task command should include the from-prompt subcommand")
}

func TestNewTaskFromPromptCommand_RequiresTwoArgs(t *testing.T) {
	cmd := newTaskFromPromptCmd(nil)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"only-prompt"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 2 arg(s)")
}

func TestNewTaskFromPromptCommand_ExistingFileNeedsOverwrite(t *testing.T) {
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "existing-task.yaml")
	require.NoError(t, os.WriteFile(taskPath, []byte("id: existing\n"), 0o644))

	cmd := newTaskFromPromptCmd(&newTaskFromPromptCmdOptions{
		NewTaskList: func(options *ux.TaskListOptions) taskList {
			return &fakeTaskList{
				runAll: true,
			}
		},
	})

	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"my prompt", taskPath})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
	assert.Contains(t, err.Error(), "--overwrite")
}

func TestNewTaskFromPromptCommand_TaskListRegistersExpectedTasks(t *testing.T) {
	fakeTaskList := &fakeTaskList{}

	cmd := newTaskFromPromptCmd(&newTaskFromPromptCmdOptions{
		NewTaskList: func(options *ux.TaskListOptions) taskList {
			return fakeTaskList
		},
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"collect telemetry", filepath.Join(t.TempDir(), "generated-task.yaml")})

	err := cmd.Execute()
	require.NoError(t, err)
	require.NotNil(t, fakeTaskList)

	assert.Equal(t, []string{
		"Getting evaluation tasks folder",
		"Starting copilot",
		"Discovering skills",
		"Execute prompt",
		"Creating eval task",
	}, fakeTaskList.titles())
}

func TestNewTaskFromPromptCommand_TaskListRunErrorReturned(t *testing.T) {
	expectedErr := errors.New("task list run failed")

	fakeTaskList := &fakeTaskList{
		runErr: expectedErr,
	}

	cmd := newTaskFromPromptCmd(&newTaskFromPromptCmdOptions{
		NewTaskList: func(options *ux.TaskListOptions) taskList {
			return fakeTaskList
		},
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"my prompt", filepath.Join(t.TempDir(), "generated-task.yaml")})

	err := cmd.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, expectedErr)
}

func TestNewTaskFromPromptCommand_CopilotInitErrorReturned(t *testing.T) {
	ctrl := gomock.NewController(t)
	client := NewMockCopilotClient(ctrl)

	client.EXPECT().Start(gomock.Any()).Return(errors.New("engine failed to initialize"))
	client.EXPECT().Stop().Times(1).Return(nil)

	cmd := newTaskFromPromptCmd(&newTaskFromPromptCmdOptions{
		NewTaskList: func(*ux.TaskListOptions) taskList { return &fakeTaskList{runAll: true} },
		NewCopilotClient: func(*copilot.ClientOptions) execution.CopilotClient {
			return client
		},
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"my prompt", filepath.Join(t.TempDir(), "generated-task.yaml")})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to initialize copilot SDK")
	assert.Contains(t, err.Error(), "engine failed to initialize")
}

func TestNewTaskFromPromptCommand_DiscoverErrorReturned(t *testing.T) {
	ctrl := gomock.NewController(t)
	client := newClientMock(ctrl)

	rootDir := t.TempDir()
	calledRoot := ""

	cmd := newTaskFromPromptCmd(&newTaskFromPromptCmdOptions{
		NewTaskList: func(*ux.TaskListOptions) taskList { return &fakeTaskList{runAll: true} },
		NewCopilotClient: func(*copilot.ClientOptions) execution.CopilotClient {
			return client
		},
		DetectContext: func(dir string, opts ...workspace.DetectOption) (*workspace.WorkspaceContext, error) {
			calledRoot = dir
			return nil, errors.New("discover failed")
		},
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--root", rootDir, "collect telemetry", filepath.Join(t.TempDir(), "generated-task.yaml")})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Equal(t, rootDir, calledRoot)
	assert.Contains(t, err.Error(), "discover failed")
}

func TestNewTaskFromPromptCommand_DiscoveredSkillsPassedToCopilotSession(t *testing.T) {
	ctrl := gomock.NewController(t)
	client := newClientMock(ctrl)

	skillDir := t.TempDir()

	client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, cfg *copilot.CreateSessionOptions) (execution.CopilotSession, error) {
		assert.Contains(t, cfg.Skills.Directories, skillDir)
		return nil, errors.New("create failed")
	})

	cmd := newTaskFromPromptCmd(&newTaskFromPromptCmdOptions{
		NewTaskList: func(*ux.TaskListOptions) taskList { return &fakeTaskList{runAll: true} },
		NewCopilotClient: func(*copilot.ClientOptions) execution.CopilotClient {
			return client
		},
		DetectContext: func(dir string, opts ...workspace.DetectOption) (*workspace.WorkspaceContext, error) {
			return &workspace.WorkspaceContext{
				Skills: []workspace.SkillInfo{{Dir: skillDir}},
			}, nil
		},
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"collect telemetry", filepath.Join(t.TempDir(), "generated-task.yaml")})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create session")
}

func TestNewTaskFromPromptCommand_EndToEndCreatesTaskFileWithoutSessionLog(t *testing.T) {
	ctrl := gomock.NewController(t)
	client := newClientMock(ctrl)
	session := newSessionMock(ctrl)

	sessionID := "session-end-to-end"
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	tempCopilotDir := filepath.Join(home, ".copilot", "session-state")

	fixturePath := filepath.Join("..", "..", "internal", "testdata", "copilot_events_using_skill.json")
	logPath := filepath.Join(tempCopilotDir, sessionID, "events.jsonl")
	require.NoFileExists(t, logPath)

	client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(session, nil)

	var handlers []func(copilot.SessionEvent)
	session.EXPECT().On(gomock.Any()).DoAndReturn(func(handler func(copilot.SessionEvent)) func() {
		handlers = append(handlers, handler)
		return func() {}
	}).Times(3)
	session.EXPECT().SessionID().Return(sessionID)
	session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, _ copilot.MessageOptions) (*copilot.SessionEvent, error) {
		for event, err := range utils.NewCopilotLogIterator(fixturePath) {
			require.NoError(t, err)
			for _, handler := range handlers {
				handler(event)
			}
		}
		return nil, nil
	})
	session.EXPECT().Disconnect().Return(nil)

	client.EXPECT().DeleteSession(gomock.Any(), sessionID).Return(nil)

	outputPath := filepath.Join(t.TempDir(), "nested", "generated-task.yaml")

	cmd := newTaskFromPromptCmd(&newTaskFromPromptCmdOptions{
		NewTaskList:      func(*ux.TaskListOptions) taskList { return &fakeTaskList{runAll: true} },
		NewCopilotClient: func(*copilot.ClientOptions) execution.CopilotClient { return client },
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--testname", "auto-generated", "--tags", "auto-generated", "use the example horn", outputPath})

	require.NoError(t, cmd.Execute())
	require.NoFileExists(t, logPath)

	actual, err := models.LoadTestCase(outputPath)
	require.NoError(t, err)

	expected := &models.TestCase{
		DisplayName: "auto-generated",
		TestID:      "auto-generated",
		Tags:        []string{"auto-generated"},
		Stimulus: models.TaskStimulus{
			Message: "use the example horn",
		},
		Validators: []models.ValidatorInline{
			{
				Identifier: "skills-check",
				Kind:       models.GraderKindSkillInvocation,
				Parameters: models.SkillInvocationGraderParameters{
					RequiredSkills: []string{"example"},
					Mode:           models.SkillMatchingModeAnyOrder,
				},
			},
			{
				Identifier: "tools-check",
				Kind:       models.GraderKindToolConstraint,
				Parameters: models.ToolConstraintGraderParameters{
					ExpectTools: []models.ToolSpecParameters{{
						Tool:         "skill",
						SkillPattern: "example",
					}},
				},
			},
			{
				Identifier: "check-response",
				Kind:       models.GraderKindText,
				Parameters: models.TextGraderParameters{
					ContainsCS: []string{"yes"},
				},
			},
		},
	}

	expectedJSON, err := json.Marshal(expected)
	require.NoError(t, err)
	actualJSON, err := json.Marshal(actual)
	require.NoError(t, err)
	require.JSONEq(t, string(expectedJSON), string(actualJSON))
	require.Equal(t, expected.Validators, actual.Validators)
	require.NotEmpty(t, actual.SourceBytes())

	responseCheck := actual.Validators[2]
	params, ok := responseCheck.Parameters.(models.TextGraderParameters)
	require.True(t, ok)
	responseGrader, err := graders.Create(responseCheck.Identifier, params)
	require.NoError(t, err)
	result, err := responseGrader.Grade(context.Background(), &graders.Context{Output: "yes"})
	require.NoError(t, err)
	require.True(t, result.Passed)
}

func TestNewTaskFromPromptCommand_TextOnlyWithoutUserMessage(t *testing.T) {
	ctrl := gomock.NewController(t)
	client := newClientMock(ctrl)
	session := newSessionMock(ctrl)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(session, nil)
	var handlers []func(copilot.SessionEvent)
	session.EXPECT().On(gomock.Any()).DoAndReturn(func(handler func(copilot.SessionEvent)) func() {
		handlers = append(handlers, handler)
		return func() {}
	}).Times(3)
	session.EXPECT().SessionID().Return("text-only-session")
	session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, options copilot.MessageOptions) (*copilot.SessionEvent, error) {
		require.Equal(t, "Respond exactly HELLO_SYNTHETIC. Do not use tools.", options.Prompt)
		var event copilot.SessionEvent
		require.NoError(t, json.Unmarshal([]byte(`{"type":"assistant.message","data":{"content":"HELLO_SYNTHETIC"}}`), &event))
		for _, handler := range handlers {
			handler(event)
		}
		return &event, nil
	})
	session.EXPECT().Disconnect().Return(nil)
	client.EXPECT().DeleteSession(gomock.Any(), "text-only-session").Return(nil)

	outputPath := filepath.Join(t.TempDir(), "generated-task.yaml")
	cmd := newTaskFromPromptCmd(&newTaskFromPromptCmdOptions{
		NewTaskList:      func(*ux.TaskListOptions) taskList { return &fakeTaskList{runAll: true} },
		NewCopilotClient: func(*copilot.ClientOptions) execution.CopilotClient { return client },
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"Respond exactly HELLO_SYNTHETIC. Do not use tools.", outputPath})
	require.NoError(t, cmd.Execute())
	actual, err := models.LoadTestCase(outputPath)
	require.NoError(t, err)
	require.Equal(t, "Respond exactly HELLO_SYNTHETIC. Do not use tools.", actual.Stimulus.Message)
	require.Equal(t, []models.ValidatorInline{{
		Identifier: "check-response",
		Kind:       models.GraderKindText,
		Parameters: models.TextGraderParameters{ContainsCS: []string{"HELLO_SYNTHETIC"}},
	}}, actual.Validators)
	require.NoDirExists(t, filepath.Join(home, ".copilot", "session-state"))
}

func TestFromPromptSessionEvents_RejectsInvalidResponse(t *testing.T) {
	for _, tt := range []struct {
		name     string
		response *execution.ExecutionResponse
		wantErr  string
	}{
		{name: "nil response", wantErr: "prompt execution returned no response"},
		{name: "unsuccessful without message", response: &execution.ExecutionResponse{}, wantErr: "prompt execution failed"},
		{
			name: "unsuccessful with partial events",
			response: &execution.ExecutionResponse{
				ErrorMsg: "trial timed out",
				Events:   copilotevents.FromSDK([]copilot.SessionEvent{{}}),
			},
			wantErr: "prompt execution failed: trial timed out",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			events, err := fromPromptSessionEvents(tt.response)
			require.EqualError(t, err, tt.wantErr)
			require.Nil(t, events)
		})
	}
}

func TestNewTaskFromPromptCommand_FailedExecutionDoesNotCreateTask(t *testing.T) {
	ctrl := gomock.NewController(t)
	client := newClientMock(ctrl)
	session := newSessionMock(ctrl)
	client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(session, nil)
	session.EXPECT().On(gomock.Any()).Return(func() {}).Times(3)
	session.EXPECT().SessionID().Return("failed-session")
	session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(nil, errors.New("model request failed"))
	session.EXPECT().Disconnect().Return(nil)
	client.EXPECT().DeleteSession(gomock.Any(), "failed-session").Return(nil)

	outputPath := filepath.Join(t.TempDir(), "generated-task.yaml")
	cmd := newTaskFromPromptCmd(&newTaskFromPromptCmdOptions{
		NewTaskList:      func(*ux.TaskListOptions) taskList { return &fakeTaskList{runAll: true} },
		NewCopilotClient: func(*copilot.ClientOptions) execution.CopilotClient { return client },
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"my prompt", outputPath})
	require.EqualError(t, cmd.Execute(), "prompt execution failed: model request failed")
	require.NoFileExists(t, outputPath)
}

func TestNewTaskFromPromptCommand_MissingAssistantResponseDoesNotCreateTask(t *testing.T) {
	ctrl := gomock.NewController(t)
	client := newClientMock(ctrl)
	session := newSessionMock(ctrl)
	client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(session, nil)
	session.EXPECT().On(gomock.Any()).Return(func() {}).Times(3)
	session.EXPECT().SessionID().Return("missing-response-session")
	session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(nil, nil)
	session.EXPECT().Disconnect().Return(nil)
	client.EXPECT().DeleteSession(gomock.Any(), "missing-response-session").Return(nil)

	outputPath := filepath.Join(t.TempDir(), "generated-task.yaml")
	cmd := newTaskFromPromptCmd(&newTaskFromPromptCmdOptions{
		NewTaskList:      func(*ux.TaskListOptions) taskList { return &fakeTaskList{runAll: true} },
		NewCopilotClient: func(*copilot.ClientOptions) execution.CopilotClient { return client },
		DetectContext: func(string, ...workspace.DetectOption) (*workspace.WorkspaceContext, error) {
			return &workspace.WorkspaceContext{}, nil
		},
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"my prompt", outputPath})

	require.EqualError(t, cmd.Execute(), "no assistant response was captured; refusing to generate an always-passing response check")
	require.NoFileExists(t, outputPath)
}

func newClientMock(ctrl *gomock.Controller) *MockCopilotClient {
	clientMock := NewMockCopilotClient(ctrl)

	// This is the basic sequence of calls that occurs anytime a copilot engine is initialized
	clientMock.EXPECT().Start(gomock.Any()).Times(1)
	clientMock.EXPECT().Stop().Times(1)
	clientMock.EXPECT().GetAuthStatus(gomock.Any()).Return(&copilot.GetAuthStatusResponse{
		IsAuthenticated: true,
	}, nil).Times(1)

	return clientMock
}
