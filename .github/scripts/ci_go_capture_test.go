package scripts

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestCIGoCaptureActualMockCommandTerminals(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	install := exec.CommandContext(t.Context(), "bash", filepath.Join(root, ".github", "scripts", "go_offline_env.sh"))
	install.Env = append(os.Environ(), "RUNNER_TEMP="+dir)
	var stderr bytes.Buffer
	install.Stderr = &stderr
	assignments, err := install.Output()
	if err != nil {
		t.Fatalf("installing rejecting runtime guard: %v\n%s", err, &stderr)
	}
	env := append(os.Environ(), strings.Split(strings.TrimSpace(string(assignments)), "\n")...)
	guard := ""
	for _, assignment := range strings.Split(strings.TrimSpace(string(assignments)), "\n") {
		if value, ok := strings.CutPrefix(assignment, "COPILOT_CLI_PATH="); ok {
			guard = value
		}
	}
	if !filepath.IsAbs(guard) {
		t.Fatal("runtime guard is not absolute")
	}
	assertNoReleaseRuntime(t, guard)
	t.Cleanup(func() { assertNoReleaseRuntime(t, guard) })
	names := []string{"TestSuggest_CopilotJSON", "TestSuggest_CopilotModel", "TestSuggest_CopilotMultipleFiles", "TestSuggest_CopilotText"}
	producer := exec.CommandContext(t.Context(), "go", "test", "-count=1", "-json", "-v", "-timeout=60s",
		"./cmd/waza/tokens", "-run", "^("+strings.Join(names, "|")+")$")
	producer.Dir, producer.Env = root, env
	stderr.Reset()
	producer.Stderr = &stderr
	stream, err := producer.Output()
	if err != nil {
		t.Fatalf("actual mock command producer: %v\n%s", err, &stderr)
	}
	runs, passes := make(map[string]int), make(map[string]int)
	for _, line := range bytes.Split(bytes.TrimSpace(stream), []byte("\n")) {
		var event struct{ Action, Test string }
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("actual producer emitted invalid JSON: %v", err)
		}
		switch event.Action {
		case "run":
			runs[event.Test]++
		case "pass":
			if event.Test != "" {
				passes[event.Test]++
			}
		}
	}
	if len(runs) != len(names) || len(passes) != len(names) {
		t.Fatalf("actual terminal inventory: runs=%v passes=%v", runs, passes)
	}
	const pkg = "github.com/microsoft/waza/cmd/waza/tokens"
	var inventory strings.Builder
	for _, name := range names {
		if runs[name] != 1 || passes[name] != 1 {
			t.Fatalf("%s: run=%d pass=%d", name, runs[name], passes[name])
		}
		inventory.WriteString(pkg + "::" + name + "\n")
	}
	all := filepath.Join(dir, "all")
	if err := os.WriteFile(all, []byte(inventory.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	collector := filepath.Join(root, ".github", "scripts", "ci-evidence.cjs")
	reportPath := filepath.Join(dir, "report.json")
	capture := exec.CommandContext(t.Context(), "node", collector, "go", reportPath, all, all)
	capture.Dir, capture.Env, capture.Stdin = dir, env, bytes.NewReader(stream)
	if output, err := capture.CombinedOutput(); err != nil {
		t.Fatalf("actual Go stream capture: %v\n%s", err, output)
	}
	finalize := exec.CommandContext(t.Context(), "node", collector, "finish-go", reportPath, "0", "0")
	finalize.Dir, finalize.Env = dir, env
	if output, err := finalize.CombinedOutput(); err != nil {
		t.Fatalf("actual Go stream finalization: %v\n%s", err, output)
	}
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Complete bool
		Targets  []struct {
			Name, Status string
			Seconds      *float64
		}
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if !report.Complete || len(report.Targets) != len(names) {
		t.Fatalf("actual mock command evidence incomplete: %s", raw)
	}
	for _, target := range report.Targets {
		if !slices.Contains(names, target.Name) || target.Status != "pass" || target.Seconds == nil {
			t.Fatalf("missing actual terminal verdict/timing: %+v", target)
		}
	}
}

func copyCICaptureScripts(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"ci_go_test_capture.sh", "ci-evidence.cjs"} {
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCIGoCaptureProductionSelectionAndExit(t *testing.T) {
	const native = "github.com/microsoft/waza/internal/controlledcomparison"
	packages := []string{"example/first", native, "example/last"}
	realNode, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"all", "remaining"} {
		for _, tc := range []struct {
			name, phase, collector, finalizer string
			want                              int
		}{
			{"success", "", "", "", 0},
			{"discovery", "discovery", "", "", 7},
			{"execution", "execution", "", "", 9},
			{"collector", "", "go", "", 11},
			{"finalize-discovery", "", "", "finish-discovery", 13},
			{"finalize-go", "", "", "finish-go", 13},
			{"discovery-finalization", "discovery", "", "finish-discovery", 7},
			{"execution-finalization", "execution", "", "finish-go", 9},
			{"collector-finalization", "", "go", "finish-go", 11},
			{"all-execution-failures", "execution", "go", "finish-go", 9},
		} {
			t.Run(scope+"/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				scripts := filepath.Join(dir, "scripts")
				copyCICaptureScripts(t, scripts)
				bin := filepath.Join(dir, "bin")
				if err := os.Mkdir(bin, 0o700); err != nil {
					t.Fatal(err)
				}
				fake := `#!/usr/bin/env bash
set -euo pipefail
if [ "$*" = version ]; then echo 'go version go1.26.4 linux/amd64'; exit 0; fi
if [ "$*" = 'list ./...' ]; then printf '%s\n' example/first github.com/microsoft/waza/internal/controlledcomparison example/last; exit 0; fi
printf '%s\n' "$@" >> "$CALLS"
echo --end-- >> "$CALLS"
if [ "$1" != test ]; then exit 88; fi
shift
if [ "$1" = -race ]; then
  phase=discovery
  shift 5
else
  phase=execution
  shift 7
fi
if [ "$*" = ./... ]; then
  selected=(example/first github.com/microsoft/waza/internal/controlledcomparison example/last)
else
  selected=("$@")
fi
if [ "$FAIL_PHASE" = discovery ] && [ "$phase" = discovery ]; then exit 7; fi
for package in "${selected[@]}"; do
  printf '{"Action":"start","Package":"%s"}\n' "$package"
  if [ "$phase" = discovery ]; then
    printf '{"Action":"output","Package":"%s","Output":"TestObserved\\n"}\n' "$package"
  else
    printf '{"Action":"run","Package":"%s","Test":"TestObserved"}\n' "$package"
    printf '{"Action":"pass","Package":"%s","Test":"TestObserved","Elapsed":1}\n' "$package"
  fi
  printf '{"Action":"pass","Package":"%s","Elapsed":2}\n' "$package"
done
if [ "$FAIL_PHASE" = execution ] && [ "$phase" = execution ]; then exit 9; fi
`
				if err := os.WriteFile(filepath.Join(bin, "go"), []byte(fake), 0o700); err != nil {
					t.Fatal(err)
				}
				fakeNode, err := os.ReadFile(filepath.Join("testdata", "ci-evidence-node-fault.sh"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(bin, "node"), fakeNode, 0o700); err != nil {
					t.Fatal(err)
				}
				calls := filepath.Join(dir, "calls")
				cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(scripts, "ci_go_test_capture.sh"), scope, "coverage.txt")
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "CALLS="+calls,
					"FAIL_PHASE="+tc.phase, "FAIL_COLLECTOR="+tc.collector, "FAIL_FINALIZER="+tc.finalizer, "REAL_NODE="+realNode)
				output, err := cmd.CombinedOutput()
				status := 0
				if err != nil {
					exit, ok := err.(*exec.ExitError)
					if !ok {
						t.Fatal(err)
					}
					status = exit.ExitCode()
				}
				if status != tc.want {
					t.Fatalf("status=%d want=%d: %s", status, tc.want, output)
				}
				rawCalls, err := os.ReadFile(calls)
				if err != nil {
					t.Fatal(err)
				}
				commands := strings.Split(strings.TrimSuffix(string(rawCalls), "--end--\n"), "--end--\n")
				wantPackages := packages
				if scope == "remaining" {
					wantPackages = []string{packages[0], packages[2]}
				}
				selector := wantPackages
				if scope == "all" {
					selector = []string{"./..."}
				}
				discovery := append([]string{"test", "-race", "-covermode=atomic", "-json", "-list", "."}, selector...)
				if !reflect.DeepEqual(strings.Fields(commands[0]), discovery) {
					t.Fatalf("discovery=%v want=%v", strings.Fields(commands[0]), discovery)
				}
				count := 2
				if tc.phase == "discovery" || tc.finalizer == "finish-discovery" {
					count = 1
				}
				if len(commands) != count {
					t.Fatalf("go invocations=%d want=%d", len(commands), count)
				}
				if count == 2 {
					execution := append([]string{"test", "-count=1", "-json", "-v", "-race", "-timeout=30m", "-coverprofile=coverage.txt", "-covermode=atomic"}, selector...)
					if !reflect.DeepEqual(strings.Fields(commands[1]), execution) {
						t.Fatalf("execution=%v want=%v", strings.Fields(commands[1]), execution)
					}
				}
				raw, err := os.ReadFile(filepath.Join(dir, "coverage.txt.evidence.json"))
				if err != nil {
					t.Fatal(err)
				}
				var report struct {
					Complete      bool
					Diagnostics   []string
					ExecutionExit *int `json:"execution_exit"`
					Selected      []string
					Targets       []struct {
						Package string
					}
				}
				if err := json.Unmarshal(raw, &report); err != nil {
					t.Fatal(err)
				}
				if tc.phase == "discovery" || tc.finalizer != "" || tc.collector != "" {
					if report.Complete || len(report.Diagnostics) == 0 {
						t.Fatalf("failed capture/finalization became complete evidence: %s", raw)
					}
					if tc.finalizer == "finish-go" && tc.collector == "" {
						if report.ExecutionExit != nil || !slices.Contains(report.Diagnostics, "execution-finalization-pending") {
							t.Fatalf("unfinalized command evidence invented an exit/completion: %s", raw)
						}
					}
					return
				}
				if tc.phase == "execution" {
					if report.Complete || report.ExecutionExit == nil || *report.ExecutionExit != 9 {
						t.Fatalf("late command failure became success: %s", raw)
					}
				} else if !report.Complete || report.ExecutionExit == nil || *report.ExecutionExit != 0 {
					t.Fatalf("successful command status missing: %s", raw)
				}
				if len(report.Targets) != len(wantPackages) {
					t.Fatalf("capture incomplete or wrong target count: %s", raw)
				}
				gotPackages := make([]string, 0, len(report.Targets))
				for _, target := range report.Targets {
					gotPackages = append(gotPackages, target.Package)
				}
				wantSorted := append([]string(nil), wantPackages...)
				slices.Sort(wantSorted)
				if !slices.Equal(gotPackages, wantSorted) {
					t.Fatalf("targets=%v want=%v", gotPackages, wantSorted)
				}
			})
		}
	}
}
