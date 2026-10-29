package runnercompat

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

func inputs(t *testing.T) ([]byte, []byte, Options) {
	t.Helper()
	eval, err := os.ReadFile("testdata/eval-1.4.yaml")
	require.NoError(t, err)
	task, err := os.ReadFile("testdata/task.yaml")
	require.NoError(t, err)
	return eval, task, Options{
		EvalPath: filepath.Join("named", "custom-suite.yaml"),
		TaskPath: filepath.Join("named", "tasks", "selected.case.yaml"),
		RunKey:   "compatibility", Selection: runnercontract.Selection{Shard: "local", Task: "selected", Trial: 1, Attempt: 1},
	}
}

func replace(t *testing.T, data []byte, from, to string) []byte {
	t.Helper()
	require.Contains(t, string(data), from, "mutation must exercise the intended declaration")
	changed := bytes.Replace(data, []byte(from), []byte(to), 1)
	require.NotEqual(t, data, changed)
	return changed
}

func TestAdapterHistoricalVersionsAndExactSource(t *testing.T) {
	for _, version := range []string{"", "1.0", "1.1", "1.4", "1.99", "2.0"} {
		for _, newline := range []string{"LF", "CRLF"} {
			t.Run(version+"/"+newline, func(t *testing.T) {
				eval, task, options := inputs(t)
				header := ""
				if version != "" {
					header = fmt.Sprintf("schemaVersion: %q\n", version)
				}
				if version == "2.0" {
					header += "scenario: compatibility\n"
				}
				if version != "1.4" {
					eval = replace(t, eval, "schemaVersion: \"1.4\"\n", header)
				}
				if newline == "CRLF" {
					eval = bytes.ReplaceAll(eval, []byte("\n"), []byte("\r\n"))
					task = bytes.ReplaceAll(task, []byte("\n"), []byte("\r\n"))
					require.Contains(t, string(eval), "\r\n")
				}
				beforeEval, beforeTask := bytes.Clone(eval), bytes.Clone(task)
				request, err := AdaptLocal(eval, task, options)
				require.NoError(t, err)
				wire, err := json.Marshal(request)
				require.NoError(t, err)
				parsed, err := runnercontract.Parse(wire)
				require.NoError(t, err)
				require.Equal(t, "ready", runnerunit.Validate(wire).Status)
				require.Equal(t, string(eval), parsed.Input.Eval)
				require.Equal(t, string(task), parsed.Input.Task)
				require.Equal(t, options.Selection, parsed.Selection)
				require.Equal(t, options.Selection.Shard, parsed.Input.Shard)
				require.Equal(t, 30000, parsed.TimeoutMS)
				require.Equal(t, []string{"exact-unit", "inline.mock-text"}, parsed.RequiredCapabilities)
				require.NotNil(t, parsed.OptionalCapabilities)
				require.Empty(t, parsed.OptionalCapabilities)
				require.False(t, parsed.AllowPaidExecution)
				require.Equal(t, beforeEval, eval)
				require.Equal(t, beforeTask, task)
			})
		}
	}
}

func TestAdapterRejectsAbsoluteTaskAndUnadmittedPrompt(t *testing.T) {
	for _, control := range []string{"absolute task", "prompt path", "alternative prompt", "relative custom task"} {
		t.Run(control, func(t *testing.T) {
			eval, task, options := inputs(t)
			switch control {
			case "absolute task":
				absolute, err := filepath.Abs(options.TaskPath)
				require.NoError(t, err)
				eval = replace(t, eval, "tasks/selected.case.yaml", filepath.ToSlash(absolute))
				options.TaskPath = absolute
			case "prompt path":
				task = replace(t, task, "prompt: hello", "prompt: inspect ./file")
			case "alternative prompt":
				task = replace(t, task, "prompt: hello", "prompt: hello alternative")
			}
			request, err := AdaptLocal(eval, task, options)
			if control == "absolute task" || control == "prompt path" {
				require.Error(t, err)
				require.Nil(t, request)
				return
			}
			require.NoError(t, err)
			wire, err := json.Marshal(request)
			require.NoError(t, err)
			require.Equal(t, "ready", runnerunit.Validate(wire).Status)
			require.Equal(t, string(task), request.Input.Task)
		})
	}
}

