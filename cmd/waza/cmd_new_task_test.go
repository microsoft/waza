package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/azure/azure-dev/cli/azd/pkg/ux"
	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
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

	client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, cfg *copilot.SessionConfig) (execution.CopilotSession, error) {
		assert.Contains(t, cfg.SkillDirectories, skillDir)
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

func TestNewTaskFromPromptCommand_EndToEndCreatesTaskFile(t *testing.T) {
	ctrl := gomock.NewController(t)
	client := newClientMock(ctrl)
	session := NewMockCopilotSession(ctrl)

	sessionID := "session-end-to-end"
	home := t.TempDir()
	tempCopilotDir := filepath.Join(home, ".copilot", "session-state")

	fixturePath := filepath.Join("..", "..", "internal", "testdata", "copilot_events_using_skill.json")
	fixtureBytes, err := os.ReadFile(fixturePath)
	require.NoError(t, err)

	logPath := filepath.Join(tempCopilotDir, sessionID, "events.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(logPath), 0o755))
	require.NoError(t, os.WriteFile(logPath, fixtureBytes, 0o644))

	client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(session, nil)

	session.EXPECT().On(gomock.Any()).Return(func() {}).Times(3)
	session.EXPECT().SessionID().Return(sessionID)
	session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(nil, nil)
	session.EXPECT().Disconnect().Return(nil)

	client.EXPECT().DeleteSession(gomock.Any(), sessionID).Return(nil)

	outputPath := filepath.Join(t.TempDir(), "nested", "generated-task.yaml")

	cmd := newTaskFromPromptCmd(&newTaskFromPromptCmdOptions{
		NewTaskList:      func(*ux.TaskListOptions) taskList { return &fakeTaskList{runAll: true} },
		NewCopilotClient: func(*copilot.ClientOptions) execution.CopilotClient { return client },
		CopilotLogDir:    func() (string, error) { return tempCopilotDir, nil },
	})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--testname", "auto-generated", "--tags", "auto-generated", "use the example horn", outputPath})

	require.NoError(t, cmd.Execute())

	actual, err := models.LoadTestCase(outputPath)
	require.NoError(t, err)

	expected := &models.TestCase{
		Path:        outputPath,
		DisplayName: "auto-generated",
		TestID:      "auto-generated",
		Tags:        []string{"auto-generated"},
		Stimulus: models.TestStimulus{
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
					ContainsCS: []string{"yesyes"},
				},
			},
		},
	}

	require.Equal(t, expected, actual)
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
