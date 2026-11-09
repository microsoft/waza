//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestRunnerCompatSymlinkParentSourcesCompiledProcess(t *testing.T) {
	binary := buildRecoveryCLI(t)
	for _, relative := range []bool{false, true} {
		t.Run(map[bool]string{false: "absolute", true: "relative"}[relative], func(t *testing.T) {
			evalPath, taskPath, eval, task := adapterCommandInputs(t)
			directory := filepath.Dir(evalPath)
			other := filepath.Join(directory, "other")
			require.NoError(t, os.MkdirAll(filepath.Join(other, "child"), 0o700))
			require.NoError(t, os.Mkdir(filepath.Join(other, "tasks"), 0o700))
			require.NoError(t, os.Symlink(filepath.Join(other, "child"), filepath.Join(directory, "link")))
			otherEval := bytes.Replace(eval, []byte("name: adapter-frozen"), []byte("name: os-selected-suite"), 1)
			otherTask := bytes.Replace(task, []byte("prompt: hello"), []byte("prompt: hello os-selected-task"), 1)
			require.NotEqual(t, eval, otherEval)
			require.NotEqual(t, task, otherTask)
			require.NoError(t, os.WriteFile(filepath.Join(other, filepath.Base(evalPath)), otherEval, 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(other, "tasks", filepath.Base(taskPath)), otherTask, 0o600))
			prefix := directory + string(filepath.Separator)
			if relative {
				prefix = ""
			}
			// Do not use filepath.Join: cleaning would erase the OS-walk control.
			uncleanEval := prefix + "link/../custom-suite.yaml"
			uncleanTask := prefix + "link/../tasks/selected.case.yaml"
			captured, err := os.ReadFile(uncleanEvalPath(directory, uncleanEval))
			require.NoError(t, err)
			require.Equal(t, otherEval, captured)
			captured, err = os.ReadFile(uncleanEvalPath(directory, uncleanTask))
			require.NoError(t, err)
			require.Equal(t, otherTask, captured)

			nativeArtifact := filepath.Join(directory, "native.json")
			adapterProcess(t, binary, directory, nil, 0, "run", uncleanEval, "--output", nativeArtifact, "--no-summary")
			native, err := models.LoadEvaluationOutcome(nativeArtifact)
			require.NoError(t, err)
			require.Equal(t, "os-selected-suite", native.BenchName)
			require.Len(t, native.TestOutcomes, 1)
			require.Len(t, native.TestOutcomes[0].Runs, 1)
			require.Equal(t, "hello", native.TestOutcomes[0].Runs[0].Prompt, "native resolves tasks from the cleaned eval directory")

			t.Run("local run", func(t *testing.T) {
				compatArtifact := filepath.Join(directory, "compat.json")
				adapterProcess(t, binary, directory, nil, 0, "run", uncleanEval,
					"--runner-compat", "--output", compatArtifact, "--no-summary")
				before, err := os.ReadFile(nativeArtifact)
				require.NoError(t, err)
				after, err := os.ReadFile(compatArtifact)
				require.NoError(t, err)
				require.JSONEq(t, string(adapterOutcomeProjection(t, before)), string(adapterOutcomeProjection(t, after)))
			})
			t.Run("offline adapter", func(t *testing.T) {
				output, _ := adapterProcess(t, binary, directory, nil, 0,
					append([]string{"runner", "adapt"}, adapterCommandArgs(uncleanEval, uncleanTask)...)...)
				request, err := runnercontract.Parse(output)
				require.NoError(t, err)
				require.Equal(t, string(otherEval), request.Input.Eval)
				require.Equal(t, string(task), request.Input.Task, "capture the normalized path validated against native task resolution")
				assertAdaptedAdmission(t, binary, directory, output)
			})
			t.Run("missing native task", func(t *testing.T) {
				require.NoError(t, os.Remove(taskPath))
				for _, compat := range []bool{false, true} {
					artifact := filepath.Join(directory, map[bool]string{false: "missing-native.json", true: "missing-compat.json"}[compat])
					args := []string{"run", uncleanEval, "--output", artifact, "--no-summary"}
					if compat {
						args = append(args, "--runner-compat")
					}
					adapterProcess(t, binary, directory, nil, 2, args...)
					_, err := os.Stat(artifact)
					require.ErrorIs(t, err, os.ErrNotExist)
				}
				output, diagnostic := adapterProcess(t, binary, directory, nil, 2,
					append([]string{"runner", "adapt"}, adapterCommandArgs(uncleanEval, uncleanTask)...)...)
				require.Empty(t, output, "the OS-walk task still exists but is not the selected native task")
				require.Contains(t, diagnostic, "opening exact adapter source")
			})
		})
	}
}