func TestAdapterPresenceAndAlternativeValidControls(t *testing.T) {
	for _, control := range []struct {
		name, evalFrom, evalTo, taskExtra string
		timeout                           int
	}{
		{"omitted attempt default", "  max_attempts: 0\n", "", "", 30000},
		{"explicit one attempt", "max_attempts: 0", "max_attempts: 1", "", 30000},
		{"omitted enabled", "", "", "", 30000},
		{"null enabled", "", "", "enabled: null\n", 30000},
		{"explicit enabled", "", "", "enabled: true\n", 30000},
		{"null timeout inherits", "", "", "timeout_seconds: null\n", 30000},
		{"task timeout override", "", "", "timeout_seconds: 7\n", 7000},
		{"explicit disabled injection", "  executor: mock", "  executor: mock\n  inject_skill_body: false", "", 30000},
		{"null injection", "  executor: mock", "  executor: mock\n  inject_skill_body: null", "", 30000},
		{"explicit empty task skills", "", "", "skill_directories: []\n", 30000},
		{"null task skills", "", "", "skill_directories: null\n", 30000},
		{"empty eval skills", "  executor: mock", "  executor: mock\n  skill_directories: []", "", 30000},
		{"scenario empty disabled skills", "schemaVersion: \"1.4\"", "schemaVersion: \"2.0\"\nscenario: no-skills", "", 30000},
		{"maximum deadline", "timeout_seconds: 30", "timeout_seconds: 86400", "", 86400000},
	} {
		t.Run(control.name, func(t *testing.T) {
			eval, task, options := inputs(t)
			if control.evalFrom != "" {
				eval = replace(t, eval, control.evalFrom, control.evalTo)
			}
			if control.name == "scenario empty disabled skills" {
				eval = replace(t, eval, "disabled_skills: [\"*\"]", "disabled_skills: []")
			}
			task = append(task, control.taskExtra...)
			request, err := AdaptLocal(eval, task, options)
			require.NoError(t, err)
			require.Equal(t, control.timeout, request.TimeoutMS)
			require.Equal(t, string(eval), request.Input.Eval)
			require.Equal(t, string(task), request.Input.Task)
			options.TimeoutMS = &control.timeout
			_, err = AdaptLocal(eval, task, options)
			require.NoError(t, err)
		})
	}
}

func TestAdapterUnsupportedAndInvalidControls(t *testing.T) {
	for _, mutation := range []struct {
		name, target, from, to, diagnostic string
	}{
		{"native", "eval", "executor: mock", "executor: copilot-sdk", "config.executor"},
		{"missing model", "eval", "  model: mock-model\n", "", "config.model"},
		{"trials", "eval", "trials_per_task: 1", "trials_per_task: 2", "one declared trial"},
		{"zero trials", "eval", "trials_per_task: 1", "trials_per_task: 0", "trials_per_task"},
		{"retries", "eval", "max_attempts: 0", "max_attempts: 2", "one declared trial"},
		{"negative attempts", "eval", "max_attempts: 0", "max_attempts: -1", "one declared trial"},
		{"ambient skills", "eval", "  disabled_skills: [\"*\"]\n", "", "ambient skill"},
		{"empty disabled not deny-all", "eval", "disabled_skills: [\"*\"]", "disabled_skills: []", "ambient skill"},
		{"inject enabled", "eval", "executor: mock", "executor: mock\n  inject_skill_body: true", "inject_skill_body"},
		{"eval skill name", "eval", "name: adapter-frozen", "name: adapter-frozen\nskill: ambient", "skill names/directories"},
		{"eval skill files", "eval", "executor: mock", "executor: mock\n  skill_directories: [skills]", "skill names/directories"},
		{"task skill override", "task", "id: selected", "id: selected\nskill_directories: [override]", "skill names/directories"},
		{"task disabled", "task", "id: selected", "id: selected\nenabled: false", "disabled"},
		{"zero task timeout", "task", "id: selected", "id: selected\ntimeout_seconds: 0", "timeout_seconds"},
		{"negative timeout", "task", "id: selected", "id: selected\ntimeout_seconds: -1", "timeout"},
		{"overflow timeout", "eval", "timeout_seconds: 30", "timeout_seconds: 86401", "timeout"},
		{"wrong string wire type", "task", "prompt: hello", "prompt: 123", "string"},
		{"null prompt", "task", "prompt: hello", "prompt: null", "null"},
		{"duplicate key", "task", "id: selected", "id: selected\nid: other", "already defined"},
		{"aliases", "task", "prompt: hello", "prompt: &prompt hello", "alias"},
		{"extra document", "task", "id: selected", "---\nid: other\n---\nid: selected", "exactly one"},
		{"case mismatch", "task", "id: selected", "ID: selected", "unsupported"},
		{"unknown fields", "eval", "name: adapter-frozen", "name: adapter-frozen\nfuture_field: true", "unsupported"},
		{"malformed eval version", "eval", "schemaVersion: \"1.4\"", "schemaVersion: broken", "schema"},
		{"cross major", "eval", "schemaVersion: \"1.4\"", "schemaVersion: \"3.0\"", "migrate"},
		{"2 without scenario", "eval", "schemaVersion: \"1.4\"", "schemaVersion: \"2.0\"", "migrate"},
		{"null scalar config", "eval", "timeout_seconds: 30", "timeout_seconds: null", "null"},
		{"batch tasks", "eval", "tasks: [\"tasks/selected.case.yaml\"]", "tasks: [one.yaml, two.yaml]", "exactly one"},
		{"glob", "eval", "tasks/selected.case.yaml", "tasks/*.yaml", "literal"},
		{"different declared path", "eval", "tasks/selected.case.yaml", "other.yaml", "filename"},
		{"reserved grader name", "eval", "name: precise", "name: _output_contains", "reserved"},
		{"duplicate graders across scopes", "task", "name: task-text", "name: precise", "unique"},
		{"invalid regex", "task", `regex_match: ["^Mock response for: hello"]`, `regex_match: ["["]`, "regex"},
		{"precision not rounded", "eval", "name: precise", "name: precise\n    weight: 0.1234567890123456789", "unsupported"},
		{"blank expectation", "task", "output_contains: [hello]", "output_contains: [\" \"]", "blank"},
		{"unknown text config", "eval", "contains_cs:", "contains_cS:", "unknown"},
		{"task version declaration", "task", "id: selected", "id: selected\nschemaVersion: \"1.4\"", "unsupported"},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			eval, task, options := inputs(t)
			if mutation.target == "eval" {
				eval = replace(t, eval, mutation.from, mutation.to)
			} else {
				task = replace(t, task, mutation.from, mutation.to)
			}
			request, err := AdaptLocal(eval, task, options)
			require.Nil(t, request)
			require.ErrorContains(t, err, mutation.diagnostic)
			require.ErrorContains(t, err, "keep using waza run")
			require.ErrorContains(t, err, "RUNNER_COMPATIBILITY_MIGRATION")
		})
	}
}

