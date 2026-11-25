package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/preflight"
	"github.com/microsoft/waza/internal/projectconfig"
	"github.com/microsoft/waza/internal/scaffold"
	"github.com/microsoft/waza/internal/usagetelemetry"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type initCIStep struct {
	ID   string
	If   string
	Run  string
	Uses string
	Env  map[string]string
	With map[string]string
	// ContinueOnError is untyped so any emitted value, including an expression, is observed.
	ContinueOnError any `yaml:"continue-on-error"`
}

type initCIJob struct {
	If             string
	Needs          string
	Environment    string
	TimeoutMinutes int `yaml:"timeout-minutes"`
	Steps          []initCIStep
}

type initCIWorkflowSpec struct {
	On struct {
		PullRequest map[string]any `yaml:"pull_request"`
		Dispatch    struct {
			Inputs map[string]struct {
				Default any
			}
		} `yaml:"workflow_dispatch"`
	}
	Permissions map[string]string
	Env         map[string]string
	Jobs        map[string]initCIJob
}

func initCIFixture(t *testing.T) (string, initCIWorkflowSpec) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, validateCLIIsolation(dir))
	cmd := newInitCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{dir, "--no-skill"})
	require.NoError(t, cmd.Execute())
	data, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", "eval.yml"))
	require.NoError(t, err)
	var workflow initCIWorkflowSpec
	require.NoError(t, yaml.Unmarshal(data, &workflow))
	readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
	require.NoError(t, err)
	require.Contains(t, string(readme), "checksum-verified standalone Waza "+workflow.Env["WAZA_VERSION"])
	require.Contains(t, string(readme), "Fixture\nisolation is not sandboxing")
	require.Contains(t, string(readme), "unresolved runtime")
	require.Contains(t, string(readme), "WAZA_COPILOT_TOKEN")
	require.Contains(t, string(readme), "Commit the evaluation root's `.gitkeep`")
	cfg, err := projectconfig.Load(dir)
	require.NoError(t, err)
	require.Equal(t, projectconfig.DefaultEngine, cfg.Defaults.Engine)
	return dir, workflow
}

func initCIStepByName(t *testing.T, job initCIJob, name string) initCIStep {
	t.Helper()
	for _, step := range job.Steps {
		if step.ID == name || strings.Contains(step.Run, name) {
			return step
		}
	}
	t.Fatalf("generated step %q not found", name)
	return initCIStep{}
}

func initCIRunMain(t *testing.T, diagnostics *bytes.Buffer, args []string) int {
	t.Helper()
	for _, name := range []string{usagetelemetry.OptOutEnv, usagetelemetry.NestedEnv, usagetelemetry.CIEnv} {
		t.Setenv(name, os.Getenv(name))
	}
	return runMain(diagnostics, args)
}

func initCIShell(t *testing.T, dir, script string, env map[string]string) (string, error) {
	t.Helper()
	binary, err := os.Executable()
	require.NoError(t, err)
	prefix := "waza() { \"$WAZA_TEST_BINARY\" -test.run='^TestInitCIHelperProcess$' -- --no-telemetry \"$@\"; }\n"
	command := exec.Command("bash", "-euo", "pipefail", "-c", prefix+script)
	command.Dir = dir
	command.Env = append(os.Environ(), "WAZA_TEST_BINARY="+filepath.ToSlash(binary),
		"WAZA_INIT_CI_HELPER=1", "WAZA_NO_UPDATE_CHECK=1", "NO_COLOR=1",
		"TMP="+dir, "TEMP="+dir, "TMPDIR="+dir,
		"COPILOT_GITHUB_TOKEN=", "GH_TOKEN=", "GITHUB_TOKEN=", "BASH_ENV=")
	for key, value := range env {
		command.Env = append(command.Env, key+"="+value)
	}
	if runtime.GOOS == "windows" {
		// The consumer runs on Ubuntu. A fixture-local startup file gives
		// every nested Bash process LF jq output without exported functions.
		bashEnv := filepath.Join(dir, "init-ci-bash-env.sh")
		require.NoError(t, os.WriteFile(bashEnv, []byte("jq() { command jq \"$@\" | tr -d '\\r'; }\n"), 0o600))
		command.Env = append(command.Env, "BASH_ENV="+filepath.ToSlash(bashEnv))
	}
	out, err := command.CombinedOutput()
	return string(out), err
}

func initCIWriteEval(t *testing.T, dir, grader string) string {
	t.Helper()
	initCIAddSkill(t, dir, "reviewed")
	evalDir := filepath.Join(dir, "evals", "reviewed")
	return initCIWriteEvalAt(t, evalDir, "eval.yaml", grader)
}

func initCIAddSkill(t *testing.T, dir, name string) {
	t.Helper()
	skillDir := filepath.Join(dir, "skills", name)
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# "+name+"\n"), 0o600))
}

func initCIRunGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	args = append([]string{"-c", "core.autocrlf=false", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=" + filepath.Join(dir, "no-hooks")}, args...)
	command := exec.CommandContext(t.Context(), "git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Waza test", "GIT_AUTHOR_EMAIL=waza-test@example.invalid",
		"GIT_COMMITTER_NAME=Waza test", "GIT_COMMITTER_EMAIL=waza-test@example.invalid")
	out, err := command.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return string(out)
}

func initCIWriteEvalAt(t *testing.T, evalDir, evalFile, grader string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(evalDir, "fixtures"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(evalDir, "task.yaml"), []byte("id: task\nname: Task\ninputs: {prompt: ready}\n"), 0o600))
	path := filepath.Join(evalDir, evalFile)
	spec := "schemaVersion: '1.4'\nname: reviewed\nskill: reviewed\nversion: '1.0'\nconfig: {executor: copilot-sdk, model: never-call, trials_per_task: 1, timeout_seconds: 5}\nmetrics: [{name: accuracy, weight: 1, threshold: 0.8}]\ntasks: [task.yaml]\n" + grader
	require.NoError(t, os.WriteFile(path, []byte(spec), 0o600))
	return filepath.ToSlash(path)
}

func TestInitCI_EntryPointRestoresTelemetryEnvironment(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(fmt.Sprintf("initially_present_%t", present), func(t *testing.T) {
			t.Setenv("CI", "true")
			names := []string{usagetelemetry.OptOutEnv, usagetelemetry.NestedEnv, usagetelemetry.CIEnv}
			for _, name := range names {
				t.Setenv(name, "")
				if !present {
					require.NoError(t, os.Unsetenv(name))
				}
			}
			t.Run("init", func(t *testing.T) {
				t.Setenv("WAZA_NO_UPDATE_CHECK", "1")
				dir := t.TempDir()
				require.NoError(t, validateCLIIsolation(dir))
				var diagnostics bytes.Buffer
				code := initCIRunMain(t, &diagnostics, []string{"--no-telemetry", "init", dir, "--no-skill"})
				require.Equal(t, ExitSuccess, code, diagnostics.String())
				require.Equal(t, "1", os.Getenv(usagetelemetry.OptOutEnv))
				require.Equal(t, "1", os.Getenv(usagetelemetry.CIEnv))
			})
			for _, name := range names {
				value, exists := os.LookupEnv(name)
				require.Equal(t, present, exists, name)
				require.Empty(t, value, name)
			}
		})
	}
}

func TestInitCI_NestedNativeJQ(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native Windows jq output normalization")
	}
	t.Setenv("BASH_ENV", "")
	dir, _ := initCIFixture(t)
	native := exec.Command("jq", "-nr", "true")
	raw, err := native.Output()
	require.NoError(t, err)
	require.Contains(t, []string{"true\n", "true\r\n"}, string(raw), "accept valid native jq line endings")
	// A native process boundary can strip exported Bash functions. The
	// fixture must initialize jq normalization in the nested shell itself.
	out, err := initCIShell(t, dir,
		`env -u 'BASH_FUNC_jq%%' bash -euo pipefail -c 'jq -nr true'`, nil)
	require.NoError(t, err, out)
	require.Equal(t, "true\n", out)
	out, err = initCIShell(t, dir,
		`env -u 'BASH_FUNC_jq%%' bash -euo pipefail -c 'jq -en false >/dev/null'`, nil)
	require.Error(t, err, out)
}

func TestInitCI_OfflineCommandsAndTrust(t *testing.T) {
	dir, workflow := initCIFixture(t)
	require.Equal(t, map[string]string{"contents": "read"}, workflow.Permissions)
	require.NotContains(t, workflow.On.PullRequest, "branches")
	require.NotContains(t, workflow.On.PullRequest, "branches-ignore")
	require.Equal(t, false, workflow.On.Dispatch.Inputs["run-agent"].Default)
	validate := workflow.Jobs["validate"]
	require.Equal(t, 10, validate.TimeoutMinutes)
	require.Empty(t, validate.If)
	for _, step := range validate.Steps {
		for _, value := range step.Env {
			require.NotContains(t, value, "secrets.")
		}
		require.NotContains(t, step.Run, "waza run")
	}
	offline := initCIStepByName(t, validate, "waza preflight")
	env := offline.Env
	env["WAZA_INIT_CI_MODE"] = "reject"
	out, err := initCIShell(t, dir, offline.Run, env)
	var emptyExit *exec.ExitError
	require.ErrorAs(t, err, &emptyExit, out)
	require.Equal(t, ExitError, emptyExit.ExitCode(), out)
	require.Contains(t, out, "workspace.empty")
	path := initCIWriteEval(t, dir, "")
	out, err = initCIShell(t, dir, offline.Run, env)
	require.NoError(t, err, out)
	require.Contains(t, out, "unresolved")
	require.Contains(t, out, "not runtime readiness or evaluation success")
	require.NoFileExists(t, filepath.Join(dir, "runtime-invoked"))
	require.NoFileExists(t, filepath.Join(dir, "results.json"))
	require.NoError(t, os.WriteFile(filepath.FromSlash(path), []byte("config: [invalid]\n"), 0o600))
	out, err = initCIShell(t, dir, offline.Run, env)
	require.Error(t, err, out)
	require.NoFileExists(t, filepath.Join(dir, "runtime-invoked"))

	agent := workflow.Jobs["agent"]
	require.Equal(t, "validate", agent.Needs)
	require.Equal(t, "waza-agent", agent.Environment)
	require.Equal(t, 45, agent.TimeoutMinutes)
	// Evaluate the emitted condition with deterministic event values, not a
	// separately authored copy of the desired gate. GitHub expression string
	// comparison ignores case, so evaluate it with nocasematch.
	condition := strings.NewReplacer(
		"github.event_name", `"$EVENT"`,
		"inputs.run-agent", `"$OPT_IN" == true`,
		"github.ref", `"$REF"`,
		"format('refs/heads/{0}', github.event.repository.default_branch)", `"$DEFAULT_REF"`,
	).Replace(agent.If)
	// Git refs are case-sensitive; the first job step must enforce the exact
	// ref before any step can receive the environment secret.
	guard := initCIStepByName(t, agent, "exact-default-ref")
	require.Equal(t, guard.ID, agent.Steps[0].ID, "exact ref guard must precede all other agent steps")
	require.Empty(t, guard.If)
	require.Nil(t, guard.ContinueOnError, "a failed exact ref guard must stop the job")
	for _, step := range agent.Steps[1:] {
		for _, value := range step.Env {
			if strings.Contains(value, "secrets.") {
				require.Empty(t, step.If, "secret-bearing step %q must not run after a failed ref guard", step.ID)
			}
		}
	}
	require.Equal(t, "refs/heads/${{ github.event.repository.default_branch }}", guard.Env["DEFAULT_REF"])
	for _, tc := range []struct {
		event, optIn, ref, defaultRef string
		jobSelected, admitted         bool
	}{
		{"pull_request", "true", "refs/heads/main", "refs/heads/main", false, false},
		{"push", "true", "refs/heads/main", "refs/heads/main", false, false},
		{"workflow_dispatch", "false", "refs/heads/main", "refs/heads/main", false, false},
		{"workflow_dispatch", "true", "refs/heads/topic", "refs/heads/main", false, false},
		{"workflow_dispatch", "true", "refs/heads/main", "refs/heads/main", true, true},
		{"pull_request", "true", "refs/heads/develop", "refs/heads/develop", false, false},
		{"workflow_dispatch", "true", "refs/heads/main", "refs/heads/develop", false, false},
		{"workflow_dispatch", "true", "refs/heads/develop", "refs/heads/develop", true, true},
		// A distinct case-variant branch passes the case-insensitive job
		// condition and must be rejected by the exact guard.
		{"workflow_dispatch", "true", "refs/heads/MAIN", "refs/heads/main", true, false},
		{"workflow_dispatch", "true", "refs/heads/Develop", "refs/heads/develop", true, false},
	} {
		out, err = initCIShell(t, dir, "shopt -s nocasematch\n[[ "+condition+" ]]", map[string]string{
			"EVENT": tc.event, "OPT_IN": tc.optIn, "REF": tc.ref, "DEFAULT_REF": tc.defaultRef,
		})
		require.Equal(t, tc.jobSelected, err == nil, "job condition %+v: %s", tc, out)
		if !tc.jobSelected {
			continue
		}
		defaultBranch := strings.TrimPrefix(tc.defaultRef, "refs/heads/")
		out, err = initCIShell(t, dir, guard.Run, map[string]string{
			"GITHUB_REF":  tc.ref,
			"DEFAULT_REF": strings.ReplaceAll(guard.Env["DEFAULT_REF"], "${{ github.event.repository.default_branch }}", defaultBranch),
		})
		require.Equal(t, tc.admitted, err == nil, "exact ref guard %+v: %s", tc, out)
		if !tc.admitted {
			require.Contains(t, out, "Trusted evaluations run only from "+tc.defaultRef)
		}
	}
}

func TestInitCI_SymlinkedSkillsRootFailsClosed(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(fmt.Sprintf("linked_%t", linked), func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, validateCLIIsolation(dir))
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte("paths:\n  skills: skills\n  evals: evals\n"), 0o600))
			skillsRoot := filepath.Join(dir, "skills")
			if linked {
				skillsRoot = filepath.Join(dir, "real-skills")
			}
			skillDir := filepath.Join(skillsRoot, "reviewed")
			require.NoError(t, os.MkdirAll(skillDir, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# reviewed\n"), 0o600))
			eval := filepath.FromSlash(initCIWriteEvalAt(t, skillDir, "eval.yaml", ""))
			if linked {
				if err := os.Symlink("real-skills", filepath.Join(dir, "skills")); err != nil {
					t.Skipf("directory symlink unavailable: %v", err)
				}
			}

			initOut, err := initCIShell(t, dir, `waza init --no-skill`, map[string]string{"WAZA_INIT_CI_MODE": "reject"})
			require.NoError(t, err, initOut)
			workflowBytes, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", "eval.yml"))
			require.NoError(t, err)
			var workflow initCIWorkflowSpec
			require.NoError(t, yaml.Unmarshal(workflowBytes, &workflow))
			step := initCIStepByName(t, workflow.Jobs["validate"], "waza preflight")
			step.Env["WAZA_INIT_CI_MODE"] = "reject"
			out, err := initCIShell(t, dir, step.Run, step.Env)
			if linked {
				var exitErr *exec.ExitError
				require.ErrorAs(t, err, &exitErr, out)
				require.Equal(t, ExitError, exitErr.ExitCode(), out)
				require.Contains(t, out, "skill discovery root")
				require.Contains(t, out, "is a symlink")
				require.NotContains(t, out, "Offline inventory only.")
			} else {
				require.NoError(t, err, out)
				require.Contains(t, out, "Offline preflight: "+eval)
				require.Contains(t, out, "Offline inventory only.")
			}
			require.NoFileExists(t, filepath.Join(dir, "runtime-invoked"))
		})
	}
}

func TestInitCI_WorkspaceInventoryUsesNativeEvalLayouts(t *testing.T) {
	for _, layout := range []string{"separated", "nested", "co-located", "linked directory"} {
		t.Run(layout, func(t *testing.T) {
			dir, workflow := initCIFixture(t)
			initCIAddSkill(t, dir, "reviewed")
			evalPath := filepath.Join(dir, "evals", "reviewed", "eval.yaml")
			switch layout {
			case "nested":
				evalPath = filepath.Join(dir, "skills", "reviewed", "evals", "eval.yaml")
			case "co-located":
				evalPath = filepath.Join(dir, "skills", "reviewed", "eval.yaml")
			case "linked directory":
				targetDir := filepath.Join(dir, "linked-evals", "reviewed")
				require.NoError(t, os.MkdirAll(targetDir, 0o755))
				evalPath = filepath.Join(targetDir, "eval.yaml")
				linkPath := filepath.Join(dir, "evals", "reviewed")
				if err := os.Symlink(filepath.Dir(evalPath), linkPath); err != nil {
					t.Skipf("directory symlink unavailable: %v", err)
				}
			}
			require.NoError(t, os.MkdirAll(filepath.Dir(evalPath), 0o755))
			require.NoError(t, os.WriteFile(evalPath, []byte("config: [invalid]\n"), 0o600))

			step := initCIStepByName(t, workflow.Jobs["validate"], "waza preflight")
			step.Env["WAZA_INIT_CI_MODE"] = "reject"
			out, err := initCIShell(t, dir, step.Run, step.Env)
			require.Error(t, err, out)
			require.Contains(t, out, "invalid eval.schema")
			require.NotContains(t, out, "No evaluation files found")
			require.NoFileExists(t, filepath.Join(dir, "runtime-invoked"))
		})
	}
}

