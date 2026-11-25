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
	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/copilotevents"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/projectconfig"
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

func TestNewTaskFromPromptCommand_RootUsesRealWorkspaceDiscovery(t *testing.T) {
	for _, configured := range []bool{false, true} {
		name := "defaults-relative-to-root"
		if configured {
			name = "paths-relative-to-config"
		}
		t.Run(name, func(t *testing.T) {
			callerDir := t.TempDir()
			rootDir := t.TempDir()
			writeWorkspace := func(dir, skills, evals, skill, evalFile string) string {
				skillDir := filepath.Join(dir, skills, skill)
				evalDir := filepath.Join(dir, evals, skill)
				require.NoError(t, os.MkdirAll(skillDir, 0o755))
				require.NoError(t, os.MkdirAll(evalDir, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
					[]byte("---\nname: "+skill+"\ndescription: Test skill\n---\n# Body\n"), 0o644))
				require.NoError(t, os.WriteFile(filepath.Join(evalDir, evalFile), []byte("name: test\n"), 0o644))
				return skillDir
			}
			decoyDir := writeWorkspace(callerDir, "skills", "evals", "decoy", "eval.yaml")
			wantSkillDir := writeWorkspace(rootDir, "skills", "evals", "selected", "eval.yaml")
			wantEvalPath := filepath.Join(rootDir, "evals", "selected", "eval.yaml")
			if configured {
				wantSkillDir = writeWorkspace(callerDir, "custom-skills", "custom-evals", "selected", "suite.yml")
				wantEvalPath = filepath.Join(callerDir, "custom-evals", "selected", "suite.yml")
				require.NoError(t, os.WriteFile(filepath.Join(callerDir, ".waza.yaml"),
					[]byte("paths:\n  skills: custom-skills\n  evals: custom-evals\nfiles:\n  evalFile: suite.yml\n"), 0o644))
				callerDir = filepath.Join(callerDir, "nested")
				require.NoError(t, os.MkdirAll(callerDir, 0o755))
			}
			t.Chdir(callerDir)

			cfg, err := projectconfig.Load(callerDir)
			require.NoError(t, err)
			if configured {
				require.Equal(t, filepath.Dir(callerDir), cfg.Dir)
			} else {
				require.Empty(t, cfg.Dir, "fixture must not inherit project configuration")
			}

			ctrl := gomock.NewController(t)
			client := newClientMock(ctrl)
			sessionErr := errors.New("stop after inspecting session configuration")
			client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).DoAndReturn(
				func(_ context.Context, cfg *copilot.SessionConfig) (execution.CopilotSession, error) {
					assert.Equal(t, []string{rootDir, wantSkillDir}, cfg.SkillDirectories)
					for _, dir := range cfg.SkillDirectories {
						rel, err := filepath.Rel(dir, decoyDir)
						require.NoError(t, err)
						assert.False(t, filepath.IsLocal(rel), "skill directory %s exposes decoy %s", dir, decoyDir)
					}
					return nil, sessionErr
				})

			cmd := newTaskFromPromptCmd(&newTaskFromPromptCmdOptions{
				NewTaskList:      func(*ux.TaskListOptions) taskList { return &fakeTaskList{runAll: true} },
				NewCopilotClient: func(*copilot.ClientOptions) execution.CopilotClient { return client },
				DetectContext: func(dir string, opts ...workspace.DetectOption) (*workspace.WorkspaceContext, error) {
					wsCtx, err := workspace.DetectContext(dir, opts...)
					require.NoError(t, err)
					require.Len(t, wsCtx.Skills, 1)
					assert.Equal(t, wantSkillDir, wsCtx.Skills[0].Dir)
					evalPath, err := workspace.FindEval(wsCtx, wsCtx.Skills[0].Name)
					require.NoError(t, err)
					assert.Equal(t, wantEvalPath, evalPath)
					return wsCtx, nil
				},
			})
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs([]string{"--root", rootDir, "use the selected skill", filepath.Join(rootDir, "task.yaml")})
			require.ErrorIs(t, cmd.Execute(), sessionErr)
		})
	}
}

func TestNewTaskFromPromptCommand_DefaultAndRelativeRoots(t *testing.T) {
	for _, rootArg := range []string{"", "selected-root"} {
		name := "default"
		if rootArg != "" {
			name = "relative"
		}
		t.Run(name, func(t *testing.T) {
			callerDir := t.TempDir()
			rootDir := callerDir
			if rootArg != "" {
				rootDir = filepath.Join(callerDir, rootArg)
				decoyDir := filepath.Join(callerDir, "skills", "decoy")
				require.NoError(t, os.MkdirAll(decoyDir, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(decoyDir, "SKILL.md"),
					[]byte("---\nname: decoy\ndescription: Decoy\n---\n"), 0o644))
			}
			skillDir := filepath.Join(rootDir, "skills", "selected")
			require.NoError(t, os.MkdirAll(skillDir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
				[]byte("---\nname: selected\ndescription: Selected\n---\n"), 0o644))
			t.Chdir(callerDir)
			project, err := projectconfig.Load(callerDir)
			require.NoError(t, err)
			require.Empty(t, project.Dir, "fixture must not inherit project configuration")

			client := newClientMock(gomock.NewController(t))
			sessionErr := errors.New("stop after inspecting session configuration")
			client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).DoAndReturn(
				func(_ context.Context, cfg *copilot.SessionConfig) (execution.CopilotSession, error) {
					require.Equal(t, []string{rootDir, skillDir}, cfg.SkillDirectories)
					require.NotEqual(t, rootDir, cfg.WorkingDirectory)
					entries, err := os.ReadDir(cfg.WorkingDirectory)
					require.NoError(t, err)
					require.Empty(t, entries, "source directory must not be copied as fixtures")
					require.Nil(t, cfg.EnableSkills)
					require.Nil(t, cfg.EnableConfigDiscovery)
					require.Empty(t, cfg.AvailableTools)
					require.NotNil(t, cfg.OnPermissionRequest)
					return nil, sessionErr
				})

			cmd := newTaskFromPromptCmd(&newTaskFromPromptCmdOptions{
				NewTaskList:      func(*ux.TaskListOptions) taskList { return &fakeTaskList{runAll: true} },
				NewCopilotClient: func(*copilot.ClientOptions) execution.CopilotClient { return client },
			})
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			args := []string{"use the selected skill", filepath.Join(callerDir, "task.yaml")}
			if rootArg != "" {
				args = append([]string{"--root", rootArg}, args...)
			}
			cmd.SetArgs(args)
			require.ErrorIs(t, cmd.Execute(), sessionErr)
		})
	}
}