func TestAdapterNeverDropsUnsupportedPresence(t *testing.T) {
	for _, declaration := range []struct{ scope, field string }{
		{"eval", "baseline"}, {"eval", "hooks"}, {"eval", "inputs"}, {"eval", "metrics"},
		{"eval", "tasks_from"}, {"eval", "range"}, {"eval", "mcp_mocks"}, {"eval", "command_mocks"},
		{"eval", "adversarial"}, {"config", "parallel"}, {"config", "workers"}, {"config", "fail_fast"},
		{"config", "instruction_files"}, {"config", "mcp_servers"}, {"config", "required_skills"},
		{"config", "trigger_skill_routing"}, {"config", "judge_model"}, {"config", "group_by"},
		{"task", "context_dir"}, {"task", "instruction_files"}, {"task", "requirements"},
		{"task", "checkpoints"}, {"task", "command_mocks"}, {"task", "golden"},
		{"inputs", "prompt_file"}, {"inputs", "files"}, {"inputs", "repos"}, {"inputs", "workdir"},
		{"inputs", "environment"}, {"inputs", "context"}, {"inputs", "follow_up_prompts"}, {"inputs", "responder"},
		{"expected", "should_trigger"}, {"expected", "behavior"}, {"expected", "tool_calls"}, {"expected", "outcomes"},
	} {
		for _, value := range []string{"null", "[]", "{}", "0", "false"} {
			t.Run(declaration.scope+"/"+declaration.field+"/"+value, func(t *testing.T) {
				eval, task, options := inputs(t)
				field := declaration.field + ": " + value + "\n"
				switch declaration.scope {
				case "eval":
					eval = append(eval, field...)
				case "config":
					eval = replace(t, eval, "config:\n", "config:\n  "+field)
				case "task":
					task = append(task, field...)
				default:
					task = replace(t, task, declaration.scope+":\n", declaration.scope+":\n  "+field)
				}
				request, err := AdaptLocal(eval, task, options)
				require.Nil(t, request)
				require.ErrorContains(t, err, "unsupported declaration")
				require.ErrorContains(t, err, declaration.field)
			})
		}
	}
}