func TestInitCI_EmptyEvaluationRootSurvivesCheckout(t *testing.T) {
	dir := t.TempDir()
	cmd := newInitCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{dir, "--no-skill"})
	require.NoError(t, cmd.Execute())
	evalsDir := filepath.Join(dir, "evals")
	require.FileExists(t, filepath.Join(evalsDir, ".gitkeep"))

	// Git checkouts do not retain empty, untracked directories.
	require.NoError(t, os.Remove(filepath.Join(dir, "skills")))
	data, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", "eval.yml"))
	require.NoError(t, err)
	var workflow initCIWorkflowSpec
	require.NoError(t, yaml.Unmarshal(data, &workflow))
	step := initCIStepByName(t, workflow.Jobs["validate"], "waza preflight")
	out, err := initCIShell(t, dir, step.Run, step.Env)
	var emptyExit *exec.ExitError
	require.ErrorAs(t, err, &emptyExit, out)
	require.Equal(t, ExitError, emptyExit.ExitCode(), out)
	require.Contains(t, out, "workspace.empty")

	require.NoError(t, os.Remove(filepath.Join(evalsDir, ".gitkeep")))
	require.NoError(t, os.Remove(evalsDir))
	out, err = initCIShell(t, dir, step.Run, step.Env)
	require.Error(t, err, out)
	require.Contains(t, out, "Evaluation directory is missing")
}

func TestInitCI_EvaluationRootSurvivesGitCheckout(t *testing.T) {
	tests := []struct {
		name        string
		evalsDir    string
		file        string
		ignored     bool
		tracked     bool
		stageMarker bool
	}{
		{name: "ignored file", evalsDir: "evals", file: "tool.exe", ignored: true, stageMarker: true},
		{name: "untracked file", evalsDir: filepath.Join("suites", "evals"), file: "notes.txt", stageMarker: true},
		{name: "tracked file", evalsDir: "evals", file: "tracked.txt", tracked: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, validateCLIIsolation(dir))
			runGit := func(dir string, args ...string) string {
				t.Helper()
				return initCIRunGit(t, dir, args...)
			}
			runGit(dir, "init", "-q", "-b", "main")
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.exe\n"), 0o600))
			evalsDir := filepath.Join(dir, tt.evalsDir)
			require.NoError(t, os.MkdirAll(evalsDir, 0o755))
			file := filepath.Join(evalsDir, tt.file)
			original := []byte("existing evaluation-root file\n")
			require.NoError(t, os.WriteFile(file, original, 0o600))
			relativeFile := filepath.ToSlash(filepath.Join(tt.evalsDir, tt.file))
			if tt.ignored {
				require.Equal(t, relativeFile, strings.TrimSpace(runGit(dir, "check-ignore", "--", relativeFile)))
			}
			if tt.tracked {
				runGit(dir, "add", "--", ".gitignore", relativeFile)
				runGit(dir, "commit", "-qm", "track evaluation file")
				require.NoError(t, os.WriteFile(filepath.Join(evalsDir, ".gitkeep"), []byte("existing marker\n"), 0o600))
			}

			cmd := newInitCommand()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetArgs([]string{dir, "--no-skill", "--evals-dir", tt.evalsDir})
			require.NoError(t, cmd.Execute())
			preserved, err := os.ReadFile(file)
			require.NoError(t, err)
			require.Equal(t, original, preserved)

			runGit(dir, "add", "--", ".gitignore", ".waza.yaml", ".github/workflows/eval.yml", "README.md")
			marker := filepath.Join(evalsDir, ".gitkeep")
			if tt.tracked {
				markerContent, err := os.ReadFile(marker)
				require.NoError(t, err)
				require.Equal(t, "existing marker\n", string(markerContent))
			}
			if tt.stageMarker {
				_, err := os.Stat(marker)
				if err == nil {
					runGit(dir, "add", "--", filepath.ToSlash(filepath.Join(tt.evalsDir, ".gitkeep")))
				} else {
					require.ErrorIs(t, err, os.ErrNotExist)
				}
			}
			staged := runGit(dir, "ls-files", "--", filepath.ToSlash(tt.evalsDir))
			if tt.tracked {
				require.Contains(t, staged, relativeFile)
				require.NotContains(t, staged, ".gitkeep", "the tracked-file control must not rely on the marker")
			} else {
				require.NotContains(t, staged, relativeFile, "the fixture file must not survive the checkout")
			}
			runGit(dir, "commit", "-qm", "add generated workflow")

			checkout := t.TempDir()
			runGit(dir, "clone", "-q", "--no-local", dir, checkout)
			data, err := os.ReadFile(filepath.Join(checkout, ".github", "workflows", "eval.yml"))
			require.NoError(t, err)
			var workflow initCIWorkflowSpec
			require.NoError(t, yaml.Unmarshal(data, &workflow))
			step := initCIStepByName(t, workflow.Jobs["validate"], "waza preflight")
			out, err := initCIShell(t, checkout, step.Run, step.Env)
			var emptyExit *exec.ExitError
			require.ErrorAs(t, err, &emptyExit, out)
			require.Equal(t, ExitError, emptyExit.ExitCode(), out)
			require.Contains(t, out, "workspace.empty")
			require.DirExists(t, filepath.Join(checkout, tt.evalsDir))
			if tt.tracked {
				content, err := os.ReadFile(filepath.Join(checkout, tt.evalsDir, tt.file))
				require.NoError(t, err)
				require.Equal(t, original, content)
			} else {
				require.NoFileExists(t, filepath.Join(checkout, tt.evalsDir, tt.file))
				require.FileExists(t, filepath.Join(checkout, tt.evalsDir, ".gitkeep"))
			}
		})
	}
}

func TestInitCI_EmptySymlinkedEvaluationRootIsKept(t *testing.T) {
	dir, _ := initCIFixture(t)
	evalsDir := filepath.Join(dir, "evals")
	require.NoError(t, os.Remove(filepath.Join(evalsDir, ".gitkeep")))
	require.NoError(t, os.Remove(evalsDir))
	inside := filepath.Join(dir, "shared-evals")
	require.NoError(t, os.MkdirAll(inside, 0o755))
	if err := os.Symlink(inside, evalsDir); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	cmd := newInitCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{dir, "--no-skill"})
	require.NoError(t, cmd.Execute())
	require.FileExists(t, filepath.Join(inside, ".gitkeep"), "an empty in-project symlink target must survive checkout")

	outside := t.TempDir()
	require.NoError(t, os.Remove(evalsDir))
	require.NoError(t, os.Symlink(outside, evalsDir))
	cmd = newInitCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{dir, "--no-skill"})
	require.NoError(t, cmd.Execute())
	require.NoFileExists(t, filepath.Join(outside, ".gitkeep"), "init must not write outside the project")
}

func TestInitCI_EvaluationRootUnderSymlink(t *testing.T) {
	t.Run("outside missing descendant", func(t *testing.T) {
		dir := t.TempDir()
		outside := t.TempDir()
		sentinel := filepath.Join(outside, "sentinel.txt")
		require.NoError(t, os.WriteFile(sentinel, []byte("outside sentinel"), 0o600))
		if err := os.Symlink(outside, filepath.Join(dir, "alias")); err != nil {
			t.Skipf("directory symlink unavailable: %v", err)
		}

		cmd := newInitCommand()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetArgs([]string{dir, "--no-skill", "--evals-dir", filepath.Join("alias", "nested")})
		err := cmd.Execute()
		require.NoDirExists(t, filepath.Join(outside, "nested"), "init must not create missing directories outside the project")
		preserved, readErr := os.ReadFile(sentinel)
		require.NoError(t, readErr)
		require.Equal(t, "outside sentinel", string(preserved))
		require.Error(t, err)
	})

	t.Run("outside existing task", func(t *testing.T) {
		dir := t.TempDir()
		outside := t.TempDir()
		initCIAddSkill(t, dir, "reviewed")
		tasks := filepath.Join(outside, "nested", "reviewed", "tasks")
		require.NoError(t, os.MkdirAll(tasks, 0o755))
		sentinel := filepath.Join(tasks, "basic-usage.yaml")
		original := []byte("existing task sentinel\n")
		require.NoError(t, os.WriteFile(sentinel, original, 0o600))
		if err := os.Symlink(outside, filepath.Join(dir, "alias")); err != nil {
			t.Skipf("directory symlink unavailable: %v", err)
		}

		cmd := newInitCommand()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetArgs([]string{dir, "--no-skill", "--evals-dir", filepath.Join("alias", "nested")})
		err := cmd.Execute()
		preserved, readErr := os.ReadFile(sentinel)
		require.NoError(t, readErr)
		require.Equal(t, original, preserved, "missing-eval scaffolding must preserve the existing task")
		require.NoFileExists(t, filepath.Join(outside, "nested", "reviewed", "eval.yaml"))
		require.Error(t, err)
	})

	for _, absolute := range []bool{false, true} {
		name := "relative inside"
		if absolute {
			name = "absolute inside"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			inside := filepath.Join(dir, "shared")
			nested := filepath.Join(inside, "nested")
			require.NoError(t, os.MkdirAll(nested, 0o755))
			sentinel := filepath.Join(nested, "existing.txt")
			require.NoError(t, os.WriteFile(sentinel, []byte("inside sentinel\n"), 0o600))
			target := "shared"
			if absolute {
				target = inside
			}
			if err := os.Symlink(target, filepath.Join(dir, "alias")); err != nil {
				t.Skipf("directory symlink unavailable: %v", err)
			}
			if absolute {
				workflowPath := filepath.Join(dir, ".github", "workflows", "eval.yml")
				require.NoError(t, os.MkdirAll(filepath.Dir(workflowPath), 0o755))
				require.NoError(t, os.WriteFile(workflowPath, []byte("existing workflow\n"), 0o600))
			}
			cmd := newInitCommand()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetArgs([]string{dir, "--no-skill", "--evals-dir", filepath.Join("alias", "nested")})
			require.NoError(t, cmd.Execute())
			require.FileExists(t, filepath.Join(nested, ".gitkeep"))
			preserved, err := os.ReadFile(sentinel)
			require.NoError(t, err)
			require.Equal(t, "inside sentinel\n", string(preserved))
		})
	}
}

func TestInitCI_RejectsNonportableWorkflowSymlinks(t *testing.T) {
	for _, mode := range []string{"absolute eval root", "absolute eval parent", "absolute skills root", "absolute link chain", "relative outside", "dangling absolute"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("WAZA_NO_UPDATE_CHECK", "1")
			dir := t.TempDir()
			require.NoError(t, validateCLIIsolation(dir))
			target := filepath.Join(dir, "shared")
			if mode == "relative outside" {
				target = t.TempDir()
			}
			require.NoError(t, os.MkdirAll(target, 0o700))
			sentinel := filepath.Join(target, "sentinel.txt")
			if mode != "dangling absolute" {
				require.NoError(t, os.WriteFile(sentinel, []byte("existing sentinel\r\n"), 0o600))
			}

			link := filepath.Join(dir, "alias")
			linkTarget := target
			switch mode {
			case "absolute link chain":
				linkTarget = "indirect"
				if err := os.Symlink(target, filepath.Join(dir, "indirect")); err != nil {
					t.Skipf("directory symlink unavailable: %v", err)
				}
			case "relative outside":
				var err error
				linkTarget, err = filepath.Rel(dir, target)
				require.NoError(t, err)
			}
			if err := os.Symlink(linkTarget, link); err != nil {
				t.Skipf("directory symlink unavailable: %v", err)
			}
			if mode == "dangling absolute" {
				require.NoError(t, os.Remove(target))
			}
			path := "alias"
			if mode == "absolute eval parent" || mode == "dangling absolute" {
				path = filepath.Join(path, "nested")
			}
			flag := "--evals-dir"
			if mode == "absolute skills root" {
				flag = "--skills-dir"
			}

			var diagnostics bytes.Buffer
			code := initCIRunMain(t, &diagnostics, []string{"--no-telemetry", "init", dir, "--no-skill", flag, path})
			require.Equal(t, ExitError, code, diagnostics.String())
			require.Contains(t, diagnostics.String(), "cannot generate CI")
			require.Contains(t, diagnostics.String(), "symlink")
			require.NoFileExists(t, filepath.Join(dir, ".github", "workflows", "eval.yml"))
			require.NoFileExists(t, filepath.Join(dir, ".waza.yaml"))
			require.NoDirExists(t, filepath.Join(dir, "skills"))
			require.NoDirExists(t, filepath.Join(target, "nested"))
			if mode == "dangling absolute" {
				require.NoDirExists(t, target, "rejected dangling link must not create its target")
			} else {
				preserved, err := os.ReadFile(sentinel)
				require.NoError(t, err)
				require.Equal(t, "existing sentinel\r\n", string(preserved))
				require.NoFileExists(t, filepath.Join(target, ".gitkeep"))
			}
		})
	}
}

func TestInitCI_RelativeEvaluationSymlinkSurvivesGitCheckout(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, validateCLIIsolation(dir))
	target := filepath.Join(dir, "shared-evals")
	require.NoError(t, os.MkdirAll(target, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(target, "sentinel.txt"), []byte("tracked sentinel\n"), 0o600))
	if err := os.Symlink("shared-evals", filepath.Join(dir, "evals")); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	cmd := newInitCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{dir, "--no-skill", "--evals-dir", "evals"})
	require.NoError(t, cmd.Execute())
	require.FileExists(t, filepath.Join(target, ".gitkeep"))

	initCIRunGit(t, dir, "init", "-q", "-b", "main")
	initCIRunGit(t, dir, "add", "--", "evals", "shared-evals", ".waza.yaml", ".github/workflows/eval.yml", ".gitignore", "README.md")
	require.Contains(t, initCIRunGit(t, dir, "ls-files", "-s", "--", "evals"), "120000 ")
	initCIRunGit(t, dir, "commit", "-qm", "portable evaluation link")
	checkout := t.TempDir()
	initCIRunGit(t, dir, "-c", "core.symlinks=true", "clone", "-q", "--no-local", dir, checkout)
	require.NoError(t, os.Rename(target, filepath.Join(dir, "unavailable-original")))
	require.NoDirExists(t, target, "the original link target must be unavailable")
	info, err := os.Lstat(filepath.Join(checkout, "evals"))
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink, "checkout must contain a real symlink")
	content, err := os.ReadFile(filepath.Join(checkout, "evals", "sentinel.txt"))
	require.NoError(t, err)
	require.Equal(t, "tracked sentinel\n", string(content))

	data, err := os.ReadFile(filepath.Join(checkout, ".github", "workflows", "eval.yml"))
	require.NoError(t, err)
	var workflow initCIWorkflowSpec
	require.NoError(t, yaml.Unmarshal(data, &workflow))
	step := initCIStepByName(t, workflow.Jobs["validate"], "waza preflight")
	step.Env["WAZA_INIT_CI_MODE"] = "reject"
	out, err := initCIShell(t, checkout, step.Run, step.Env)
	var emptyExit *exec.ExitError
	require.ErrorAs(t, err, &emptyExit, out)
	require.Equal(t, ExitError, emptyExit.ExitCode(), out)
	require.Contains(t, out, "workspace.empty")
	require.NoFileExists(t, filepath.Join(checkout, "runtime-invoked"))
}