// Configuration is located from the working directory, not --root; see docs/GUIDE.md.
// Each fixture contains four distinct skills so the selected configuration
// source is identified by the exact skill directory passed to the session.
func TestNewTaskFromPromptCommand_RootConfigurationComesFromWorkingDirectory(t *testing.T) {
	const customPaths = "paths:\n  skills: custom-skills\n"
	const defaultPaths = "files:\n  evalFile: eval.yaml\n"

	type fixture struct {
		callerDir, rootDir                                   string
		callerDefault, callerCustom, rootDefault, rootCustom string
	}
	tests := []struct {
		name        string
		configDir   func(f fixture) string
		config      string
		wantProject func(f fixture) string
		wantSkill   func(f fixture) string
	}{
		{
			name:        "no-config",
			wantProject: func(fixture) string { return "" },
			wantSkill:   func(f fixture) string { return f.rootDefault },
		},
		{
			name:        "target-root-config-not-consulted",
			configDir:   func(f fixture) string { return f.rootDir },
			config:      customPaths,
			wantProject: func(fixture) string { return "" },
			wantSkill:   func(f fixture) string { return f.rootDefault },
		},
		{
			name:        "caller-config-custom-paths",
			configDir:   func(f fixture) string { return f.callerDir },
			config:      customPaths,
			wantProject: func(f fixture) string { return f.callerDir },
			wantSkill:   func(f fixture) string { return f.callerCustom },
		},
		{
			name:        "caller-config-default-paths",
			configDir:   func(f fixture) string { return f.callerDir },
			config:      defaultPaths,
			wantProject: func(f fixture) string { return f.callerDir },
			wantSkill:   func(f fixture) string { return f.callerDefault },
		},
	}
	for _, tt := range tests {
		for _, relativeRoot := range []bool{false, true} {
			rootForm := "absolute-root"
			if relativeRoot {
				rootForm = "relative-root"
			}
			t.Run(tt.name+"/"+rootForm, func(t *testing.T) {
				writeSkill := func(dir string) string {
					require.NoError(t, os.MkdirAll(dir, 0o755))
					require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"),
						[]byte("---\nname: "+filepath.Base(dir)+"\ndescription: Test skill\n---\n"), 0o644))
					return dir
				}
				f := fixture{callerDir: t.TempDir()}
				f.rootDir = t.TempDir()
				rootArg := f.rootDir
				if relativeRoot {
					f.rootDir = filepath.Join(f.callerDir, "selected-root")
					rootArg = "selected-root"
				}
				f.callerDefault = writeSkill(filepath.Join(f.callerDir, "skills", "caller-default"))
				f.callerCustom = writeSkill(filepath.Join(f.callerDir, "custom-skills", "caller-custom"))
				f.rootDefault = writeSkill(filepath.Join(f.rootDir, "skills", "root-default"))
				f.rootCustom = writeSkill(filepath.Join(f.rootDir, "custom-skills", "root-custom"))
				if tt.configDir != nil {
					require.NoError(t, os.WriteFile(filepath.Join(tt.configDir(f), ".waza.yaml"), []byte(tt.config), 0o644))
				}
				t.Chdir(f.callerDir)

				project, err := projectconfig.Load(f.callerDir)
				require.NoError(t, err)
				require.Equal(t, tt.wantProject(f), project.Dir)
				wantSkillDir := tt.wantSkill(f)

				client := newClientMock(gomock.NewController(t))
				sessionErr := errors.New("stop after inspecting session configuration")
				client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ context.Context, cfg *copilot.SessionConfig) (execution.CopilotSession, error) {
						assert.Equal(t, []string{f.rootDir, wantSkillDir}, cfg.SkillDirectories)
						return nil, sessionErr
					})

				cmd := newTaskFromPromptCmd(&newTaskFromPromptCmdOptions{
					NewTaskList:      func(*ux.TaskListOptions) taskList { return &fakeTaskList{runAll: true} },
					NewCopilotClient: func(*copilot.ClientOptions) execution.CopilotClient { return client },
					DetectContext: func(dir string, opts ...workspace.DetectOption) (*workspace.WorkspaceContext, error) {
						assert.Equal(t, rootArg, dir)
						wsCtx, err := workspace.DetectContext(dir, opts...)
						require.NoError(t, err)
						require.Len(t, wsCtx.Skills, 1)
						assert.Equal(t, wantSkillDir, wsCtx.Skills[0].Dir)
						return wsCtx, nil
					},
				})
				cmd.SetOut(&bytes.Buffer{})
				cmd.SetErr(&bytes.Buffer{})
				cmd.SetArgs([]string{"--root", rootArg, "use a skill", filepath.Join(f.callerDir, "task.yaml")})
				require.ErrorIs(t, cmd.Execute(), sessionErr)
			})
		}
	}
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
