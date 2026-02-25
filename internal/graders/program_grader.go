package graders

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/spboyer/waza/internal/models"
	"github.com/spboyer/waza/internal/projectconfig"
)

// defaultProgramTimeoutSeconds is the default timeout for program graders when none is specified.
const defaultProgramTimeoutSeconds = 30

// ProgramGraderArgs holds the arguments for creating a program grader.
type ProgramGraderArgs struct {
	// Name is the identifier for this grader, used in results and error messages.
	Name string
	// Command is the program to execute for grading.
	Command string `mapstructure:"command"`
	// Args are the arguments to pass to the program.
	Args []string `mapstructure:"args"`
	// Timeout is the maximum execution time in seconds. Defaults to 30 if not set.
	Timeout int `mapstructure:"timeout"`
}

// programGrader runs an external program/script to grade agent output.
// The agent output is passed via stdin, and the workspace directory is
// available as the WAZA_WORKSPACE_DIR environment variable.
// Exit code 0 = pass (1.0), non-zero = fail (0.0).
type programGrader struct {
	name    string
	command string
	args    []string
	timeout time.Duration
}

// NewProgramGrader creates a [programGrader] that runs an external command to grade output.
func NewProgramGrader(args ProgramGraderArgs) (*programGrader, error) {
	if args.Command == "" {
		return nil, fmt.Errorf("program grader '%s' must have a 'command'", args.Name)
	}

	timeout := args.Timeout
	if timeout <= 0 {
		// Per-grader timeout not specified; use project config default
		cfg, err := projectconfig.Load(".")
		if err == nil && cfg != nil && cfg.Graders.ProgramTimeout > 0 {
			timeout = cfg.Graders.ProgramTimeout
		} else {
			timeout = defaultProgramTimeoutSeconds
		}
	}

	return &programGrader{
		name:    args.Name,
		command: args.Command,
		args:    args.Args,
		timeout: time.Duration(timeout) * time.Second,
	}, nil
}

func (pg *programGrader) Name() string            { return pg.name }
func (pg *programGrader) Kind() models.GraderKind { return models.GraderKindProgram }

func (pg *programGrader) Grade(ctx context.Context, gradingContext *Context) (*models.GraderResults, error) {
	return measureTime(func() (*models.GraderResults, error) {
		timeoutCtx, cancel := context.WithTimeout(ctx, pg.timeout)
		defer cancel()

		cmd := exec.CommandContext(timeoutCtx, pg.command, pg.args...)

		// Pass agent output via stdin
		cmd.Stdin = strings.NewReader(gradingContext.Output)

		// Set workspace dir as environment variable
		cmd.Env = append(cmd.Environ(), fmt.Sprintf("WAZA_WORKSPACE_DIR=%s", gradingContext.WorkspaceDir))

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()

		notes := strings.TrimSpace(stdout.String())
		errOutput := strings.TrimSpace(stderr.String())

		if err != nil {
			feedback := fmt.Sprintf("Program exited with error: %v", err)
			if errOutput != "" {
				feedback = fmt.Sprintf("%s; stderr: %s", feedback, errOutput)
			}

			return &models.GraderResults{
				Name:     pg.name,
				Type:     models.GraderKindProgram,
				Score:    0.0,
				Passed:   false,
				Feedback: feedback,
				Details: map[string]any{
					"command":       pg.command,
					"args":          pg.args,
					"exit_error":    err.Error(),
					"stdout":        notes,
					"stderr":        errOutput,
					"workspace_dir": gradingContext.WorkspaceDir,
				},
			}, nil
		}

		feedback := "Program exited successfully"
		if notes != "" {
			feedback = notes
		}

		return &models.GraderResults{
			Name:     pg.name,
			Type:     models.GraderKindProgram,
			Score:    1.0,
			Passed:   true,
			Feedback: feedback,
			Details: map[string]any{
				"command":       pg.command,
				"args":          pg.args,
				"stdout":        notes,
				"stderr":        errOutput,
				"workspace_dir": gradingContext.WorkspaceDir,
			},
		}, nil
	})
}