func TestInitCI_RelocatedEvalSymlinks(t *testing.T) {
	for _, tc := range []struct {
		name, layout    string
		absolute        bool
		untrackedTarget bool
		wantBroken      bool
		unrelatedLink   bool
		unrelatedDir    bool
		unsupported     bool
	}{
		{name: "absolute suite", layout: "suite", absolute: true, wantBroken: true},
		{name: "absolute manifest", layout: "manifest", absolute: true, wantBroken: true},
		{name: "untracked relative suite target", layout: "suite", untrackedTarget: true, wantBroken: true},
		{name: "absolute nested eval directory", layout: "nested", absolute: true, wantBroken: true},
		{name: "absolute colocated manifest", layout: "colocated", absolute: true, wantBroken: true},
		{name: "absolute skill definition", layout: "definition", absolute: true, wantBroken: true},
		{name: "absolute skills root", layout: "skills root", absolute: true, wantBroken: true, unsupported: true},
		{name: "absolute skill directory", layout: "skill child", absolute: true, wantBroken: true, unsupported: true},
		{name: "relative skill directory", layout: "skill child", unsupported: true},
		{name: "relative top-level skill directory", layout: "root child", unsupported: true},
		{name: "relative suite", layout: "suite"},
		{name: "relative manifest", layout: "manifest"},
		{name: "relative nested eval directory", layout: "nested"},
		{name: "relative skills root", layout: "skills root", unsupported: true},
		{name: "unrelated dangling fixture", layout: "suite", unrelatedLink: true},
		{name: "unrelated top-level directory link", layout: "suite", unrelatedDir: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, validateCLIIsolation(dir))
			var output bytes.Buffer
			require.Equal(t, ExitSuccess, initCIRunMain(t, &output, []string{"--no-telemetry", "init", dir, "--no-skill"}), output.String())
			workflowPath := filepath.Join(dir, ".github", "workflows", "eval.yml")
			data, err := os.ReadFile(workflowPath)
			require.NoError(t, err)
			var workflow initCIWorkflowSpec
			require.NoError(t, yaml.Unmarshal(data, &workflow))
			step := initCIStepByName(t, workflow.Jobs["validate"], "waza preflight")
			step.Env["WAZA_INIT_CI_MODE"] = "reject"

			shared := filepath.Join(dir, "shared")
			if tc.layout == "root child" {
				shared = filepath.Join(dir, ".storage")
			}
			if tc.layout != "skills root" && tc.layout != "skill child" && tc.layout != "root child" {
				initCIWriteEvalAt(t, shared, "eval.yaml", "")
			}
			skillDir := filepath.Join(dir, "skills", "reviewed")
			var link, target, evalPath string
			switch tc.layout {
			case "suite":
				link, target = filepath.Join(dir, "evals", "reviewed"), shared
				evalPath = filepath.Join(link, "eval.yaml")
			case "manifest":
				evalDir := filepath.Join(dir, "evals", "reviewed")
				require.NoError(t, os.MkdirAll(filepath.Join(evalDir, "fixtures"), 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(evalDir, "task.yaml"), []byte("id: task\nname: Task\ninputs: {prompt: ready}\n"), 0o600))
				link, target = filepath.Join(evalDir, "eval.yaml"), filepath.Join(shared, "eval.yaml")
				evalPath = link
			case "nested":
				initCIAddSkill(t, dir, "reviewed")
				link, target = filepath.Join(skillDir, "evals"), shared
				evalPath = filepath.Join(link, "eval.yaml")
			case "colocated":
				initCIAddSkill(t, dir, "reviewed")
				require.NoError(t, os.MkdirAll(filepath.Join(skillDir, "fixtures"), 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(skillDir, "task.yaml"), []byte("id: task\nname: Task\ninputs: {prompt: ready}\n"), 0o600))
				link, target = filepath.Join(skillDir, "eval.yaml"), filepath.Join(shared, "eval.yaml")
				evalPath = link
			case "definition":
				initCIWriteEvalAt(t, skillDir, "eval.yaml", "")
				require.NoError(t, os.MkdirAll(filepath.Join(shared, "definitions"), 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(shared, "definitions", "SKILL.md"), []byte("# reviewed\n"), 0o600))
				link, target = filepath.Join(skillDir, "SKILL.md"), filepath.Join(shared, "definitions", "SKILL.md")
				evalPath = filepath.Join(skillDir, "eval.yaml")
			case "skills root":
				initCIAddSkill(t, dir, "reviewed")
				initCIWriteEvalAt(t, skillDir, "eval.yaml", "")
				require.NoError(t, os.Rename(filepath.Join(dir, "skills"), shared))
				link, target = filepath.Join(dir, "skills"), shared
				evalPath = filepath.Join(skillDir, "eval.yaml")
			case "skill child", "root child":
				target = filepath.Join(shared, "reviewed")
				initCIWriteEvalAt(t, target, "eval.yaml", "")
				require.NoError(t, os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("# reviewed\n"), 0o600))
				link = skillDir
				if tc.layout == "root child" {
					link = filepath.Join(dir, "reviewed")
				}
				evalPath = filepath.Join(link, "eval.yaml")
			}
			linkTarget := target
			if !tc.absolute {
				linkTarget, err = filepath.Rel(filepath.Dir(link), target)
				require.NoError(t, err)
				linkTarget = filepath.ToSlash(linkTarget)
			}
			if err := os.Symlink(linkTarget, link); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			if tc.unrelatedLink {
				require.NoError(t, os.Symlink("missing.txt", filepath.Join(shared, "fixtures", "unrelated.txt")))
			}
			if tc.unrelatedDir {
				require.NoError(t, os.WriteFile(filepath.Join(shared, "fixtures", "anchor.txt"), []byte("tracked fixture\n"), 0o600))
				require.NoError(t, os.Symlink("shared/fixtures", filepath.Join(dir, "docs-link")))
			}
			out, err := initCIShell(t, dir, step.Run, step.Env)
			if tc.unsupported {
				require.Error(t, err, "the current workspace detector cannot scan a symlinked skill directory")
				var exitErr *exec.ExitError
				require.ErrorAs(t, err, &exitErr)
				require.Equal(t, ExitError, exitErr.ExitCode(), out)
				require.Contains(t, out, "symlink")
				require.Contains(t, out, "skill")
				require.Contains(t, out, filepath.Base(link))
			} else {
				require.NoError(t, err, out)
				require.Equal(t, 1, strings.Count(out, "Offline preflight:"), out)
				require.Contains(t, out, "Offline preflight: "+evalPath)
			}
			require.NotContains(t, out, "No evaluation files found")
			require.NoFileExists(t, filepath.Join(dir, "runtime-invoked"))

			initCIRunGit(t, dir, "init", "-q", "-b", "main")
			initCIRunGit(t, dir, "add", "--", ".waza.yaml", ".github/workflows/eval.yml", "evals")
			if tc.layout != "suite" && tc.layout != "manifest" && tc.layout != "root child" {
				initCIRunGit(t, dir, "add", "--", "skills")
			}
			if tc.layout == "root child" {
				initCIRunGit(t, dir, "add", "--", "reviewed")
			}
			if tc.unrelatedDir {
				initCIRunGit(t, dir, "add", "--", "docs-link")
			}
			if !tc.untrackedTarget {
				sharedRel, err := filepath.Rel(dir, shared)
				require.NoError(t, err)
				initCIRunGit(t, dir, "add", "--", filepath.ToSlash(sharedRel))
			}
			linkRel, err := filepath.Rel(dir, link)
			require.NoError(t, err)
			require.Contains(t, initCIRunGit(t, dir, "ls-files", "-s", "--", filepath.ToSlash(linkRel)), "120000 ")
			initCIRunGit(t, dir, "commit", "-qm", "add linked evaluation")
			checkout := t.TempDir()
			initCIRunGit(t, dir, "-c", "core.symlinks=true", "clone", "-q", "--no-local", dir, checkout)
			require.NoError(t, os.Rename(shared, filepath.Join(dir, "original-unavailable")))
			checkoutLink := filepath.Join(checkout, linkRel)
			info, err := os.Lstat(checkoutLink)
			require.NoError(t, err)
			require.NotZero(t, info.Mode()&os.ModeSymlink, "checkout must preserve the link")
			_, err = os.Stat(checkoutLink)
			if tc.wantBroken {
				require.ErrorIs(t, err, os.ErrNotExist, "the checkout link must be dangling")
			} else {
				require.NoError(t, err, "the checkout link must resolve without the original target")
			}

			out, err = initCIShell(t, checkout, step.Run, step.Env)
			if tc.wantBroken || tc.unsupported {
				require.Error(t, err, "an unavailable or unsupported discovery link must not become a successful inventory: %s", out)
				var exitErr *exec.ExitError
				require.ErrorAs(t, err, &exitErr)
				require.Equal(t, ExitError, exitErr.ExitCode(), out)
				require.Contains(t, out, "symlink")
				require.Contains(t, out, filepath.Base(checkoutLink))
			} else {
				require.NoError(t, err, out)
				evalRel, err := filepath.Rel(dir, evalPath)
				require.NoError(t, err)
				require.Equal(t, 1, strings.Count(out, "Offline preflight:"), out)
				require.Contains(t, out, "Offline preflight: "+filepath.Join(checkout, evalRel))
			}
			if tc.unrelatedLink {
				unrelated := filepath.Join(checkout, "shared", "fixtures", "unrelated.txt")
				info, err := os.Lstat(unrelated)
				require.NoError(t, err)
				require.NotZero(t, info.Mode()&os.ModeSymlink)
				_, err = os.Stat(unrelated)
				require.ErrorIs(t, err, os.ErrNotExist)
			}
			if tc.unrelatedDir {
				info, err := os.Stat(filepath.Join(checkout, "docs-link"))
				require.NoError(t, err)
				require.True(t, info.IsDir())
			}
			require.NotContains(t, out, "No evaluation files found")
			require.NoFileExists(t, filepath.Join(checkout, "runtime-invoked"))
		})
	}
}

func TestInitCI_SkillResourceSymlinks(t *testing.T) {
	for _, tc := range []struct {
		name        string
		layout      string
		wantBlocked bool
	}{
		{name: "unrelated resource", layout: "resource"},
		{name: "nested resource", layout: "nested resource"},
		{name: "resource cycle", layout: "cycle"},
		{name: "ignored vendor skill", layout: "vendor"},
		{name: "linked direct skill", layout: "direct skill", wantBlocked: true},
		{name: "linked nested skill", layout: "nested skill", wantBlocked: true},
		{name: "linked APM skill", layout: "apm skill", wantBlocked: true},
		{name: "linked agent", layout: "agent", wantBlocked: true},
		{name: "indirect linked skill", layout: "indirect skill", wantBlocked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, workflow := initCIFixture(t)
			initCIWriteEval(t, dir, "")
			docs := filepath.Join(dir, "docs")
			require.NoError(t, os.MkdirAll(docs, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(docs, "README.md"), []byte("unrelated resource\n"), 0o600))
			switch tc.layout {
			case "nested resource":
				require.NoError(t, os.MkdirAll(filepath.Join(docs, "guide"), 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(docs, "guide", "notes.md"), []byte("notes\n"), 0o600))
			case "cycle":
				if err := os.Symlink(".", filepath.Join(docs, "loop")); err != nil {
					t.Skipf("directory symlink unavailable: %v", err)
				}
			case "vendor", "direct skill", "nested skill", "apm skill", "agent":
				skillDir := docs
				switch tc.layout {
				case "vendor":
					skillDir = filepath.Join(docs, "vendor", "hidden")
				case "nested skill":
					skillDir = filepath.Join(docs, "nested")
				case "apm skill":
					skillDir = filepath.Join(docs, ".apm", "skills", "hidden")
				}
				require.NoError(t, os.MkdirAll(skillDir, 0o700))
				skillFile := "SKILL.md"
				if tc.layout == "agent" {
					skillFile = "hidden.agent.md"
				}
				require.NoError(t, os.WriteFile(filepath.Join(skillDir, skillFile), []byte("# hidden\n"), 0o600))
			case "indirect skill":
				hidden := filepath.Join(dir, "hidden")
				require.NoError(t, os.MkdirAll(hidden, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(hidden, "SKILL.md"), []byte("# hidden\n"), 0o600))
				if err := os.Symlink("../hidden", filepath.Join(docs, "indirect")); err != nil {
					t.Skipf("directory symlink unavailable: %v", err)
				}
			}
			if err := os.Symlink("../docs", filepath.Join(dir, "skills", "a-docs")); err != nil {
				t.Skipf("directory symlink unavailable: %v", err)
			}

			step := initCIStepByName(t, workflow.Jobs["validate"], "waza preflight")
			step.Env["WAZA_INIT_CI_MODE"] = "reject"
			out, err := initCIShell(t, dir, step.Run, step.Env)
			if tc.wantBlocked {
				require.Error(t, err, out)
				var exitErr *exec.ExitError
				require.ErrorAs(t, err, &exitErr)
				require.Equal(t, ExitError, exitErr.ExitCode(), out)
				require.Contains(t, out, "a-docs")
				require.Contains(t, out, "symlink")
			} else {
				require.NoError(t, err, out)
				require.Equal(t, 1, strings.Count(out, "Offline preflight:"), out)
				require.Contains(t, out, "Offline preflight: "+filepath.Join(dir, "evals", "reviewed", "eval.yaml"))
			}
			require.NotContains(t, out, "No evaluation files found")
			require.NoFileExists(t, filepath.Join(dir, "runtime-invoked"))
		})
	}
}

func TestInitCI_EvalFilenamePrecedenceWithSymlinks(t *testing.T) {
	for _, tc := range []struct {
		name       string
		layout     string
		wantError  bool
		wantSource []string
	}{
		{name: "lower priority dangling", layout: "lower dangling", wantSource: []string{"suite.yml"}},
		{name: "fallback valid", layout: "fallback", wantSource: []string{"eval.yaml"}},
		{name: "child lower priority dangling", layout: "child lower dangling", wantSource: []string{filepath.Join("reviewed", "suite.yml")}},
		{name: "fallback filename is a directory", layout: "fallback directory", wantSource: []string{"suite.yml", filepath.Join("eval.yaml", "suite.yml")}},
		{name: "selected filename dangling", layout: "selected dangling", wantError: true},
		{name: "unrelated candidate dangling", layout: "unrelated dangling", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, workflow := initCIFixture(t)
			evals := filepath.Join(dir, "evals")
			link := ""
			switch tc.layout {
			case "lower dangling", "unrelated dangling":
				initCIWriteEvalAt(t, evals, "suite.yml", "")
				link = filepath.Join(evals, "eval.yaml")
				if tc.layout == "unrelated dangling" {
					link = filepath.Join(evals, "z")
				}
			case "fallback":
				initCIWriteEvalAt(t, evals, "eval.yaml", "")
			case "child lower dangling":
				initCIAddSkill(t, dir, "reviewed")
				child := filepath.Join(evals, "reviewed")
				initCIWriteEvalAt(t, child, "suite.yml", "")
				link = filepath.Join(child, "eval.yaml")
			case "fallback directory":
				initCIWriteEvalAt(t, evals, "suite.yml", "")
				initCIWriteEvalAt(t, filepath.Join(evals, "eval.yaml"), "suite.yml", "")
			case "selected dangling":
				initCIWriteEvalAt(t, evals, "eval.yaml", "")
				link = filepath.Join(evals, "suite.yml")
			}
			if link != "" {
				if err := os.Symlink("missing", link); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			}

			step := initCIStepByName(t, workflow.Jobs["validate"], "waza preflight")
			step.Env["EVAL_FILE"] = "suite.yml"
			step.Env["WAZA_INIT_CI_MODE"] = "reject"
			out, err := initCIShell(t, dir, step.Run, step.Env)
			if tc.wantError {
				require.Error(t, err, out)
				var exitErr *exec.ExitError
				require.ErrorAs(t, err, &exitErr)
				require.Equal(t, ExitError, exitErr.ExitCode(), out)
				require.Contains(t, out, filepath.Base(link))
				require.Contains(t, out, "symlink")
			} else {
				require.NoError(t, err, out)
				require.Equal(t, len(tc.wantSource), strings.Count(out, "Offline preflight:"), out)
				for _, source := range tc.wantSource {
					require.Contains(t, out, "Offline preflight: "+filepath.Join(evals, source))
				}
			}
			require.NotContains(t, out, "No evaluation files found")
			require.NoFileExists(t, filepath.Join(dir, "runtime-invoked"))
		})
	}
}

func TestInitCI_WorkspaceRetainsDiagnosticsOnDiscoveryErrors(t *testing.T) {
	for _, failure := range []string{"none", "eval entries", "skill discovery"} {
		for _, format := range []string{"human", "json"} {
			t.Run(failure+"_"+format, func(t *testing.T) {
				dir, _ := initCIFixture(t)
				evals := filepath.Join(dir, "evals")
				invalid := filepath.Join(evals, "a", "eval.yaml")
				require.NoError(t, os.MkdirAll(filepath.Dir(invalid), 0o700))
				require.NoError(t, os.WriteFile(invalid, []byte("config: [invalid]\n"), 0o600))
				valid := filepath.Join(evals, "c", "eval.yaml")
				initCIWriteEvalAt(t, filepath.Dir(valid), "eval.yaml", "")
				switch failure {
				case "eval entries":
					for _, name := range []string{"b", "z"} {
						if err := os.Symlink("missing", filepath.Join(evals, name)); err != nil {
							t.Skipf("symlink unavailable: %v", err)
						}
					}
				case "skill discovery":
					if err := os.Symlink("missing", filepath.Join(dir, "skills", "broken")); err != nil {
						t.Skipf("symlink unavailable: %v", err)
					}
				}

				outputPath := filepath.Join(dir, "preflight-output")
				errorPath := filepath.Join(dir, "preflight-error")
				out, err := initCIShell(t, dir, `waza preflight --workspace --format "$FORMAT" >"$OUTPUT" 2>"$ERRORS"`, map[string]string{
					"FORMAT": format, "OUTPUT": filepath.ToSlash(outputPath), "ERRORS": filepath.ToSlash(errorPath),
					"WAZA_INIT_CI_MODE": "reject",
				})
				require.Error(t, err, out)
				var exitErr *exec.ExitError
				require.ErrorAs(t, err, &exitErr)
				wantExit := 1
				if failure != "none" {
					wantExit = ExitError
				}
				require.Equal(t, wantExit, exitErr.ExitCode(), out)
				output, err := os.ReadFile(outputPath)
				require.NoError(t, err)
				diagnostics, err := os.ReadFile(errorPath)
				require.NoError(t, err)
				if format == "human" {
					require.Contains(t, string(output), "Offline preflight: "+invalid)
					require.Contains(t, string(output), "Offline preflight: "+valid)
					require.Contains(t, string(output), "invalid eval.schema")
				} else {
					var reports []*preflight.Report
					require.NoError(t, json.Unmarshal(output, &reports), string(output))
					require.Len(t, reports, 2)
					require.Equal(t, invalid, reports[0].Source)
					require.True(t, reports[0].Failed(false), "%+v", reports[0].Diagnostics)
					require.Equal(t, valid, reports[1].Source)
					require.False(t, reports[1].Failed(false), "%+v", reports[1].Diagnostics)
				}
				switch failure {
				case "eval entries":
					require.Contains(t, string(diagnostics), filepath.Join(evals, "b"))
					require.Contains(t, string(diagnostics), filepath.Join(evals, "z"))
					require.Contains(t, string(diagnostics), "symlink")
				case "skill discovery":
					require.Contains(t, string(diagnostics), filepath.Join(dir, "skills", "broken"))
					require.Contains(t, string(diagnostics), "symlink")
				default:
					require.Contains(t, string(diagnostics), "preflight prerequisites failed")
				}
				require.NoFileExists(t, filepath.Join(dir, "runtime-invoked"))
			})
		}
	}
}

