package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/azure/azure-dev/cli/azd/pkg/ux"
	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/cmd/waza/newtask"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/workspace"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newNewTaskCommand() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "task",
		Short: "Automatically create tasks, using copilot logs or prompts",
	}

	rootCmd.AddCommand(newTaskFromPromptCmd(nil))
	return rootCmd
}

type newTaskFromPromptCmdOptions struct {
	NewTaskList      func(options *ux.TaskListOptions) taskList
	DetectContext    func(dir string, opts ...workspace.DetectOption) (*workspace.WorkspaceContext, error)
	NewCopilotClient func(clientOptions *copilot.ClientOptions) execution.CopilotClient
	CopilotLogDir    func() (string, error)
}

var defaultNewTaskList = func(options *ux.TaskListOptions) taskList {
	return &taskListWrapper{
		inner: ux.NewTaskList(options),
	}
}

func newTaskFromPromptCmd(options *newTaskFromPromptCmdOptions) *cobra.Command {
	if options == nil {
		options = &newTaskFromPromptCmdOptions{}
	}

	newTaskListFn := defaultNewTaskList

	if options.NewTaskList != nil {
		newTaskListFn = options.NewTaskList
	}

	detectContext := workspace.DetectContext

	if options.DetectContext != nil {
		detectContext = options.DetectContext
	}

	copilotLogDirFn := copilotLogDir

	if options.CopilotLogDir != nil {
		copilotLogDirFn = options.CopilotLogDir
	}

	newCopilotClient := options.NewCopilotClient

	var model string
	var rootDir string
	var testName string
	var tags []string
	var overwrite bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "from-prompt <prompt> <task path>",
		Short: "Run your scenario test prompt and automatically generate a task file with graders",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) (finalErr error) {
			prompt, taskFilePath := args[0], args[1]

			taskList := newTaskListFn(&ux.TaskListOptions{})

			taskList.AddTask(ux.TaskOptions{
				Title: "Getting evaluation tasks folder",
				Action: func(spf ux.SetProgressFunc) (ux.TaskState, error) {
					if _, err := os.Stat(taskFilePath); err == nil && !overwrite {
						return ux.Error, fmt.Errorf("%s already exists, can't overwrite without --overwrite", taskFilePath)
					}
					return ux.Success, nil
				},
			})

			var engine *execution.CopilotEngine

			taskList.AddTask(ux.TaskOptions{
				Title: "Starting copilot",
				Action: func(spf ux.SetProgressFunc) (ux.TaskState, error) {
					tmpEngine := execution.NewCopilotEngineBuilder("", &execution.CopilotEngineBuilderOptions{
						NewCopilotClient: newCopilotClient,
					}).Build()

					if err := tmpEngine.Initialize(cmd.Context()); err != nil {
						return ux.Error, fmt.Errorf("failed to initialize copilot SDK: %w", err)
					}

					engine = tmpEngine
					return ux.Success, nil
				},
			})

			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()

				if engine != nil {
					if engineErr := engine.Shutdown(ctx); engineErr != nil {
						finalErr = errors.Join(finalErr, engineErr)
						return
					}
				}
			}()

			var skillPaths []string

			taskList.AddTask(ux.TaskOptions{
				Title: "Discovering skills",
				Action: func(spf ux.SetProgressFunc) (ux.TaskState, error) {
					ctx, err := detectContext(rootDir, configDetectOptions()...)

					if err != nil {
						return ux.Error, err
					}

					for _, skill := range ctx.Skills {
						skillPaths = append(skillPaths, skill.Dir)
					}

					slog.Debug("Adding discovered skill files", slog.Any("skills", skillPaths))
					return ux.Success, nil
				},
			})
			var sessionID string

			taskList.AddTask(ux.TaskOptions{
				Title: "Execute prompt",
				Action: func(spf ux.SetProgressFunc) (ux.TaskState, error) {
					resp, err := engine.Execute(cmd.Context(), &execution.ExecutionRequest{
						ModelID:    model,
						Message:    prompt,
						SkillPaths: skillPaths,
						Timeout:    timeout,
					})

					if err != nil {
						return ux.Error, err
					}

					sessionID = resp.SessionID
					return ux.Success, nil
				},
			})

			taskList.AddTask(ux.TaskOptions{
				Title: "Creating eval task",
				Action: func(spf ux.SetProgressFunc) (state ux.TaskState, finalErr error) {
					logDir, err := copilotLogDirFn()

					if err != nil {
						return ux.Error, err
					}

					logPath := filepath.Join(logDir, sessionID, "events.jsonl")

					testCase, err := newtask.CreateTestCaseFromCopilotLog(logPath, &newtask.CreateTestCaseFromCopilotLogOptions{
						DisplayName: testName,
						TestID:      testName,
						Tags:        tags,
					})

					if err != nil {
						return ux.Error, err
					}

					var root yaml.Node

					if err := root.Encode(testCase); err != nil {
						return ux.Error, err
					}

					root.HeadComment = "yaml-language-server: $schema=https://raw.githubusercontent.com/microsoft/waza/main/schemas/task.schema.json"

					if err := os.MkdirAll(filepath.Dir(taskFilePath), 0755); err != nil {
						return ux.Error, fmt.Errorf("failed to create directory for %s: %w", taskFilePath, err)
					}

					writer, err := os.Create(taskFilePath)

					if err != nil {
						return ux.Error, err
					}

					defer func() {
						err := writer.Close()
						finalErr = errors.Join(finalErr, err)
					}()

					encoder := yaml.NewEncoder(writer)
					encoder.SetIndent(2)

					if err := encoder.Encode(root); err != nil {
						return ux.Error, err
					}

					if err := encoder.Close(); err != nil {
						return ux.Error, err
					}

					return ux.Success, nil
				},
			})

			if err := taskList.Run(); err != nil {
				return err
			}

			cmd.Printf("New task file written to %s\n", taskFilePath)
			return nil
		},
	}

	cmd.Flags().StringVar(&model, "model", "claude-sonnet-4.5", "Model to use for generation. These should match GitHub copilot's model names")
	cmd.Flags().StringVar(&rootDir, "root", ".", "Directory used to discover skills")
	cmd.Flags().StringVar(&testName, "testname", "auto-generated-test", "Test name and ID for the generated task")
	cmd.Flags().StringSliceVar(&tags, "tags", nil, "Comma-separated tags to add to the generated task")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "Overwrite output file if it already exists")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "Maximum time to allow for the prompt to complete")

	return cmd
}

func copilotLogDir() (string, error) {
	homeDir, err := os.UserHomeDir()

	if err != nil {
		return "", err
	}

	return filepath.Join(homeDir, ".copilot", "session-state"), nil
}

type taskList interface {
	AddTask(options ux.TaskOptions) taskList
	Run() error
}

type taskListWrapper struct {
	inner *ux.TaskList
}

func (tlw *taskListWrapper) AddTask(options ux.TaskOptions) taskList {
	_ = tlw.inner.AddTask(options)
	return tlw
}

func (tlw *taskListWrapper) Run() error {
	return tlw.inner.Run()
}