func TestAdapterSelectionDeadlineAndSize(t *testing.T) {
	for _, change := range []struct {
		name string
		edit func(*Options)
	}{
		{"trial zero", func(o *Options) { o.Selection.Trial = 0 }},
		{"trial two", func(o *Options) { o.Selection.Trial = 2 }},
		{"attempt zero", func(o *Options) { o.Selection.Attempt = 0 }},
		{"attempt two", func(o *Options) { o.Selection.Attempt = 2 }},
		{"wrong id", func(o *Options) { o.Selection.Task = "other" }},
		{"bad run key", func(o *Options) { o.RunKey = "../invalid" }},
		{"missing eval path", func(o *Options) { o.EvalPath = "" }},
		{"missing task path", func(o *Options) { o.TaskPath = "" }},
		{"shorter deadline", func(o *Options) { o.TimeoutMS = new(1000) }},
		{"zero deadline", func(o *Options) { o.TimeoutMS = new(0) }},
	} {
		t.Run(change.name, func(t *testing.T) {
			eval, task, options := inputs(t)
			change.edit(&options)
			request, err := AdaptLocal(eval, task, options)
			require.Nil(t, request)
			require.ErrorContains(t, err, "keep using waza run")
		})
	}
	eval, task, options := inputs(t)
	_, err := AdaptLocal(bytes.Repeat([]byte(" "), runnercontract.MaxRequestBytes), task, options)
	require.ErrorContains(t, err, "limit")
	_, err = AdaptLocal(append(bytes.Clone(eval), 0xff), task, options)
	require.ErrorContains(t, err, "UTF-8")
	// A source that fits the raw limit can exceed it once JSON-escaped.
	task = replace(t, task, "prompt: hello", "prompt: '"+strings.Repeat(`\`, runnercontract.MaxRequestBytes/2)+"'")
	_, err = AdaptLocal(eval, task, options)
	require.ErrorContains(t, err, "exceeds")
}

func TestAdapterNoChecksAndNonTextGraders(t *testing.T) {
	eval, _, options := inputs(t)
	eval = replace(t, eval, "graders:\n  - name: precise\n    type: text\n    config:\n      contains_cs: [\"Mock response for: hello\"]\n", "")
	task := []byte("id: selected\nname: Selected task\ninputs:\n  prompt: hello\n")
	_, err := AdaptLocal(eval, task, options)
	require.ErrorContains(t, err, "at least one")
	task = append(task, []byte("graders:\n  - name: script\n    type: code\n    config:\n      assertions: ['True']\n")...)
	_, err = AdaptLocal(eval, task, options)
	require.ErrorContains(t, err, "not builtin text")
}

func TestAdapterOfflineAmbientAndExternalDependencySentinels(t *testing.T) {
	eval, task, options := inputs(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, ".waza.yaml"), []byte("broken: ["), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(directory, "skills"), 0o700))
	t.Chdir(directory)
	t.Setenv("COPILOT_CLI_PATH", filepath.Join(directory, "must-never-launch"))
	t.Setenv("ENABLE_COPILOT_TESTS", "false")
	request, err := AdaptLocal(eval, task, options)
	require.NoError(t, err, "adaptation must not read ambient project config or open source paths")
	require.NotNil(t, request)
	for _, target := range []string{"eval", "task"} {
		badEval, badTask := bytes.Clone(eval), bytes.Clone(task)
		remote := "graders:\n  - name: external\n    type: json_schema\n    config:\n      schema:\n        $ref: " + server.URL + "/schema\n"
		if target == "eval" {
			badEval = replace(t, badEval, "graders:\n  - name: precise\n    type: text\n    config:\n      contains_cs: [\"Mock response for: hello\"]\n", remote)
		} else {
			badTask = replace(t, badTask, "graders:\n  - name: task-text\n    type: text\n    config:\n      regex_match: [\"^Mock response for: hello\"]\n", remote)
		}
		_, err := AdaptLocal(badEval, badTask, options)
		require.ErrorContains(t, err, "external")
	}
	require.Zero(t, requests.Load(), "offline schema guards must deny external fetches")
}

func TestAdapterFrozenInputChecksums(t *testing.T) {
	for name, expected := range map[string]string{
		"eval-1.4.yaml": "baefad0b21ccd27037174fa05ab8389673e91bc7e916bac53827399ac0a42ec4",
		"task.yaml":     "1c007c8d56b3642bf9bf0fc97891ea64be9684c28dd7a53e49c655ff723f9654",
	} {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		require.NoError(t, err)
		sum := sha256.Sum256(data)
		require.Equal(t, expected, hex.EncodeToString(sum[:]), "frozen historical input changed")
	}
}