func TestInitCI_WorkspaceRetainsColocatedEvalsOnSkillDiscoveryErrors(t *testing.T) {
	for _, withLinks := range []bool{false, true} {
		for _, format := range []string{"human", "json"} {
			t.Run(fmt.Sprintf("links_%t_%s", withLinks, format), func(t *testing.T) {
				dir, _ := initCIFixture(t)
				initCIAddSkill(t, dir, "a")
				initCIAddSkill(t, dir, "c")
				invalid := filepath.Join(dir, "skills", "a", "eval.yaml")
				require.NoError(t, os.WriteFile(invalid, []byte("config: [invalid]\n"), 0o600))
				valid := filepath.Join(dir, "skills", "c", "evals", "eval.yaml")
				initCIWriteEvalAt(t, filepath.Dir(valid), "eval.yaml", "")
				sources := []string{invalid, valid}
				for _, item := range []struct {
					root string
					name string
				}{
					{root: filepath.Join(dir, ".github", "skills"), name: "d"},
					{root: filepath.Join(dir, ".github", "skills"), name: "f"},
					{root: filepath.Join(dir, ".apm", "skills"), name: "g"},
					{root: filepath.Join(dir, ".apm", "skills"), name: "i"},
				} {
					skillDir := filepath.Join(item.root, item.name)
					require.NoError(t, os.MkdirAll(skillDir, 0o700))
					require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# "+item.name+"\n"), 0o600))
					sources = append(sources, filepath.Join(skillDir, "eval.yaml"))
					initCIWriteEvalAt(t, skillDir, "eval.yaml", "")
				}
				broken := []string{
					filepath.Join(dir, "skills", "b"),
					filepath.Join(dir, "skills", "z"),
					filepath.Join(dir, ".github", "skills", "e"),
					filepath.Join(dir, ".apm", "skills", "h"),
				}
				if withLinks {
					for _, path := range broken {
						if err := os.Symlink("missing", path); err != nil {
							t.Skipf("symlink unavailable: %v", err)
						}
					}
				}

				outputPath := filepath.Join(dir, "preflight-output")
				errorPath := filepath.Join(dir, "preflight-error")
				out, err := initCIShell(t, dir, `waza preflight --workspace --format "$FORMAT" >"$OUTPUT" 2>"$ERRORS"`, map[string]string{
					"FORMAT": format, "OUTPUT": filepath.ToSlash(outputPath), "ERRORS": filepath.ToSlash(errorPath),
					"WAZA_INIT_CI_MODE": "reject",
				})
				require.Error(t, err, out)
				var exitErr *exec.ExitError
				require.ErrorAs(t, err, &exitErr)
				wantExit := 1
				if withLinks {
					wantExit = ExitError
				}
				require.Equal(t, wantExit, exitErr.ExitCode(), out)
				output, err := os.ReadFile(outputPath)
				require.NoError(t, err)
				diagnostics, err := os.ReadFile(errorPath)
				require.NoError(t, err)
				if format == "human" {
					for _, source := range sources {
						require.Contains(t, string(output), "Offline preflight: "+source)
					}
					require.Contains(t, string(output), "invalid eval.schema")
				} else {
					var reports []*preflight.Report
					require.NoError(t, json.Unmarshal(output, &reports), string(output))
					require.Len(t, reports, len(sources))
					for i, source := range sources {
						require.Equal(t, source, reports[i].Source)
						require.Equal(t, i == 0, reports[i].Failed(false), "%+v", reports[i].Diagnostics)
					}
				}
				if withLinks {
					for _, path := range broken {
						require.Contains(t, string(diagnostics), path)
					}
					require.Contains(t, string(diagnostics), "symlink")
				} else {
					require.Contains(t, string(diagnostics), "preflight prerequisites failed")
				}
				require.NoFileExists(t, filepath.Join(dir, "runtime-invoked"))
			})
		}
	}
}

func TestInitCI_WorkspaceRetainsOtherSkillLayoutsOnDiscoveryErrors(t *testing.T) {
	for _, layout := range []string{"ancestor APM", "workspace children"} {
		for _, withLinks := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s_links_%t", layout, withLinks), func(t *testing.T) {
				dir, _ := initCIFixture(t)
				skillRoot := dir
				if layout == "ancestor APM" {
					skillRoot = filepath.Join(filepath.Dir(dir), ".apm", "skills")
				}
				var sources []string
				for _, name := range []string{"a", "c"} {
					skillDir := filepath.Join(skillRoot, name)
					require.NoError(t, os.MkdirAll(skillDir, 0o700))
					require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# "+name+"\n"), 0o600))
					eval := filepath.Join(skillDir, "eval.yaml")
					sources = append(sources, eval)
					if name == "a" {
						require.NoError(t, os.WriteFile(eval, []byte("config: [invalid]\n"), 0o600))
					} else {
						initCIWriteEvalAt(t, skillDir, "eval.yaml", "")
					}
				}
				if withLinks {
					for _, name := range []string{"b", "z"} {
						if err := os.Symlink("missing", filepath.Join(skillRoot, name)); err != nil {
							t.Skipf("symlink unavailable: %v", err)
						}
					}
				}

				outputPath := filepath.Join(dir, "preflight-output")
				errorPath := filepath.Join(dir, "preflight-error")
				out, err := initCIShell(t, dir, `waza preflight --workspace --format json >"$OUTPUT" 2>"$ERRORS"`, map[string]string{
					"OUTPUT": filepath.ToSlash(outputPath), "ERRORS": filepath.ToSlash(errorPath), "WAZA_INIT_CI_MODE": "reject",
				})
				require.Error(t, err, out)
				var exitErr *exec.ExitError
				require.ErrorAs(t, err, &exitErr)
				wantExit := 1
				if withLinks {
					wantExit = ExitError
				}
				require.Equal(t, wantExit, exitErr.ExitCode(), out)
				output, err := os.ReadFile(outputPath)
				require.NoError(t, err)
				var reports []*preflight.Report
				require.NoError(t, json.Unmarshal(output, &reports), string(output))
				require.Len(t, reports, len(sources))
				for i, source := range sources {
					require.Equal(t, source, reports[i].Source)
					require.Equal(t, i == 0, reports[i].Failed(false), "%+v", reports[i].Diagnostics)
				}
				diagnostics, err := os.ReadFile(errorPath)
				require.NoError(t, err)
				if withLinks {
					require.Contains(t, string(diagnostics), filepath.Join(skillRoot, "b"))
					require.Contains(t, string(diagnostics), filepath.Join(skillRoot, "z"))
				}
				require.NoFileExists(t, filepath.Join(dir, "runtime-invoked"))
			})
		}
	}
}

func TestInitCI_WorkspaceRetainsRequestedEvalsWithPartialAncestor(t *testing.T) {
	for _, tc := range []struct {
		name             string
		withLink         bool
		override         bool
		absoluteOverride bool
		sameEvalsRoot    bool
		brokenEvalLink   bool
	}{
		{name: "healthy ancestor"},
		{name: "partial ancestor", withLink: true},
		{name: "partial ancestor with relative eval override", withLink: true, override: true},
		{name: "partial ancestor with absolute eval override", withLink: true, override: true, absoluteOverride: true},
		{name: "partial ancestor with aliased eval root", withLink: true, sameEvalsRoot: true},
		{name: "aliased eval root retains discovery errors", withLink: true, sameEvalsRoot: true, brokenEvalLink: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			dir := filepath.Join(parent, "workspace")
			require.NoError(t, os.MkdirAll(dir, 0o700))
			require.NoError(t, validateCLIIsolation(dir))
			cfg, err := projectconfig.Load(dir)
			require.NoError(t, err)
			require.Empty(t, cfg.Dir, "fixture must use the default paths without a config")

			skillDir := filepath.Join(parent, ".apm", "skills", "reviewed")
			require.NoError(t, os.MkdirAll(skillDir, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# reviewed\n"), 0o600))
			skillEval := filepath.Join(skillDir, "eval.yaml")
			initCIWriteEvalAt(t, skillDir, "eval.yaml", "")
			if tc.sameEvalsRoot {
				require.NoError(t, os.MkdirAll(filepath.Join(dir, "evals"), 0o700))
				if err := os.Symlink(filepath.Join("workspace", "evals"), filepath.Join(parent, "evals")); err != nil {
					t.Skipf("directory symlink unavailable: %v", err)
				}
				parentInfo, err := os.Stat(filepath.Join(parent, "evals"))
				require.NoError(t, err)
				requestedInfo, err := os.Stat(filepath.Join(dir, "evals"))
				require.NoError(t, err)
				require.True(t, os.SameFile(parentInfo, requestedInfo), "fixture roots must refer to the same directory")
			}
			parentEval := filepath.Join(parent, "evals", "ancestor-extra", "eval.yaml")
			initCIWriteEvalAt(t, filepath.Dir(parentEval), "eval.yaml", "")
			requestedEval := filepath.Join(dir, "evals", "workspace-extra", "eval.yaml")
			require.NoError(t, os.MkdirAll(filepath.Dir(requestedEval), 0o700))
			require.NoError(t, os.WriteFile(requestedEval, []byte("config: [invalid]\n"), 0o600))
			if tc.brokenEvalLink {
				require.NoError(t, os.Symlink("missing", filepath.Join(dir, "evals", "broken")))
			}
			overrideEval := filepath.Join(dir, "chosen", "eval.yaml")
			initCIWriteEvalAt(t, filepath.Dir(overrideEval), "eval.yaml", "")
			if tc.withLink {
				if err := os.Symlink("missing", filepath.Join(parent, ".apm", "skills", "broken")); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			}

			outputPath := filepath.Join(dir, "preflight-output")
			errorPath := filepath.Join(dir, "preflight-error")
			script := `waza preflight --workspace --format json >"$OUTPUT" 2>"$ERRORS"`
			if tc.override {
				script = `waza preflight --workspace --format json --evals-dir "$OVERRIDE" >"$OUTPUT" 2>"$ERRORS"`
			}
			override := "chosen"
			if tc.absoluteOverride {
				override = filepath.ToSlash(filepath.Join(dir, override))
			}
			out, err := initCIShell(t, dir, script, map[string]string{
				"OUTPUT": filepath.ToSlash(outputPath), "ERRORS": filepath.ToSlash(errorPath), "OVERRIDE": override, "WAZA_INIT_CI_MODE": "reject",
			})
			if tc.withLink {
				require.Error(t, err, out)
				var exitErr *exec.ExitError
				require.ErrorAs(t, err, &exitErr)
				require.Equal(t, ExitError, exitErr.ExitCode(), out)
			} else {
				require.NoError(t, err, out)
			}
			output, err := os.ReadFile(outputPath)
			require.NoError(t, err)
			var reports []*preflight.Report
			require.NoError(t, json.Unmarshal(output, &reports), string(output))
			sources := make(map[string]bool)
			for _, report := range reports {
				require.NotContains(t, sources, report.Source, "each evaluation must be reported once")
				sources[report.Source] = report.Failed(false)
			}
			t.Logf("observed reports: %+v", sources)
			require.Contains(t, sources, skillEval)
			require.False(t, sources[skillEval])
			if tc.override {
				require.Contains(t, sources, overrideEval)
				require.False(t, sources[overrideEval])
				require.NotContains(t, sources, parentEval)
				require.NotContains(t, sources, requestedEval)
			} else {
				require.Contains(t, sources, parentEval)
				require.False(t, sources[parentEval])
			}
			if tc.withLink && !tc.override {
				reportedRequestedEval := requestedEval
				if tc.sameEvalsRoot {
					reportedRequestedEval = filepath.Join(parent, "evals", "workspace-extra", "eval.yaml")
					require.NotContains(t, sources, requestedEval, "the aliased root must not produce duplicate reports")
				}
				require.Contains(t, sources, reportedRequestedEval)
				require.True(t, sources[reportedRequestedEval])
				require.Len(t, reports, 3)
				var diagnosticFound bool
				for _, report := range reports {
					if report.Source == reportedRequestedEval {
						for _, diagnostic := range report.Diagnostics {
							diagnosticFound = diagnosticFound || diagnostic.Code == "eval.configuration" && diagnostic.State == preflight.Invalid
						}
					}
				}
				require.True(t, diagnosticFound, "requested eval must retain its invalid configuration diagnostic")
				human, humanErr := initCIShell(t, dir, `waza preflight --workspace`, map[string]string{"WAZA_INIT_CI_MODE": "reject"})
				require.Error(t, humanErr, human)
				var humanExit *exec.ExitError
				require.ErrorAs(t, humanErr, &humanExit)
				require.Equal(t, ExitError, humanExit.ExitCode(), human)
				require.Contains(t, human, "Offline preflight: "+skillEval)
				require.Contains(t, human, "Offline preflight: "+parentEval)
				require.Contains(t, human, "invalid eval.configuration [error] "+reportedRequestedEval)
				require.Contains(t, human, filepath.Join(parent, ".apm", "skills", "broken"))
				require.Equal(t, 3, strings.Count(human, "Offline preflight: "))
				if tc.brokenEvalLink {
					require.Contains(t, human, filepath.Join(parent, "evals", "broken"))
				}
			} else {
				require.NotContains(t, sources, requestedEval, "successful ancestor discovery and explicit overrides keep their existing roots")
				require.Len(t, reports, 2)
			}
			if tc.withLink {
				diagnostics, err := os.ReadFile(errorPath)
				require.NoError(t, err)
				require.Contains(t, string(diagnostics), filepath.Join(parent, ".apm", "skills", "broken"))
				if tc.brokenEvalLink {
					require.Contains(t, string(diagnostics), filepath.Join(parent, "evals", "broken"))
				}
			}
			require.NoFileExists(t, filepath.Join(dir, "runtime-invoked"))
		})
	}
}

func TestInitCI_WorkspaceRetainsLocalSkillsWithPartialAncestor(t *testing.T) {
	for _, tc := range []struct {
		name             string
		brokenAncestor   bool
		skillsOverride   bool
		absoluteOverride bool
	}{
		{name: "healthy ancestor"},
		{name: "partial ancestor", brokenAncestor: true},
		{name: "partial ancestor with relative skills override", brokenAncestor: true, skillsOverride: true},
		{name: "partial ancestor with absolute skills override", brokenAncestor: true, skillsOverride: true, absoluteOverride: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			dir := filepath.Join(parent, "workspace")
			require.NoError(t, os.MkdirAll(dir, 0o700))
			require.NoError(t, validateCLIIsolation(dir))
			cfg, err := projectconfig.Load(dir)
			require.NoError(t, err)
			require.Empty(t, cfg.Dir, "fixture must have no project configuration")

			ancestorSkill := filepath.Join(parent, ".apm", "skills", "reviewed")
			require.NoError(t, os.MkdirAll(ancestorSkill, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(ancestorSkill, "SKILL.md"), []byte("# reviewed\n"), 0o600))
			ancestorEval := filepath.FromSlash(initCIWriteEvalAt(t, ancestorSkill, "eval.yaml", ""))
			broken := filepath.Join(parent, ".apm", "skills", "broken")
			if tc.brokenAncestor {
				if err := os.Symlink("missing", broken); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			}

			var localEvals []string
			for _, root := range []string{"skills", "chosen"} {
				localSkill := filepath.Join(dir, root, "local")
				require.NoError(t, os.MkdirAll(localSkill, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(localSkill, "SKILL.md"), []byte("# local\n"), 0o600))
				eval := filepath.Join(localSkill, "eval.yaml")
				require.NoError(t, os.WriteFile(eval, []byte("config: [invalid]\n"), 0o600))
				localEvals = append(localEvals, eval)
			}
			requestedEval := filepath.FromSlash(initCIWriteEvalAt(t, filepath.Join(dir, "evals", "extra"), "eval.yaml", ""))

			outputPath := filepath.Join(dir, "preflight-output")
			errorPath := filepath.Join(dir, "preflight-error")
			script := `waza preflight --workspace --format json >"$OUTPUT" 2>"$ERRORS"`
			override := "chosen"
			if tc.absoluteOverride {
				override = filepath.ToSlash(filepath.Join(dir, override))
			}
			if tc.skillsOverride {
				script = `waza preflight --workspace --format json --skills-dir "$OVERRIDE" >"$OUTPUT" 2>"$ERRORS"`
			}
			out, err := initCIShell(t, dir, script, map[string]string{
				"OUTPUT": filepath.ToSlash(outputPath), "ERRORS": filepath.ToSlash(errorPath), "OVERRIDE": override, "WAZA_INIT_CI_MODE": "reject",
			})
			if tc.brokenAncestor {
				var exitErr *exec.ExitError
				require.ErrorAs(t, err, &exitErr, out)
				require.Equal(t, ExitError, exitErr.ExitCode(), out)
			} else {
				require.NoError(t, err, out)
			}
			output, err := os.ReadFile(outputPath)
			require.NoError(t, err)
			var reports []*preflight.Report
			require.NoError(t, json.Unmarshal(output, &reports), string(output))
			sources := make(map[string]*preflight.Report)
			for _, report := range reports {
				require.NotContains(t, sources, report.Source)
				sources[report.Source] = report
			}
			require.Contains(t, sources, ancestorEval)
			require.False(t, sources[ancestorEval].Failed(false))
			if tc.brokenAncestor {
				want, excluded := localEvals[0], localEvals[1]
				if tc.skillsOverride {
					want, excluded = excluded, want
				}
				require.Contains(t, sources, want, "local co-located eval must survive partial ancestor discovery")
				require.True(t, sources[want].Failed(false))
				require.NotContains(t, sources, excluded, "explicit skills overrides must retain precedence")
				require.Contains(t, sources, requestedEval)
				require.False(t, sources[requestedEval].Failed(false))
				require.Len(t, reports, 3)
				diagnostics, err := os.ReadFile(errorPath)
				require.NoError(t, err)
				require.Contains(t, string(diagnostics), broken)
				if !tc.skillsOverride {
					human, err := initCIShell(t, dir, `waza preflight --workspace`, map[string]string{"WAZA_INIT_CI_MODE": "reject"})
					require.Error(t, err, human)
					require.Contains(t, human, "invalid eval.configuration [error] "+want)
					require.Contains(t, human, broken)
				}
			} else {
				require.NotContains(t, sources, localEvals[0], "healthy ancestor precedence must remain unchanged")
				require.NotContains(t, sources, localEvals[1])
				require.NotContains(t, sources, requestedEval)
				require.Len(t, reports, 1)
			}
			require.NoFileExists(t, filepath.Join(dir, "runtime-invoked"))
		})
	}
}

