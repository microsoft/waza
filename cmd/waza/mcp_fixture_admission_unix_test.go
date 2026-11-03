//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/mcpmock"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestRunCommandMCPFixtureAdmissionDoesNotWaitForFIFOWriter(t *testing.T) {
	for _, scope := range []string{"eval", "task"} {
		for _, kind := range []string{"regular", "symlink-regular", "fifo", "symlink-fifo"} {
			t.Run(scope+"/"+kind, func(t *testing.T) {
				resetRunGlobals()
				dir := t.TempDir()
				fixtures := filepath.Join(dir, "fixtures")
				require.NoError(t, os.Mkdir(fixtures, 0o700))
				path := filepath.Join(fixtures, "ask_user.json")
				target := path
				if strings.HasPrefix(kind, "symlink-") {
					target = filepath.Join(t.TempDir(), "target.json")
					require.NoError(t, os.Symlink(target, path), "external regular-file symlinks remain supported")
				}
				fifo := strings.HasSuffix(kind, "fifo")
				if fifo {
					require.NoError(t, unix.Mkfifo(target, 0o600))
					info, err := os.Stat(path)
					require.NoError(t, err)
					require.NotZero(t, info.Mode()&os.ModeNamedPipe)
				} else {
					require.NoError(t, os.WriteFile(target, []byte(`{"responses":[{"return":{"answer":"ready"}}]}`), 0o600))
				}

				log := filepath.Join(dir, "hooks.log")
				eval := `schemaVersion: "1.1"
name: fixture-admission
skill: example
config: {executor: copilot-sdk, model: test, trials_per_task: 1, timeout_seconds: 10}
metrics: []
tasks: [task.yaml]
hooks:
  before_run:
    - command: '` + cliHookCommand(t, "append", log, "before_run") + `'
      error_on_fail: true
  after_run:
    - command: '` + cliHookCommand(t, "append", log, "after_run") + `'
      error_on_fail: true
`
				task := "id: probe\nname: Probe\ninputs: {prompt: fixture probe}\n"
				declaration := "mcp_mocks:\n  - name: host\n    fixtures: fixtures\n"
				if scope == "eval" {
					eval += declaration
				} else {
					task += declaration
				}
				spec := filepath.Join(dir, "eval.yaml")
				output := filepath.Join(dir, "results.json")
				require.NoError(t, os.WriteFile(spec, []byte(eval), 0o600))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(task), 0o600))
				previous := newRunEngine
				t.Cleanup(func() { newRunEngine = previous })
				constructions := 0
				newRunEngine = func(models.Config) (execution.AgentEngine, error) {
					constructions++
					return &taskAdmissionStartupEngine{}, nil
				}
				cmd := newRunCommand()
				cmd.SetArgs([]string{spec, "--context-dir", dir, "--output", output, "--no-skills", "--skip-graders"})
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				type fixtureResult struct {
					runErr    error
					source    *mcpmock.SourceConfig
					sourceErr error
				}
				done := make(chan fixtureResult, 1)
				go func() {
					runErr := cmd.ExecuteContext(ctx)
					source, sourceErr := mcpmock.FromEvalConfigWithSources(
						models.MCPMockConfig{Name: "host", Fixtures: fixtures}, dir, nil, "1.1")
					done <- fixtureResult{runErr, source, sourceErr}
				}()
				var result fixtureResult
				blocked := false
				select {
				case result = <-done:
				case <-time.After(5 * time.Second):
					cancel()
					if !fifo {
						t.Fatal("regular fixture admission did not return")
					}
					// Release only the test-owned pipe so a regressed reader can exit.
					fd, err := unix.Open(target, unix.O_RDWR|unix.O_NONBLOCK, 0)
					require.NoError(t, err)
					require.NoError(t, unix.Close(fd))
					result = <-done
					blocked = true
				}
				require.False(t, blocked, "fixture admission waited for a FIFO writer instead of rejecting the opened special file")
				if fifo {
					require.ErrorContains(t, result.runErr, "regular")
					require.ErrorContains(t, result.runErr, "ask_user.json")
					require.ErrorContains(t, result.sourceErr, "regular")
					require.ErrorContains(t, result.sourceErr, "ask_user.json")
					require.Zero(t, constructions)
				} else {
					require.ErrorContains(t, result.runErr, "task admission startup sentinel")
					require.NoError(t, result.sourceErr)
					require.Equal(t, map[string]any{"answer": "ready"}, result.source.Config.Tools["ask_user"].Responses[0].Return)
					require.Equal(t, 1, constructions)
				}
				require.NoFileExists(t, output)
				data, err := os.ReadFile(log)
				require.NoError(t, err)
				require.Equal(t, "before_run\nafter_run\n", strings.ReplaceAll(string(data), "\r\n", "\n"))
			})
		}
	}
}