func TestRunnerCompatAmbientAncestorLimitCompiledProcess(t *testing.T) {
	binary := buildRecoveryCLI(t)
	for _, depth := range []int{0, 9, 10} {
		t.Run(map[int]string{0: "cwd", 9: "last ancestor", 10: "outside limit"}[depth], func(t *testing.T) {
			evalPath, _, _, _ := adapterCommandInputs(t)
			root := filepath.Dir(evalPath)
			directory := root
			for range depth {
				directory = filepath.Join(directory, "child")
			}
			require.NoError(t, os.MkdirAll(directory, 0o700))
			require.NoError(t, unix.Mkfifo(filepath.Join(root, ".waza.yaml"), 0o600))
			artifact := filepath.Join(root, "compat.json")
			code := 2
			if depth == 10 {
				code = 0
			}
			_, diagnostic := adapterProcess(t, binary, directory, nil, code, "run", evalPath,
				"--runner-compat", "--output", artifact, "--no-summary")
			if code != 0 {
				require.Contains(t, diagnostic, "cannot preserve ambient .waza.yaml")
				_, err := os.Stat(artifact)
				require.ErrorIs(t, err, os.ErrNotExist)
				return
			}
			nativeArtifact := filepath.Join(root, "native.json")
			adapterProcess(t, binary, directory, nil, 0, "run", evalPath, "--output", nativeArtifact, "--no-summary")
			before, err := os.ReadFile(nativeArtifact)
			require.NoError(t, err)
			after, err := os.ReadFile(artifact)
			require.NoError(t, err)
			require.JSONEq(t, string(adapterOutcomeProjection(t, before)), string(adapterOutcomeProjection(t, after)))
		})
	}
}

func uncleanEvalPath(directory, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return directory + string(filepath.Separator) + path
}

func TestRunnerCompatAmbientPresenceCompiledProcess(t *testing.T) {
	binary := buildRecoveryCLI(t)
	for _, name := range []string{".waza.yaml", "trigger_tests.yaml"} {
		for _, kind := range []string{"fifo", "fifo symlink", "directory", "dangling symlink", "malformed file"} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				evalPath, _, _, _ := adapterCommandInputs(t)
				directory := filepath.Dir(evalPath)
				path := filepath.Join(directory, name)
				switch kind {
				case "fifo", "fifo symlink":
					fifo := path
					if kind == "fifo symlink" {
						fifo += ".fifo"
					}
					require.NoError(t, unix.Mkfifo(fifo, 0o600))
					if fifo != path {
						require.NoError(t, os.Symlink(fifo, path))
					}
					info, err := os.Stat(path)
					require.NoError(t, err)
					require.NotZero(t, info.Mode()&os.ModeNamedPipe)
				case "directory":
					require.NoError(t, os.Mkdir(path, 0o700))
				case "dangling symlink":
					require.NoError(t, os.Symlink(path+".missing", path))
				case "malformed file":
					require.NoError(t, os.WriteFile(path, []byte("invalid: ["), 0o600))
				}
				artifact := filepath.Join(directory, "forbidden.json")
				output, diagnostic := adapterProcess(t, binary, directory, nil, 2, "run", evalPath,
					"--runner-compat", "--output", artifact)
				require.Empty(t, output)
				if name == ".waza.yaml" {
					require.Contains(t, diagnostic, "cannot preserve ambient .waza.yaml")
				} else {
					require.Contains(t, diagnostic, "cannot preserve adjacent trigger tests")
				}
				_, err := os.Stat(artifact)
				require.ErrorIs(t, err, os.ErrNotExist)
			})
		}
	}
}