func TestInitCI_WorkspaceDeduplicatesLocalSkillEvalThroughAliasedRoot(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "workspace")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, validateCLIIsolation(dir))
	ancestorSkill := filepath.Join(parent, ".apm", "skills", "reviewed")
	localSkill := filepath.Join(dir, "skills", "reviewed")
	for _, path := range []string{ancestorSkill, localSkill} {
		require.NoError(t, os.MkdirAll(path, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(path, "SKILL.md"), []byte("# reviewed\n"), 0o600))
	}
	broken := filepath.Join(parent, ".apm", "skills", "broken")
	if err := os.Symlink("missing", broken); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	evalDir := filepath.Join(dir, "evals", "reviewed")
	require.NoError(t, os.MkdirAll(evalDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(evalDir, "eval.yaml"), []byte("config: [invalid]\n"), 0o600))
	alias := filepath.Join(parent, "evals")
	if err := os.Symlink(filepath.Join("workspace", "evals"), alias); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	parentInfo, err := os.Stat(alias)
	require.NoError(t, err)
	localInfo, err := os.Stat(filepath.Join(dir, "evals"))
	require.NoError(t, err)
	require.True(t, os.SameFile(parentInfo, localInfo), "fixture roots must be the same directory")

	outputPath := filepath.Join(dir, "preflight-output")
	errorPath := filepath.Join(dir, "preflight-error")
	out, err := initCIShell(t, dir, `waza preflight --workspace --format json >"$OUTPUT" 2>"$ERRORS"`, map[string]string{
		"OUTPUT": filepath.ToSlash(outputPath), "ERRORS": filepath.ToSlash(errorPath), "WAZA_INIT_CI_MODE": "reject",
	})
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr, out)
	require.Equal(t, ExitError, exitErr.ExitCode(), out)
	output, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	var reports []*preflight.Report
	require.NoError(t, json.Unmarshal(output, &reports), string(output))
	require.Len(t, reports, 1, "one physical eval must not be reported under both root aliases")
	require.Equal(t, filepath.Join(alias, "reviewed", "eval.yaml"), reports[0].Source)
	require.True(t, reports[0].Failed(false))
	diagnostics, err := os.ReadFile(errorPath)
	require.NoError(t, err)
	require.Contains(t, string(diagnostics), broken)
}

func TestInitCI_WorkspaceLinkedManifestsUseContainingDirectories(t *testing.T) {
	for _, taskState := range []string{"valid", "invalid", "missing"} {
		t.Run(taskState, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, validateCLIIsolation(dir))
			initCIAddSkill(t, dir, "reviewed")
			manifest := filepath.FromSlash(initCIWriteEvalAt(t, dir, "shared-eval.yml", ""))
			evals := filepath.Join(dir, "evals")
			first := filepath.Join(evals, "a")
			second := filepath.Join(evals, "b")
			for _, evalDir := range []string{first, second} {
				require.NoError(t, os.MkdirAll(filepath.Join(evalDir, "fixtures"), 0o700))
				target, err := filepath.Rel(evalDir, manifest)
				require.NoError(t, err)
				if err := os.Symlink(target, filepath.Join(evalDir, "eval.yaml")); err != nil {
					t.Skipf("file symlink unavailable: %v", err)
				}
			}
			firstInfo, err := os.Stat(filepath.Join(first, "eval.yaml"))
			require.NoError(t, err)
			secondInfo, err := os.Stat(filepath.Join(second, "eval.yaml"))
			require.NoError(t, err)
			require.True(t, os.SameFile(firstInfo, secondInfo), "both manifests must resolve to the same file")
			if err := os.Symlink("a", filepath.Join(evals, "a-alias")); err != nil {
				t.Skipf("directory symlink unavailable: %v", err)
			}
			validTask := []byte("id: valid-task\nname: Task\ninputs: {prompt: ready}\n")
			require.NoError(t, os.WriteFile(filepath.Join(first, "task.yaml"), validTask, 0o600))
			switch taskState {
			case "valid":
				require.NoError(t, os.WriteFile(filepath.Join(second, "task.yaml"), validTask, 0o600))
			case "invalid":
				require.NoError(t, os.WriteFile(filepath.Join(second, "task.yaml"), []byte("id: [bad]\nname: Task\ninputs: {prompt: ready}\n"), 0o600))
			}

			outputPath := filepath.Join(dir, "preflight-output")
			errorPath := filepath.Join(dir, "preflight-error")
			out, runErr := initCIShell(t, dir, `waza preflight --workspace --format json >"$OUTPUT" 2>"$ERRORS"`, map[string]string{
				"OUTPUT": filepath.ToSlash(outputPath), "ERRORS": filepath.ToSlash(errorPath), "WAZA_INIT_CI_MODE": "reject",
			})
			if taskState == "valid" {
				require.NoError(t, runErr, out)
			} else {
				var exitErr *exec.ExitError
				require.ErrorAs(t, runErr, &exitErr, out)
				require.Equal(t, 1, exitErr.ExitCode(), out)
			}
			output, err := os.ReadFile(outputPath)
			require.NoError(t, err)
			var reports []*preflight.Report
			require.NoError(t, json.Unmarshal(output, &reports), string(output))
			require.Len(t, reports, 2, "distinct resource bases must remain separate, while directory aliases deduplicate")
			require.Equal(t, filepath.Join(first, "eval.yaml"), reports[0].Source)
			require.Equal(t, filepath.Join(second, "eval.yaml"), reports[1].Source)
			require.False(t, reports[0].Failed(false))
			require.Equal(t, taskState != "valid", reports[1].Failed(false))
			require.Len(t, reports[0].Tasks, 1)
			if taskState == "valid" {
				require.Len(t, reports[1].Tasks, 1)
			} else {
				want := "task.schema"
				if taskState == "missing" {
					want = "tasks.empty"
				}
				var found bool
				for _, diagnostic := range reports[1].Diagnostics {
					found = found || diagnostic.Code == want && diagnostic.State == preflight.Invalid
				}
				require.True(t, found, "%s", output)
			}
			require.NoFileExists(t, filepath.Join(dir, "runtime-invoked"))
		})
	}
}

func TestInitCI_WorkspaceLinkedDirectoryUsesRelativeParent(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(fmt.Sprintf("invalid=%t", invalid), func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, validateCLIIsolation(dir))
			initCIAddSkill(t, dir, "reviewed")
			skillDir := filepath.Join(dir, "skills", "reviewed")
			eval := filepath.FromSlash(initCIWriteEvalAt(t, skillDir, "eval.yaml", ""))
			spec, err := os.ReadFile(eval)
			require.NoError(t, err)
			updated := strings.Replace(string(spec), "tasks: [task.yaml]", "tasks: [../tasks/task.yaml]", 1)
			require.NotEqual(t, string(spec), updated, "fixture must use a parent-relative task")
			require.NoError(t, os.WriteFile(eval, []byte(updated), 0o600))

			evalsDir := filepath.Join(dir, "evals")
			alias := filepath.Join(evalsDir, "linked")
			require.NoError(t, os.MkdirAll(evalsDir, 0o700))
			target, err := filepath.Rel(evalsDir, skillDir)
			require.NoError(t, err)
			if err := os.Symlink(target, alias); err != nil {
				t.Skipf("directory symlink unavailable: %v", err)
			}
			actualInfo, err := os.Stat(eval)
			require.NoError(t, err)
			aliasEval := filepath.Join(alias, "eval.yaml")
			aliasInfo, err := os.Stat(aliasEval)
			require.NoError(t, err)
			require.True(t, os.SameFile(actualInfo, aliasInfo), "fixture must use the same physical manifest")

			validTask := []byte("id: valid-task\nname: Task\ninputs: {prompt: ready}\n")
			invalidTask := []byte("id: [bad]\nname: Task\ninputs: {prompt: ready}\n")
			for _, taskDir := range []string{filepath.Join(dir, "skills", "tasks"), filepath.Join(evalsDir, "tasks")} {
				require.NoError(t, os.MkdirAll(taskDir, 0o700))
				task := validTask
				if invalid && taskDir == filepath.Join(evalsDir, "tasks") {
					task = invalidTask
				}
				require.NoError(t, os.WriteFile(filepath.Join(taskDir, "task.yaml"), task, 0o600))
			}

			outputPath := filepath.Join(dir, "preflight-output")
			errorPath := filepath.Join(dir, "preflight-error")
			out, runErr := initCIShell(t, dir, `waza preflight --workspace --format json >"$OUTPUT" 2>"$ERRORS"`, map[string]string{
				"OUTPUT": filepath.ToSlash(outputPath), "ERRORS": filepath.ToSlash(errorPath), "WAZA_INIT_CI_MODE": "reject",
			})
			if invalid {
				var exitErr *exec.ExitError
				require.ErrorAs(t, runErr, &exitErr, out)
				require.Equal(t, 1, exitErr.ExitCode(), out)
			} else {
				require.NoError(t, runErr, out)
			}
			output, err := os.ReadFile(outputPath)
			require.NoError(t, err)
			var reports []*preflight.Report
			require.NoError(t, json.Unmarshal(output, &reports), string(output))
			require.Len(t, reports, 2, "relative parent paths resolve differently through the directory alias")
			require.Equal(t, eval, reports[0].Source)
			require.Equal(t, aliasEval, reports[1].Source)
			require.False(t, reports[0].Failed(false))
			require.Equal(t, invalid, reports[1].Failed(false))
			if invalid {
				var found bool
				for _, diagnostic := range reports[1].Diagnostics {
					found = found || diagnostic.Code == "task.schema" && diagnostic.State == preflight.Invalid
				}
				require.True(t, found, "%s", output)
			}
			require.NoFileExists(t, filepath.Join(dir, "runtime-invoked"))
		})
	}
}

func TestInitCI_WorkspaceLinkedDirectoryDescriptionDoesNotChangeResolution(t *testing.T) {
	for _, fixture := range []struct {
		description string
		untracked   bool
	}{
		{description: "ordinary description"},
		{description: "../../unrelated"},
		{description: "../../unrelated", untracked: true},
	} {
		for _, invalid := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/untracked=%t/invalid=%t", fixture.description, fixture.untracked, invalid), func(t *testing.T) {
				dir := t.TempDir()
				require.NoError(t, validateCLIIsolation(dir))
				initCIAddSkill(t, dir, "reviewed")
				skillDir := filepath.Join(dir, "skills", "reviewed")
				eval := filepath.FromSlash(initCIWriteEvalAt(t, skillDir, "eval.yaml", ""))
				spec, err := os.ReadFile(eval)
				require.NoError(t, err)
				spec = append(spec, []byte("description: "+fixture.description+"\n")...)
				if fixture.untracked {
					spec = append(spec, []byte("hooks:\n  before_run: [{command: ../uninspected-hook, working_directory: ../uninspected}]\n")...)
				}
				require.NoError(t, os.WriteFile(eval, spec, 0o600))

				evalsDir := filepath.Join(dir, "evals")
				require.NoError(t, os.MkdirAll(evalsDir, 0o700))
				alias := filepath.Join(evalsDir, "linked")
				target, err := filepath.Rel(evalsDir, skillDir)
				require.NoError(t, err)
				if err := os.Symlink(target, alias); err != nil {
					t.Skipf("directory symlink unavailable: %v", err)
				}
				for _, name := range []string{"eval.yaml", "task.yaml"} {
					actualInfo, err := os.Stat(filepath.Join(skillDir, name))
					require.NoError(t, err)
					aliasInfo, err := os.Stat(filepath.Join(alias, name))
					require.NoError(t, err)
					require.True(t, os.SameFile(actualInfo, aliasInfo), "%s must resolve identically through both aliases", name)
				}
				if invalid {
					require.NoError(t, os.WriteFile(filepath.Join(skillDir, "task.yaml"), []byte("id: [bad]\nname: Task\ninputs: {prompt: ready}\n"), 0o600))
				}

				outputPath := filepath.Join(dir, "preflight-output")
				errorPath := filepath.Join(dir, "preflight-error")
				out, runErr := initCIShell(t, dir, `waza preflight --workspace --format json >"$OUTPUT" 2>"$ERRORS"`, map[string]string{
					"OUTPUT": filepath.ToSlash(outputPath), "ERRORS": filepath.ToSlash(errorPath), "WAZA_INIT_CI_MODE": "reject",
				})
				if invalid {
					var exitErr *exec.ExitError
					require.ErrorAs(t, runErr, &exitErr, out)
					require.Equal(t, 1, exitErr.ExitCode(), out)
				} else {
					require.NoError(t, runErr, out)
				}
				output, err := os.ReadFile(outputPath)
				require.NoError(t, err)
				var reports []*preflight.Report
				require.NoError(t, json.Unmarshal(output, &reports), string(output))
				require.NotEmpty(t, reports)
				for _, report := range reports {
					for _, diagnostic := range report.Diagnostics {
						require.NotEqual(t, "eval.schema", diagnostic.Code, "fixture must be schema-valid: %s", output)
					}
				}
				count := 1
				if fixture.untracked {
					count = 2
				}
				require.Len(t, reports, count, "metadata must not split an identical tracked context; untracked references must retain aliases")
				require.Equal(t, eval, reports[0].Source)
				if fixture.untracked {
					require.Equal(t, filepath.Join(alias, "eval.yaml"), reports[1].Source)
					require.Equal(t, invalid, reports[1].Failed(false))
				}
				require.Equal(t, invalid, reports[0].Failed(false))
				if invalid {
					var found bool
					for _, diagnostic := range reports[0].Diagnostics {
						found = found || diagnostic.Code == "task.schema" && diagnostic.State == preflight.Invalid
					}
					require.True(t, found, "%s", output)
				} else {
					require.Len(t, reports[0].Tasks, 1)
					require.Equal(t, filepath.Join(skillDir, "task.yaml"), reports[0].Tasks[0].Source)
				}
				require.NoFileExists(t, filepath.Join(dir, "runtime-invoked"))
			})
		}
	}
}

func TestInitCI_WorkspaceLinkedDirectoryUsesTaskRelativeResources(t *testing.T) {
	for _, resource := range []string{"skill_directory", "mcp_fixture"} {
		for _, missing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/missing=%t", resource, missing), func(t *testing.T) {
				dir := t.TempDir()
				require.NoError(t, validateCLIIsolation(dir))
				initCIAddSkill(t, dir, "reviewed")
				skillDir := filepath.Join(dir, "skills", "reviewed")
				eval := filepath.FromSlash(initCIWriteEvalAt(t, skillDir, "eval.yaml", ""))
				task := "id: task\nname: Task\ninputs: {prompt: ready}\n"
				diagnosticCode := "skill.directory"
				if resource == "skill_directory" {
					task += "skill_directories: [../context-skills]\n"
				} else {
					task += "mcp_mocks:\n  - name: dependency\n    fixtures: ../responses\n"
					diagnosticCode = "mock.mcp"
				}
				require.NoError(t, os.WriteFile(filepath.Join(skillDir, "task.yaml"), []byte(task), 0o600))

				evalsDir := filepath.Join(dir, "evals")
				require.NoError(t, os.MkdirAll(evalsDir, 0o700))
				alias := filepath.Join(evalsDir, "linked")
				target, err := filepath.Rel(evalsDir, skillDir)
				require.NoError(t, err)
				if err := os.Symlink(target, alias); err != nil {
					t.Skipf("directory symlink unavailable: %v", err)
				}
				for _, name := range []string{"eval.yaml", "task.yaml"} {
					actualInfo, err := os.Stat(filepath.Join(skillDir, name))
					require.NoError(t, err)
					aliasInfo, err := os.Stat(filepath.Join(alias, name))
					require.NoError(t, err)
					require.True(t, os.SameFile(actualInfo, aliasInfo), "%s must resolve to the same physical file", name)
				}
				for i, parent := range []string{filepath.Dir(skillDir), evalsDir} {
					if missing && i == 1 {
						continue
					}
					if resource == "skill_directory" {
						require.NoError(t, os.MkdirAll(filepath.Join(parent, "context-skills"), 0o700))
					} else {
						require.NoError(t, os.MkdirAll(filepath.Join(parent, "responses"), 0o700))
						require.NoError(t, os.WriteFile(filepath.Join(parent, "responses", "read.json"), []byte(`{"responses":[{"return":"ready"}]}`), 0o600))
					}
				}

				outputPath := filepath.Join(dir, "preflight-output")
				errorPath := filepath.Join(dir, "preflight-error")
				env := map[string]string{
					"OUTPUT": filepath.ToSlash(outputPath), "ERRORS": filepath.ToSlash(errorPath), "WAZA_INIT_CI_MODE": "reject",
					"EVAL": filepath.ToSlash(filepath.Join(alias, "eval.yaml")),
				}
				for _, workspaceMode := range []bool{false, true} {
					script := `waza preflight "$EVAL" --format json >"$OUTPUT" 2>"$ERRORS"`
					if workspaceMode {
						script = `waza preflight --workspace --format json >"$OUTPUT" 2>"$ERRORS"`
					}
					out, runErr := initCIShell(t, dir, script, env)
					if missing {
						var exitErr *exec.ExitError
						require.ErrorAs(t, runErr, &exitErr, out)
						require.Equal(t, 1, exitErr.ExitCode(), out)
					} else {
						require.NoError(t, runErr, out)
					}
					output, err := os.ReadFile(outputPath)
					require.NoError(t, err)
					var report *preflight.Report
					if workspaceMode {
						var reports []*preflight.Report
						require.NoError(t, json.Unmarshal(output, &reports), string(output))
						require.Len(t, reports, 2, "task-relative resources resolve differently through the alias")
						require.Equal(t, eval, reports[0].Source)
						require.False(t, reports[0].Failed(false))
						report = reports[1]
					} else {
						require.NoError(t, json.Unmarshal(output, &report), string(output))
					}
					require.Equal(t, filepath.Join(alias, "eval.yaml"), filepath.FromSlash(report.Source))
					require.Len(t, report.Tasks, 1)
					require.Equal(t, missing, report.Failed(false))
					found := false
					for _, diagnostic := range report.Diagnostics {
						require.NotEqual(t, "eval.schema", diagnostic.Code)
						require.NotEqual(t, "task.schema", diagnostic.Code)
						found = found || diagnostic.Code == diagnosticCode && diagnostic.State == preflight.Invalid
					}
					require.Equal(t, missing, found, "%s", output)
				}
				require.NoFileExists(t, filepath.Join(dir, "runtime-invoked"))
			})
		}
	}
}

func TestInitCI_WorkspacePreflightEmptyInventoryFailsClosed(t *testing.T) {
	run := func(t *testing.T, cwd string, skillsOverride ...string) ([]*preflight.Report, []byte, int) {
		t.Helper()
		outputPath := filepath.Join(cwd, "preflight-output")
		errorPath := filepath.Join(cwd, "preflight-error")
		script := `waza preflight --workspace --format json >"$OUTPUT" 2>"$ERRORS"`
		env := map[string]string{
			"OUTPUT": filepath.ToSlash(outputPath), "ERRORS": filepath.ToSlash(errorPath), "WAZA_INIT_CI_MODE": "reject",
		}
		if len(skillsOverride) > 0 {
			require.Len(t, skillsOverride, 1)
			script = `waza preflight --workspace --format json --skills-dir "$OVERRIDE" >"$OUTPUT" 2>"$ERRORS"`
			env["OVERRIDE"] = filepath.ToSlash(skillsOverride[0])
		}
		out, err := initCIShell(t, cwd, script, env)
		code := 0
		if err != nil {
			var exitErr *exec.ExitError
			require.ErrorAs(t, err, &exitErr, out)
			code = exitErr.ExitCode()
		}
		output, err := os.ReadFile(outputPath)
		require.NoError(t, err)
		var reports []*preflight.Report
		require.NoError(t, json.Unmarshal(output, &reports), string(output))
		diagnostics, err := os.ReadFile(errorPath)
		require.NoError(t, err)
		require.NoFileExists(t, filepath.Join(cwd, "runtime-invoked"))
		return reports, diagnostics, code
	}
	assertEmpty := func(t *testing.T, cwd string, reports []*preflight.Report, diagnostics []byte, code int) {
		t.Helper()
		require.Equal(t, ExitError, code, string(diagnostics))
		require.Empty(t, reports)
		var diagnostic preflight.Diagnostic
		require.NoError(t, json.Unmarshal(diagnostics, &diagnostic), string(diagnostics))
		require.Equal(t, "workspace.empty", diagnostic.Code)
		require.Equal(t, preflight.Unresolved, diagnostic.State)
		require.Equal(t, "error", diagnostic.Severity)
		require.Equal(t, cwd, diagnostic.Source)
		require.Contains(t, diagnostic.Remediation, "project root")
		require.Contains(t, diagnostic.Remediation, "explicit")
	}

	dir := t.TempDir()
	require.NoError(t, validateCLIIsolation(dir))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte("{}\n"), 0o600))
	nested := filepath.Join(dir, "nested")
	require.NoError(t, os.MkdirAll(nested, 0o700))
	skillDir := filepath.Join(dir, ".github", "skills", "reviewed")
	require.NoError(t, os.MkdirAll(skillDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# reviewed\n"), 0o600))
	rootEval := filepath.Join(skillDir, "eval.yaml")
	require.NoError(t, os.WriteFile(rootEval, []byte("config: [invalid]\n"), 0o600))

	t.Run("root finds invalid implicit skill eval", func(t *testing.T) {
		reports, diagnostics, code := run(t, dir)
		require.Equal(t, 1, code, string(diagnostics))
		require.Len(t, reports, 1)
		require.Equal(t, rootEval, reports[0].Source)
		require.True(t, reports[0].Failed(false))
		require.Equal(t, "eval.schema", reports[0].Diagnostics[0].Code)
	})
	t.Run("nested uninspected implicit root fails closed", func(t *testing.T) {
		reports, diagnostics, code := run(t, nested)
		require.Equal(t, ExitError, code, string(diagnostics))
		require.Empty(t, reports)
		require.Contains(t, string(diagnostics), fmt.Sprintf("%q", filepath.Join(dir, ".github", "skills")))
		require.Contains(t, string(diagnostics), "project root")
	})
	t.Run("selected implicit root is inspected", func(t *testing.T) {
		for _, override := range []string{filepath.Join("..", ".github", "skills"), filepath.Join(dir, ".github", "skills")} {
			reports, diagnostics, code := run(t, nested, override)
			require.Equal(t, 1, code, string(diagnostics))
			require.Len(t, reports, 1)
			require.Equal(t, rootEval, reports[0].Source)
			require.True(t, reports[0].Failed(false))
			require.Equal(t, "eval.schema", reports[0].Diagnostics[0].Code)
			require.NotContains(t, string(diagnostics), "not inspected")
		}
	})
	t.Run("nested configured skill eval remains invalid", func(t *testing.T) {
		initCIAddSkill(t, dir, "configured")
		configuredEval := filepath.Join(dir, "skills", "configured", "eval.yaml")
		require.NoError(t, os.WriteFile(configuredEval, []byte("config: [invalid]\n"), 0o600))
		reports, diagnostics, code := run(t, nested)
		require.Equal(t, ExitError, code, string(diagnostics))
		require.Len(t, reports, 1)
		require.Equal(t, configuredEval, reports[0].Source)
		require.True(t, reports[0].Failed(false))
		require.Equal(t, "eval.schema", reports[0].Diagnostics[0].Code)
		require.Contains(t, string(diagnostics), fmt.Sprintf("%q", filepath.Join(dir, ".github", "skills")))
	})
	t.Run("valid configured skill cannot mask uninspected implicit root", func(t *testing.T) {
		configured := filepath.Join(dir, "skills", "configured")
		eval := filepath.FromSlash(initCIWriteEvalAt(t, configured, "eval.yaml", ""))
		reports, diagnostics, code := run(t, nested)
		require.Equal(t, ExitError, code, string(diagnostics))
		require.Len(t, reports, 1)
		require.Equal(t, eval, reports[0].Source)
		require.False(t, reports[0].Failed(false))
		require.Contains(t, string(diagnostics), fmt.Sprintf("%q", filepath.Join(dir, ".github", "skills")))
		require.Contains(t, string(diagnostics), "project root")
	})
	t.Run("valid admitted skill and eval pass when implicit root is absent", func(t *testing.T) {
		require.NoError(t, os.Remove(rootEval))
		require.NoError(t, os.Remove(filepath.Join(skillDir, "SKILL.md")))
		require.NoError(t, os.Remove(skillDir))
		require.NoError(t, os.Remove(filepath.Dir(skillDir)))
		configured := filepath.Join(dir, "skills", "configured")
		eval := filepath.Join(configured, "eval.yaml")
		reports, diagnostics, code := run(t, nested)
		require.Equal(t, 0, code, string(diagnostics))
		require.Len(t, reports, 1)
		require.Equal(t, eval, reports[0].Source)
		require.False(t, reports[0].Failed(false))
	})
	t.Run("empty and unrelated implicit root passes", func(t *testing.T) {
		resource := filepath.Join(dir, ".github", "skills", "resources")
		require.NoError(t, os.MkdirAll(resource, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(resource, "README.md"), []byte("not a skill\n"), 0o600))
		reports, diagnostics, code := run(t, nested)
		require.Equal(t, 0, code, string(diagnostics))
		require.Len(t, reports, 1)
		require.False(t, reports[0].Failed(false))
		require.Empty(t, diagnostics)
	})
	t.Run("admitted skill without an eval retains empty report behavior", func(t *testing.T) {
		standalone := t.TempDir()
		require.NoError(t, validateCLIIsolation(standalone))
		initCIAddSkill(t, standalone, "reviewed")
		reports, diagnostics, code := run(t, standalone)
		require.Equal(t, 0, code, string(diagnostics))
		require.Empty(t, reports)
		require.Empty(t, diagnostics)
	})
	t.Run("target-free invalid eval is not masked", func(t *testing.T) {
		standalone := t.TempDir()
		require.NoError(t, validateCLIIsolation(standalone))
		eval := filepath.Join(standalone, "evals", "example", "eval.yaml")
		require.NoError(t, os.MkdirAll(filepath.Dir(eval), 0o700))
		require.NoError(t, os.WriteFile(eval, []byte("config: [invalid]\n"), 0o600))
		reports, diagnostics, code := run(t, standalone)
		require.Equal(t, 1, code, string(diagnostics))
		require.Len(t, reports, 1)
		require.Equal(t, eval, reports[0].Source)
		require.Equal(t, "eval.schema", reports[0].Diagnostics[0].Code)
	})
	t.Run("discovery error is not replaced by empty diagnostic", func(t *testing.T) {
		standalone := t.TempDir()
		require.NoError(t, validateCLIIsolation(standalone))
		require.NoError(t, os.MkdirAll(filepath.Join(standalone, "skills"), 0o700))
		broken := filepath.Join(standalone, "skills", "broken")
		if err := os.Symlink("missing", broken); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		reports, diagnostics, code := run(t, standalone)
		require.Equal(t, ExitError, code, string(diagnostics))
		require.Empty(t, reports)
		require.Contains(t, string(diagnostics), broken)
		require.NotContains(t, string(diagnostics), "workspace.empty")
	})
	t.Run("genuinely empty workspace fails", func(t *testing.T) {
		empty := t.TempDir()
		require.NoError(t, validateCLIIsolation(empty))
		reports, diagnostics, code := run(t, empty)
		assertEmpty(t, empty, reports, diagnostics, code)
		human, err := initCIShell(t, empty, `waza preflight --workspace`, map[string]string{"WAZA_INIT_CI_MODE": "reject"})
		var exitErr *exec.ExitError
		require.ErrorAs(t, err, &exitErr, human)
		require.Equal(t, ExitError, exitErr.ExitCode(), human)
		require.Contains(t, human, "workspace.empty")
		require.Contains(t, human, empty)
		require.Contains(t, human, "project root")
	})
}

func TestInitCI_MissingEvalPreservesExistingTask(t *testing.T) {
	dir := t.TempDir()
	initCIAddSkill(t, dir, "reviewed")
	tasks := filepath.Join(dir, "evals", "reviewed", "tasks")
	require.NoError(t, os.MkdirAll(tasks, 0o755))
	task := filepath.Join(tasks, "basic-usage.yaml")
	original := []byte("existing task sentinel\r\n")
	require.NoError(t, os.WriteFile(task, original, 0o600))

	cmd := newInitCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{dir, "--no-skill"})
	require.NoError(t, cmd.Execute())
	preserved, err := os.ReadFile(task)
	require.NoError(t, err)
	require.Equal(t, original, preserved)
	require.FileExists(t, filepath.Join(dir, "evals", "reviewed", "eval.yaml"))
	require.FileExists(t, filepath.Join(dir, "evals", "reviewed", "fixtures", "sample.py"))
}

func TestInitCI_WorkspaceInventoryFollowsEvalRootSymlink(t *testing.T) {
	dir, _ := initCIFixture(t)
	cfg := projectconfig.New()
	cfg.Paths.Evals = "suites"
	config, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), config, 0o600))
	workflowPath := filepath.Join(dir, ".github", "workflows", "eval.yml")
	require.NoError(t, os.Remove(workflowPath))
	cmd := newInitCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{dir, "--no-skill"})
	require.NoError(t, cmd.Execute())
	initCIAddSkill(t, dir, "reviewed")

	suitesDir := filepath.Join(dir, "suites")
	require.NoError(t, os.Remove(filepath.Join(suitesDir, ".gitkeep")))
	require.NoError(t, os.Remove(suitesDir))
	targetDir := filepath.Join(dir, "shared-evals", "reviewed")
	require.NoError(t, os.MkdirAll(targetDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(targetDir, "eval.yaml"), []byte("config: [invalid]\n"), 0o600))
	if err := os.Symlink(filepath.Dir(targetDir), suitesDir); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}

	data, err := os.ReadFile(workflowPath)
	require.NoError(t, err)
	var workflow initCIWorkflowSpec
	require.NoError(t, yaml.Unmarshal(data, &workflow))
	step := initCIStepByName(t, workflow.Jobs["validate"], "waza preflight")
	step.Env["WAZA_INIT_CI_MODE"] = "reject"
	out, err := initCIShell(t, dir, step.Run, step.Env)
	require.Error(t, err, out)
	require.Contains(t, out, "invalid eval.schema")
	require.NotContains(t, out, "No evaluation files found")
}

func TestInitCI_SelectedExecutionResultContract(t *testing.T) {
	dir, workflow := initCIFixture(t)
	agent := workflow.Jobs["agent"]
	run := initCIStepByName(t, agent, "run-eval")
	validate := initCIStepByName(t, agent, "validate-result")
	enforce := initCIStepByName(t, agent, "VALIDATION_OUTCOME")
	require.Equal(t, "always()", validate.If)
	require.Equal(t, "always()", enforce.If)
	require.Equal(t, "${{ secrets.WAZA_COPILOT_TOKEN }}", run.Env["COPILOT_GITHUB_TOKEN"])
	for _, step := range agent.Steps {
		if strings.Contains(step.Uses, "upload-artifact@") {
			require.Equal(t, "always()", step.If)
			require.Equal(t, "error", step.With["if-no-files-found"])
			require.Equal(t, "results.json", step.With["path"])
		}
	}
	validator, err := filepath.Abs(filepath.Join("..", "..", ".github", "scripts", "validate-waza-result.sh"))
	require.NoError(t, err)
	// The exact shared consumer command, using the local copy of the
	// release-matched script rather than fetching code during offline tests.
	_, validateCommand, found := strings.Cut(validate.Run, "bash ")
	require.True(t, found)
	validateCommand = "bash " + strings.ReplaceAll(validateCommand, `"$RUNNER_TEMP/validate-waza-result.sh"`, `"$VALIDATOR"`)
	for _, tc := range []struct {
		name, mode, grader, artifact string
		token, success               bool
		executor                     string
		nonAgent, gradingFails       bool
	}{
		{name: "passing grading", token: true, success: true, grader: "graders:\n  - type: text\n    name: pass\n    config: {contains: [ready]}\n"},
		{name: "completed failing grading", token: true, gradingFails: true, grader: "graders:\n  - type: text\n    name: fail\n    config: {contains: [never-match]}\n"},
		{name: "task runtime error", token: true, mode: "task-error"},
		{name: "runtime launch rejected", token: true, mode: "reject"},
		{name: "missing authentication"},
		{name: "missing artifact", token: true, artifact: "missing"},
		{name: "malformed artifact", token: true, artifact: "malformed"},
		{name: "passing mock is not agent evidence", token: true, executor: "mock", nonAgent: true, grader: "graders:\n  - type: text\n    name: pass\n    config: {contains: [ready]}\n"},
		{name: "missing executor identity", token: true, artifact: "missing engine", nonAgent: true},
		{name: "unknown executor identity", token: true, artifact: "unknown engine", nonAgent: true},
		{name: "invalid executor identity", token: true, artifact: "invalid engine", nonAgent: true},
		{name: "inherited mock is not agent evidence", token: true, executor: "inherited mock", nonAgent: true, grader: "graders:\n  - type: text\n    name: pass\n    config: {contains: [ready]}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := initCIWriteEval(t, dir, tc.grader)
			if tc.executor != "" {
				spec, err := os.ReadFile(filepath.FromSlash(path))
				require.NoError(t, err)
				replacement := "executor: mock,"
				if tc.executor == "inherited mock" {
					replacement = ""
					require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte("defaults: {engine: mock, model: never-call}\n"), 0o600))
				}
				require.NoError(t, os.WriteFile(filepath.FromSlash(path), []byte(strings.ReplaceAll(string(spec), "executor: copilot-sdk,", replacement)), 0o600))
			}
			output := filepath.Join(dir, "github-output")
			require.NoError(t, os.WriteFile(output, nil, 0o600))
			token := ""
			if tc.token {
				token = "synthetic-offline-token"
			}
			out, runErr := initCIShell(t, dir, run.Run, map[string]string{
				"EVAL_PATH": path, "COPILOT_GITHUB_TOKEN": token,
				"GITHUB_OUTPUT": filepath.ToSlash(output), "WAZA_INIT_CI_MODE": tc.mode,
			})
			if tc.token {
				require.NoError(t, runErr, out)
			} else {
				require.Error(t, runErr, out)
				require.Contains(t, out, "Configure WAZA_COPILOT_TOKEN")
			}
			record, err := os.ReadFile(output)
			require.NoError(t, err)
			runExit := strings.TrimSpace(strings.TrimPrefix(string(record), "exit_code="))
			if tc.token && tc.mode == "" && !tc.gradingFails {
				require.Equal(t, "0", runExit, out)
			}
			if tc.grader != "" || tc.mode == "task-error" {
				data, err := os.ReadFile(filepath.Join(dir, "results.json"))
				require.NoError(t, err)
				var result struct {
					Summary struct{ Failed, Errors int }
				}
				require.NoError(t, json.Unmarshal(data, &result))
				if tc.mode == "task-error" {
					require.Equal(t, 1, result.Summary.Errors)
				} else {
					require.Zero(t, result.Summary.Errors, string(data))
					if tc.gradingFails {
						require.Equal(t, 1, result.Summary.Failed)
					} else {
						require.Zero(t, result.Summary.Failed)
					}
				}
			}
			if tc.artifact == "missing" {
				require.NoError(t, os.Remove(filepath.Join(dir, "results.json")))
			} else if tc.artifact == "malformed" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "results.json"), []byte("{"), 0o600))
			} else if tc.artifact != "" {
				data, err := os.ReadFile(filepath.Join(dir, "results.json"))
				require.NoError(t, err)
				var result map[string]any
				require.NoError(t, json.Unmarshal(data, &result))
				config, ok := result["config"].(map[string]any)
				require.True(t, ok)
				switch tc.artifact {
				case "missing engine":
					delete(config, "engine_type")
				case "unknown engine":
					config["engine_type"] = "unknown"
				case "invalid engine":
					config["engine_type"] = map[string]string{"type": "copilot-sdk"}
				default:
					t.Fatalf("unhandled artifact case %q", tc.artifact)
				}
				data, err = json.Marshal(result)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(dir, "results.json"), data, 0o600))
			}
			require.NoError(t, os.WriteFile(output, nil, 0o600))
			out, validationErr := initCIShell(t, dir, validateCommand, map[string]string{
				"VALIDATOR": filepath.ToSlash(validator), "RUN_EXIT_CODE": runExit, "GITHUB_OUTPUT": filepath.ToSlash(output),
			})
			record, err = os.ReadFile(output)
			require.NoError(t, err)
			if tc.success {
				require.NoError(t, validationErr, out)
				require.Contains(t, string(record), "status=success")
			} else if tc.nonAgent {
				require.Error(t, validationErr, out)
				require.Contains(t, out, "cannot satisfy the agent-evidence gate")
				require.FileExists(t, filepath.Join(dir, "results.json"))
			} else if tc.gradingFails || tc.mode == "task-error" {
				require.NoError(t, validationErr, out)
				require.Contains(t, string(record), "status=failure")
				require.NotEqual(t, "0", runExit)
				require.FileExists(t, filepath.Join(dir, "results.json"))
			} else {
				require.Error(t, validationErr, out)
			}
			outcome, status := "failure", "failure"
			if validationErr == nil {
				outcome = "success"
			}
			if strings.Contains(string(record), "status=success") {
				status = "success"
			}
			out, err = initCIShell(t, dir, enforce.Run, map[string]string{
				"VALIDATION_OUTCOME": outcome, "EVALUATION_STATUS": status,
			})
			require.Equal(t, tc.success, err == nil, out)
		})
	}
}

func TestInitCI_CustomEvalPaths(t *testing.T) {
	dir, _ := initCIFixture(t)
	configPath := filepath.Join(dir, ".waza.yaml")
	cfg := projectconfig.New()
	cfg.Paths.Evals = "configured suites"
	cfg.Files.EvalFile = "suite.yml"
	config, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, config, 0o600))
	workflowPath := filepath.Join(dir, ".github", "workflows", "eval.yml")
	require.NoError(t, os.Remove(workflowPath))
	cmd := newInitCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{dir, "--no-skill", "--evals-dir", "generated suites"})
	require.ErrorContains(t, cmd.Execute(), `--evals-dir "generated suites" conflicts with .waza.yaml paths.evals "configured suites"`)
	require.NoFileExists(t, workflowPath)
	require.NoDirExists(t, filepath.Join(dir, "generated suites"))

	cmd = newInitCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{dir, "--no-skill", "--evals-dir", "configured suites"})
	require.NoError(t, cmd.Execute())
	initCIAddSkill(t, dir, "reviewed")
	data, err := os.ReadFile(workflowPath)
	require.NoError(t, err)
	var workflow initCIWorkflowSpec
	require.NoError(t, yaml.Unmarshal(data, &workflow))
	require.Equal(t, "configured suites/my-skill/suite.yml", workflow.On.Dispatch.Inputs["eval-path"].Default)
	step := initCIStepByName(t, workflow.Jobs["validate"], "waza preflight")
	require.Equal(t, "configured suites", step.Env["EVALS_PATH"])
	require.Equal(t, "suite.yml", step.Env["EVAL_FILE"])
	evalDir := filepath.Join(dir, "configured suites", "reviewed")
	require.NoError(t, os.MkdirAll(evalDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(evalDir, "suite.yml"), []byte("config: [invalid]\n"), 0o600))
	step.Env["WAZA_INIT_CI_MODE"] = "reject"
	out, err := initCIShell(t, dir, step.Run, step.Env)
	require.Error(t, err, out)
	require.NotContains(t, out, "No evaluation files found")
	require.NoFileExists(t, filepath.Join(dir, "runtime-invoked"))
}

func TestInitCI_ExistingConfigSelectsPathsOverDetection(t *testing.T) {
	for _, suffix := range []string{"/", ""} {
		name := "without trailing slash"
		if suffix != "" {
			name = "with trailing slash"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, validateCLIIsolation(dir))
			skillsPath, evalsPath := "skills"+suffix, "evals"+suffix
			configPath := filepath.Join(dir, ".waza.yaml")
			config := []byte(fmt.Sprintf("paths:\n  skills: %s\n  evals: %s\n", skillsPath, evalsPath))
			require.NoError(t, os.WriteFile(configPath, config, 0o600))
			initCIAddSkill(t, dir, "configured")
			decoySkill := filepath.Join(dir, "a-skills", "decoy")
			require.NoError(t, os.MkdirAll(decoySkill, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(decoySkill, "SKILL.md"), []byte("# decoy\n"), 0o600))
			configuredEval := filepath.Join(dir, "evals", "configured", "eval.yaml")
			decoyEval := filepath.Join(dir, "a-evals", "decoy", "eval.yaml")
			for _, path := range []string{configuredEval, decoyEval} {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, []byte("config: [invalid]\n"), 0o600))
			}

			var initOut bytes.Buffer
			cmd := newInitCommand()
			cmd.SetOut(&initOut)
			cmd.SetArgs([]string{dir, "--no-skill"})
			require.NoError(t, cmd.Execute())
			require.Contains(t, initOut.String(), "Skill: configured")
			require.NotContains(t, initOut.String(), "Skill: decoy")
			preserved, err := os.ReadFile(configPath)
			require.NoError(t, err)
			require.Equal(t, config, preserved)

			data, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", "eval.yml"))
			require.NoError(t, err)
			var workflow initCIWorkflowSpec
			require.NoError(t, yaml.Unmarshal(data, &workflow))
			step := initCIStepByName(t, workflow.Jobs["validate"], "waza preflight")
			require.Equal(t, skillsPath, step.Env["SKILLS_PATH"])
			require.Equal(t, evalsPath, step.Env["EVALS_PATH"])
			require.Equal(t, "evals/my-skill/eval.yaml", workflow.On.Dispatch.Inputs["eval-path"].Default)

			ordinary, err := initCIShell(t, dir, "waza preflight --workspace", map[string]string{"WAZA_INIT_CI_MODE": "reject"})
			require.Error(t, err, ordinary)
			require.Contains(t, ordinary, configuredEval)
			require.NotContains(t, ordinary, decoyEval)
			step.Env["WAZA_INIT_CI_MODE"] = "reject"
			generated, err := initCIShell(t, dir, step.Run, step.Env)
			require.Error(t, err, generated)
			require.Contains(t, generated, configuredEval)
			require.NotContains(t, generated, decoyEval)
			require.NoFileExists(t, filepath.Join(dir, "runtime-invoked"))
		})
	}
}

func TestInitCI_ExistingWorkflowRetainsDetection(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, validateCLIIsolation(dir))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte("paths:\n  skills: skills/\n  evals: evals/\n"), 0o600))
	skillDir := filepath.Join(dir, "a-skills", "reviewed")
	evalDir := filepath.Join(dir, "a-evals", "reviewed")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.MkdirAll(evalDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# reviewed\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(evalDir, "eval.yaml"), []byte("config: [invalid]\n"), 0o600))
	workflowPath := filepath.Join(dir, ".github", "workflows", "eval.yml")
	require.NoError(t, os.MkdirAll(filepath.Dir(workflowPath), 0o755))
	original := []byte("existing workflow sentinel\n")
	require.NoError(t, os.WriteFile(workflowPath, original, 0o600))

	var out bytes.Buffer
	cmd := newInitCommand()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{dir, "--no-skill"})
	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), "Skill: reviewed")
	require.Contains(t, out.String(), "a-skills")
	require.Contains(t, out.String(), "a-evals")
	preserved, err := os.ReadFile(workflowPath)
	require.NoError(t, err)
	require.Equal(t, original, preserved)
}

func TestInitCI_ExistingWorkflowAllowsLiteralExpressions(t *testing.T) {
	const expression = "${{ 1 }}"
	for _, tc := range []struct {
		name        string
		config      string
		args        []string
		createdPath string
	}{
		{
			name:        "configured skills directory",
			config:      "paths:\n  skills: 'skills-" + expression + "'\n",
			createdPath: "skills-" + expression,
		},
		{
			name:        "configured eval directory",
			config:      "paths:\n  evals: 'suites-" + expression + "'\n",
			createdPath: filepath.Join("suites-"+expression, ".gitkeep"),
		},
		{
			name:        "configured eval filename",
			config:      "files:\n  evalFile: '" + expression + ".yml'\n",
			createdPath: filepath.Join("evals", ".gitkeep"),
		},
		{
			name:        "eval directory flag",
			args:        []string{"--evals-dir", "suites-" + expression},
			createdPath: filepath.Join("suites-"+expression, ".gitkeep"),
		},
	} {
		for _, existing := range []bool{true, false} {
			name := tc.name + "/missing workflow"
			if existing {
				name = tc.name + "/existing workflow"
			}
			t.Run(name, func(t *testing.T) {
				t.Setenv("WAZA_NO_UPDATE_CHECK", "1")
				dir := t.TempDir()
				require.NoError(t, validateCLIIsolation(dir))
				configPath := filepath.Join(dir, ".waza.yaml")
				if tc.config != "" {
					require.NoError(t, os.WriteFile(configPath, []byte(tc.config), 0o600))
				}
				workflowPath := filepath.Join(dir, ".github", "workflows", "eval.yml")
				original := []byte("existing workflow sentinel\r\n")
				if existing {
					require.NoError(t, os.MkdirAll(filepath.Dir(workflowPath), 0o755))
					require.NoError(t, os.WriteFile(workflowPath, original, 0o600))
				}

				var diagnostics bytes.Buffer
				args := append([]string{"--no-telemetry", "init", dir, "--no-skill"}, tc.args...)
				code := initCIRunMain(t, &diagnostics, args)
				if existing {
					require.Equal(t, ExitSuccess, code, diagnostics.String())
					preserved, err := os.ReadFile(workflowPath)
					require.NoError(t, err)
					require.Equal(t, original, preserved)
					require.FileExists(t, filepath.Join(dir, "README.md"))
					_, err = os.Stat(filepath.Join(dir, tc.createdPath))
					require.NoError(t, err, "init must repair the project using the literal path")
				} else {
					require.Equal(t, ExitError, code, diagnostics.String())
					require.Contains(t, diagnostics.String(), "GitHub Actions would evaluate")
					require.NoFileExists(t, workflowPath)
					require.NoDirExists(t, filepath.Join(dir, "skills"))
					require.NoDirExists(t, filepath.Join(dir, "evals"))
					require.NoFileExists(t, filepath.Join(dir, "README.md"))
				}
				if tc.config != "" {
					preserved, err := os.ReadFile(configPath)
					require.NoError(t, err)
					require.Equal(t, tc.config, string(preserved))
				}
			})
		}
	}
}

func TestInitCI_StandaloneRepairAllowsExistingWorkflowWithLiteralEvalFile(t *testing.T) {
	for _, tc := range []struct {
		name        string
		workflow    string
		evalFile    string
		wantSuccess bool
		wantError   string
	}{
		{name: "existing workflow", workflow: "file", evalFile: "${{ github.token }}.yml", wantSuccess: true},
		{name: "existing workflow symlink", workflow: "symlink", evalFile: "${{ github.token }}.yml", wantSuccess: true},
		{name: "missing workflow", evalFile: "${{ github.token }}.yml", wantError: "GitHub Actions would evaluate"},
		{name: "dangling workflow symlink", workflow: "dangling", evalFile: "${{ github.token }}.yml", wantError: "cannot be resolved"},
		{name: "dangling workflow symlink with safe filename", workflow: "dangling", evalFile: "suite.yml", wantError: "cannot be resolved"},
		{name: "workflow is a directory", workflow: "directory", evalFile: "${{ github.token }}.yml", wantError: "exists but is a directory"},
		{name: "missing workflow with safe filename", evalFile: "suite.yml", wantSuccess: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("WAZA_NO_UPDATE_CHECK", "1")
			dir := t.TempDir()
			require.NoError(t, validateCLIIsolation(dir))
			configPath := filepath.Join(dir, ".waza.yaml")
			config := []byte("files:\n  evalFile: '" + tc.evalFile + "'\n")
			require.NoError(t, os.WriteFile(configPath, config, 0o600))
			root := filepath.Join(dir, "reviewed")
			require.NoError(t, os.MkdirAll(root, 0o700))
			skillPath := filepath.Join(root, "SKILL.md")
			skill := []byte(defaultSkillMD("reviewed") + "\nUser-owned sentinel.\n")
			require.NoError(t, os.WriteFile(skillPath, skill, 0o600))
			workflowPath := filepath.Join(root, ".github", "workflows", "eval.yml")
			workflow := []byte("existing workflow sentinel\r\n")
			workflowTarget := filepath.Join(root, "workflow-target.yml")
			var originalLink os.FileInfo
			var originalLinkTarget string
			if tc.workflow != "" {
				require.NoError(t, os.MkdirAll(filepath.Dir(workflowPath), 0o700))
			}
			switch tc.workflow {
			case "file":
				require.NoError(t, os.WriteFile(workflowPath, workflow, 0o600))
			case "symlink", "dangling":
				if tc.workflow == "symlink" {
					require.NoError(t, os.WriteFile(workflowTarget, workflow, 0o600))
				}
				target, err := filepath.Rel(filepath.Dir(workflowPath), workflowTarget)
				require.NoError(t, err)
				if err := os.Symlink(target, workflowPath); err != nil {
					t.Skipf("workflow symlink unavailable: %v", err)
				}
				originalLink, err = os.Lstat(workflowPath)
				require.NoError(t, err)
				originalLinkTarget, err = os.Readlink(workflowPath)
				require.NoError(t, err)
			case "directory":
				require.NoError(t, os.Mkdir(workflowPath, 0o700))
			}

			var diagnostics bytes.Buffer
			code := initCIRunMain(t, &diagnostics, []string{"--no-telemetry", "new", "skill", "reviewed", "--output-dir", dir})
			if tc.wantSuccess {
				require.Equal(t, ExitSuccess, code, diagnostics.String())
				require.FileExists(t, filepath.Join(root, "evals", tc.evalFile))
				require.FileExists(t, filepath.Join(root, "evals", "tasks", "basic-usage.yaml"))
				require.FileExists(t, filepath.Join(root, "README.md"))
				if tc.workflow == "file" || tc.workflow == "symlink" {
					preserved, err := os.ReadFile(workflowPath)
					require.NoError(t, err)
					require.Equal(t, workflow, preserved)
				} else {
					require.FileExists(t, workflowPath)
				}
			} else {
				require.Equal(t, ExitError, code, diagnostics.String())
				require.Contains(t, diagnostics.String(), tc.wantError)
				require.NoDirExists(t, filepath.Join(root, "evals"))
				require.NoFileExists(t, filepath.Join(root, "README.md"))
				if tc.workflow == "" {
					require.NoDirExists(t, filepath.Join(root, ".github"))
				}
			}
			if tc.workflow == "symlink" || tc.workflow == "dangling" {
				info, err := os.Lstat(workflowPath)
				require.NoError(t, err)
				require.NotZero(t, info.Mode()&os.ModeSymlink)
				require.True(t, os.SameFile(originalLink, info), "the existing link must not be replaced")
				target, err := os.Readlink(workflowPath)
				require.NoError(t, err)
				require.Equal(t, originalLinkTarget, target)
				if tc.workflow == "dangling" {
					require.NoFileExists(t, workflowTarget)
				}
			}
			preservedSkill, err := os.ReadFile(skillPath)
			require.NoError(t, err)
			require.Equal(t, skill, preservedSkill)
			preservedConfig, err := os.ReadFile(configPath)
			require.NoError(t, err)
			require.Equal(t, config, preservedConfig)
		})
	}
}

func TestInitCI_DanglingExistingWorkflowDoesNotSucceed(t *testing.T) {
	t.Setenv("WAZA_NO_UPDATE_CHECK", "1")
	dir := t.TempDir()
	require.NoError(t, validateCLIIsolation(dir))
	workflowPath := filepath.Join(dir, ".github", "workflows", "eval.yml")
	require.NoError(t, os.MkdirAll(filepath.Dir(workflowPath), 0o755))
	target := filepath.Join(dir, "missing-workflow.yml")
	if err := os.Symlink(target, workflowPath); err != nil {
		t.Skipf("file symlink unavailable: %v", err)
	}

	var diagnostics bytes.Buffer
	code := initCIRunMain(t, &diagnostics, []string{"--no-telemetry", "init", dir, "--no-skill"})
	require.Equal(t, ExitError, code, diagnostics.String())
	require.NoFileExists(t, target)
	info, err := os.Lstat(workflowPath)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink)
}

func TestInitCI_RejectsActionsExpressionsInLiteralPaths(t *testing.T) {
	const expression = "suites-${{ github.token }}"
	t.Run("init evals dir", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, validateCLIIsolation(dir))
		cmd := newInitCommand()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SilenceUsage = true
		cmd.SetArgs([]string{dir, "--no-skill", "--evals-dir", expression})
		err := cmd.Execute()
		require.ErrorContains(t, err, `generated CI cannot use path "`+expression+`"`)
		require.NoFileExists(t, filepath.Join(dir, ".github", "workflows", "eval.yml"))
		require.NoDirExists(t, filepath.Join(dir, expression))
	})
	t.Run("init rejection preserves existing missing-eval task", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, validateCLIIsolation(dir))
		initCIAddSkill(t, dir, "reviewed")
		tasks := filepath.Join(dir, expression, "reviewed", "tasks")
		require.NoError(t, os.MkdirAll(tasks, 0o755))
		task := filepath.Join(tasks, "basic-usage.yaml")
		original := []byte("existing task sentinel\r\n")
		require.NoError(t, os.WriteFile(task, original, 0o600))

		cmd := newInitCommand()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SilenceUsage = true
		cmd.SetArgs([]string{dir, "--no-skill", "--evals-dir", expression})
		require.ErrorContains(t, cmd.Execute(), "GitHub Actions would evaluate")
		preserved, err := os.ReadFile(task)
		require.NoError(t, err)
		require.Equal(t, original, preserved)
		require.NoFileExists(t, filepath.Join(dir, expression, "reviewed", "eval.yaml"))
		require.NoDirExists(t, filepath.Join(dir, expression, "reviewed", "fixtures"))
		require.NoFileExists(t, filepath.Join(dir, ".github", "workflows", "eval.yml"))
	})
	t.Run("init rejection does not create target directory", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "not-created")
		cmd := newInitCommand()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SilenceUsage = true
		cmd.SetArgs([]string{dir, "--no-skill", "--evals-dir", expression})
		require.ErrorContains(t, cmd.Execute(), "GitHub Actions would evaluate")
		require.NoDirExists(t, dir)
	})
	t.Run("new skill eval file", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, validateCLIIsolation(dir))
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte("files:\n  evalFile: '${{ github.token }}.yml'\n"), 0o600))
		cmd := newNewSkillCommand()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SilenceUsage = true
		cmd.SetArgs([]string{"reviewed", "--output-dir", dir})
		err := cmd.Execute()
		require.ErrorContains(t, err, "GitHub Actions would evaluate")
		require.NoFileExists(t, filepath.Join(dir, "reviewed", ".github", "workflows", "eval.yml"))
		require.NoFileExists(t, filepath.Join(dir, "reviewed", "SKILL.md"))
	})
	t.Run("standalone repair preserves malformed skill", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, validateCLIIsolation(dir))
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte("files:\n  evalFile: '${{ github.token }}.yml'\n"), 0o600))
		root := filepath.Join(dir, "reviewed")
		require.NoError(t, os.MkdirAll(root, 0o700))
		skillPath := filepath.Join(root, "SKILL.md")
		original := []byte("User-owned content without frontmatter.\r\nKeep these original bytes.\n")
		require.NoError(t, os.WriteFile(skillPath, original, 0o600))
		cmd := newNewSkillCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetIn(strings.NewReader(""))
		cmd.SilenceUsage = true
		cmd.SetArgs([]string{"reviewed", "--output-dir", dir})
		require.ErrorContains(t, cmd.Execute(), "GitHub Actions would evaluate")
		require.Contains(t, out.String(), "empty or malformed")
		preserved, err := os.ReadFile(skillPath)
		require.NoError(t, err, "rejected configuration must not delete the original skill")
		require.Equal(t, original, preserved)
		require.NoDirExists(t, filepath.Join(root, "evals"))
		require.NoDirExists(t, filepath.Join(root, ".github"))

		require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte("files:\n  evalFile: suite.yml\n"), 0o600))
		require.NoError(t, cmd.Execute())
		repaired, err := os.ReadFile(skillPath)
		require.NoError(t, err)
		require.Contains(t, string(repaired), "name: reviewed")
		require.FileExists(t, filepath.Join(root, "evals", "suite.yml"))
		require.FileExists(t, filepath.Join(root, ".github", "workflows", "eval.yml"))
	})
	t.Run("standalone repair keeps existing workflow", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, validateCLIIsolation(dir))
		evalFile := "${{ github.token }}.yml"
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte("files:\n  evalFile: '"+evalFile+"'\n"), 0o600))
		root := filepath.Join(dir, "reviewed")
		workflowPath := filepath.Join(root, ".github", "workflows", "eval.yml")
		require.NoError(t, os.MkdirAll(filepath.Dir(workflowPath), 0o700))
		workflow := []byte("name: user-owned workflow\n")
		require.NoError(t, os.WriteFile(workflowPath, workflow, 0o600))
		skillPath := filepath.Join(root, "SKILL.md")
		require.NoError(t, os.WriteFile(skillPath, []byte("User-owned content without frontmatter.\n"), 0o600))
		cmd := newNewSkillCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetIn(strings.NewReader(""))
		cmd.SilenceUsage = true
		cmd.SetArgs([]string{"reviewed", "--output-dir", dir})
		require.NoError(t, cmd.Execute(), "an existing workflow is preserved, so its configuration must not block repair")
		repaired, err := os.ReadFile(skillPath)
		require.NoError(t, err)
		require.Contains(t, string(repaired), "name: reviewed")
		require.FileExists(t, filepath.Join(root, "evals", evalFile))
		require.FileExists(t, filepath.Join(root, "README.md"))
		preserved, err := os.ReadFile(workflowPath)
		require.NoError(t, err)
		require.Equal(t, workflow, preserved)
	})
	t.Run("standalone repair rejects workflow directory before mutation", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, validateCLIIsolation(dir))
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte("files:\n  evalFile: '${{ github.token }}.yml'\n"), 0o600))
		root := filepath.Join(dir, "reviewed")
		workflowPath := filepath.Join(root, ".github", "workflows", "eval.yml")
		require.NoError(t, os.MkdirAll(workflowPath, 0o700))
		skillPath := filepath.Join(root, "SKILL.md")
		original := []byte("User-owned content without frontmatter.\nKeep these original bytes.\n")
		require.NoError(t, os.WriteFile(skillPath, original, 0o600))
		cmd := newNewSkillCommand()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetIn(strings.NewReader(""))
		cmd.SilenceUsage = true
		cmd.SetArgs([]string{"reviewed", "--output-dir", dir})
		require.ErrorContains(t, cmd.Execute(), "exists but is a directory")
		preserved, err := os.ReadFile(skillPath)
		require.NoError(t, err)
		require.Equal(t, original, preserved)
		require.NoDirExists(t, filepath.Join(root, "evals"))
	})
	t.Run("standalone repair rejects dangling workflow before mutation", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, validateCLIIsolation(dir))
		root := filepath.Join(dir, "reviewed")
		workflowPath := filepath.Join(root, ".github", "workflows", "eval.yml")
		require.NoError(t, os.MkdirAll(filepath.Dir(workflowPath), 0o700))
		target := filepath.Join(root, "missing-workflow.yml")
		if err := os.Symlink(target, workflowPath); err != nil {
			t.Skipf("file symlink unavailable: %v", err)
		}
		skillPath := filepath.Join(root, "SKILL.md")
		original := []byte("User-owned content without frontmatter.\nKeep these original bytes.\n")
		require.NoError(t, os.WriteFile(skillPath, original, 0o600))
		cmd := newNewSkillCommand()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetIn(strings.NewReader(""))
		cmd.SilenceUsage = true
		cmd.SetArgs([]string{"reviewed", "--output-dir", dir})
		require.ErrorContains(t, cmd.Execute(), "dangling symlink")
		preserved, err := os.ReadFile(skillPath)
		require.NoError(t, err)
		require.Equal(t, original, preserved)
		require.NoDirExists(t, filepath.Join(root, "evals"))
		require.NoFileExists(t, target)
		info, err := os.Lstat(workflowPath)
		require.NoError(t, err)
		require.NotZero(t, info.Mode()&os.ModeSymlink)
	})
}

func TestInitCI_StandaloneRepairRequiresPreservedWorkflow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".github", "workflows", "eval.yml")
	entries := []scaffold.FileEntry{{Path: path, Label: "CI pipeline"}}
	cmd := newNewSkillCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)

	err := writeScaffold(cmd, entries, "reviewed", true, filepath.Join(dir, "tasks"), filepath.Join(dir, "fixtures"), path)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.Contains(t, err.Error(), "checking existing workflow")
	require.Empty(t, out.String(), "a missing preserved workflow must not produce a success summary")

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	workflow := []byte("existing workflow\n")
	require.NoError(t, os.WriteFile(path, workflow, 0o600))
	require.NoError(t, writeScaffold(cmd, entries, "reviewed", true, filepath.Join(dir, "tasks"), filepath.Join(dir, "fixtures"), path))
	preserved, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, workflow, preserved)
}

func TestInitCI_InventoryUsesLiteralFilenameAndFallbackPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name             string
		evalFile         string
		configured       string
		legacy           string
		globDecoy        string
		wantPreflightErr bool
	}{
		{
			name:             "legacy fallback when configured file is absent",
			evalFile:         "suite.yml",
			legacy:           "config: [invalid]\n",
			wantPreflightErr: true,
		},
		{
			name:             "metacharacters are literal in configured filename",
			evalFile:         "suite[1].yml",
			configured:       "config: [invalid]\n",
			globDecoy:        "valid",
			wantPreflightErr: true,
		},
		{
			name:       "configured file takes precedence over legacy fallback",
			evalFile:   "suite.yml",
			configured: "valid",
			legacy:     "config: [invalid]\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, _ := initCIFixture(t)
			cfg := projectconfig.New()
			cfg.Paths.Evals = "custom suites"
			cfg.Files.EvalFile = tc.evalFile
			config, err := yaml.Marshal(cfg)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), config, 0o600))
			workflowPath := filepath.Join(dir, ".github", "workflows", "eval.yml")
			require.NoError(t, os.Remove(workflowPath))
			cmd := newInitCommand()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetArgs([]string{dir, "--no-skill"})
			require.NoError(t, cmd.Execute())
			initCIAddSkill(t, dir, "reviewed")
			data, err := os.ReadFile(workflowPath)
			require.NoError(t, err)
			var workflow initCIWorkflowSpec
			require.NoError(t, yaml.Unmarshal(data, &workflow))
			step := initCIStepByName(t, workflow.Jobs["validate"], "waza preflight")
			step.Env["WAZA_INIT_CI_MODE"] = "reject"

			evalDir := filepath.Join(dir, "custom suites", "reviewed")
			if tc.configured == "valid" {
				initCIWriteEvalAt(t, evalDir, tc.evalFile, "")
			} else if tc.configured != "" {
				require.NoError(t, os.MkdirAll(evalDir, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(evalDir, tc.evalFile), []byte(tc.configured), 0o600))
			}
			if tc.legacy != "" {
				require.NoError(t, os.MkdirAll(evalDir, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(evalDir, "eval.yaml"), []byte(tc.legacy), 0o600))
			}
			if tc.globDecoy == "valid" {
				initCIWriteEvalAt(t, evalDir, "suite1.yml", "")
			}

			out, err := initCIShell(t, dir, step.Run, step.Env)
			require.Equal(t, tc.wantPreflightErr, err != nil, out)
			require.NotContains(t, out, "No evaluation files found")
			require.NoFileExists(t, filepath.Join(dir, "runtime-invoked"))
		})
	}
}

func TestInitCI_StandaloneGeneratedPaths(t *testing.T) {
	for _, evalFile := range []string{"eval.yaml", "suite.yml"} {
		t.Run(evalFile, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, validateCLIIsolation(dir), "standalone fixture must not inherit repository configuration")
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte("files:\n  evalFile: "+evalFile+"\n"), 0o600))
			cmd := newNewSkillCommand()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetArgs([]string{"reviewed", "--output-dir", dir})
			require.NoError(t, cmd.Execute())
			root := filepath.Join(dir, "reviewed")
			workflowPath := filepath.Join(root, ".github", "workflows", "eval.yml")
			data, err := os.ReadFile(workflowPath)
			require.NoError(t, err)
			require.NotContains(t, string(data), "/main/")
			var workflow initCIWorkflowSpec
			require.NoError(t, yaml.Unmarshal(data, &workflow))
			require.Equal(t, filepath.ToSlash(filepath.Join("evals", evalFile)), workflow.On.Dispatch.Inputs["eval-path"].Default)
			require.Equal(t, false, workflow.On.Dispatch.Inputs["run-agent"].Default)
			require.Contains(t, workflow.Jobs["agent"].If, "github.event.repository.default_branch")
			require.Equal(t, "waza-agent", workflow.Jobs["agent"].Environment)
			require.NotContains(t, workflow.On.PullRequest, "branches")
			readme, err := os.ReadFile(filepath.Join(root, "README.md"))
			require.NoError(t, err)
			require.Contains(t, string(readme), "checksum-verified standalone Waza "+workflow.Env["WAZA_VERSION"])
			require.NoError(t, os.Remove(filepath.Join(dir, ".waza.yaml")), "standalone checkout must not inherit parent configuration")
			require.NoError(t, validateCLIIsolation(root))
			// Preflight and run must both resolve the standalone skill for required_skills.
			evalPath := filepath.Join(root, "evals", evalFile)
			evalData, err := os.ReadFile(evalPath)
			require.NoError(t, err)
			required := strings.Replace(string(evalData), "config:\n", "config:\n  required_skills: [reviewed]\n", 1)
			require.NotEqual(t, string(evalData), required, "fixture must declare required_skills")
			require.NoError(t, os.WriteFile(evalPath, []byte(required), 0o600))
			step := initCIStepByName(t, workflow.Jobs["validate"], "waza preflight")
			step.Env["WAZA_INIT_CI_MODE"] = "reject"
			out, err := initCIShell(t, root, step.Run, step.Env)
			require.NoError(t, err, out)
			require.Contains(t, out, "Offline preflight: "+filepath.Join(root, "evals", evalFile))
			require.NotContains(t, out, "No evaluation files found")
			require.NoFileExists(t, filepath.Join(root, "runtime-invoked"))
			require.NoFileExists(t, filepath.Join(root, "results.json"))
			// Explicit single-file preflight from the checkout must agree with run.
			t.Chdir(root)
			single := newPreflightCommand()
			single.SetOut(&bytes.Buffer{})
			single.SilenceUsage = true
			single.SetArgs([]string{filepath.Join("evals", evalFile)})
			require.NoError(t, single.Execute())

			// The trusted job must load the skill under test without a .waza.yaml.
			run := initCIStepByName(t, workflow.Jobs["agent"], "waza run")
			output := filepath.Join(root, "github-output")
			require.NoError(t, os.WriteFile(output, nil, 0o600))
			out, err = initCIShell(t, root, run.Run, map[string]string{
				"EVAL_PATH": filepath.ToSlash(filepath.Join("evals", evalFile)), "COPILOT_GITHUB_TOKEN": "synthetic-offline-token",
				"GITHUB_OUTPUT": filepath.ToSlash(output), "WAZA_INIT_CI_MODE": "record-skills",
			})
			require.NoError(t, err, out)
			require.FileExists(t, filepath.Join(root, "results.json"), out)
			sessions, err := os.ReadFile(filepath.Join(root, "session-skill-paths"))
			require.NoError(t, err, out)
			wantRoot, err := filepath.EvalSymlinks(root)
			require.NoError(t, err)
			lines := strings.Split(strings.TrimSuffix(strings.ReplaceAll(string(sessions), "\r\n", "\n"), "\n"), "\n")
			require.Len(t, lines, 3, "each scaffolded task must start one recorded session")
			for _, line := range lines {
				var resolved []string
				for _, p := range strings.Split(line, "\x1f") {
					if p == "" {
						continue
					}
					r, err := filepath.EvalSymlinks(p)
					require.NoError(t, err)
					resolved = append(resolved, r)
				}
				require.Equal(t, []string{wantRoot}, resolved, "session must receive exactly the standalone skill root")
			}
			for _, name := range []string{"results.json", "runtime-invoked", "session-skill-paths"} {
				require.NoError(t, os.Remove(filepath.Join(root, name)))
			}

			require.NoError(t, os.WriteFile(filepath.Join(root, "evals", evalFile), []byte("config: [invalid]\n"), 0o600))
			out, err = initCIShell(t, root, step.Run, step.Env)
			require.Error(t, err, out)
			require.NotContains(t, out, "No evaluation files found")
			require.NoFileExists(t, filepath.Join(root, "runtime-invoked"))
			// Repairing a standalone scaffold must preserve a user-owned workflow.
			require.NoError(t, os.WriteFile(workflowPath, []byte("name: User workflow\n"), 0o600))
			require.NoError(t, cmd.Execute())
			data, err = os.ReadFile(workflowPath)
			require.NoError(t, err)
			require.Equal(t, "name: User workflow\n", string(data))
		})
	}
}

type initCIEngine struct{ preflightEngineSpy }

// initCISkillRecorder records the skill directories each session receives.
type initCISkillRecorder struct{ execution.AgentEngine }

func (e *initCISkillRecorder) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	f, err := os.OpenFile("session-skill-paths", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintln(f, strings.Join(req.SkillPaths, "\x1f")); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return e.AgentEngine.Execute(ctx, req)
}

func (*initCIEngine) Execute(context.Context, *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	return nil, errors.New("synthetic task runtime error")
}

func TestInitCIHelperProcess(t *testing.T) {
	if os.Getenv("WAZA_INIT_CI_HELPER") != "1" {
		return
	}
	newRunEngine = func(models.Config) (execution.AgentEngine, error) {
		if err := os.WriteFile("runtime-invoked", nil, 0o600); err != nil {
			return nil, err
		}
		if os.Getenv("WAZA_INIT_CI_MODE") == "reject" {
			return nil, errors.New("offline runtime launch rejected")
		}
		if os.Getenv("WAZA_INIT_CI_MODE") == "task-error" {
			return &initCIEngine{}, nil
		}
		if os.Getenv("WAZA_INIT_CI_MODE") == "record-skills" {
			return &initCISkillRecorder{execution.NewMockEngine("never-call")}, nil
		}
		return execution.NewMockEngine("never-call"), nil
	}
	for i, arg := range os.Args {
		if arg == "--" {
			cmd := newRootCommand()
			cmd.SetArgs(os.Args[i+1:])
			if err := cmd.Execute(); err != nil {
				os.Exit(reportExit(os.Stderr, err))
			}
			os.Exit(0)
		}
	}
	t.Fatal("missing CLI arguments")
}
