package validation

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLazySchemasProductionCLIStartup(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	sourcePath := filepath.Join(root, "internal", "validation", "schema.go")
	source, err := os.ReadFile(sourcePath)
	require.NoError(t, err)
	// Poison the real compiler boundary: any startup compilation must fail,
	// while a validation command proves the sentinel is actually reachable.
	poisoned := poisonSchemaCompiler(t, string(source))
	project := isolatedSchemaCLIDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(project, ".waza.yaml"), []byte("{}\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(project, "evals", "startup-test"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(project, "evals", "startup-test", "eval.yaml"), []byte(validEvalYAML), 0o600))
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, project)
	}
	dir := isolatedSchemaCLIDir(t)
	rel, err := filepath.Rel(project, dir)
	require.NoError(t, err)
	require.False(t, filepath.IsLocal(rel), "startup fixture must escape project-local temporary storage")
	overlaySource := filepath.Join(dir, "schema.go")
	require.NoError(t, os.WriteFile(overlaySource, []byte(poisoned), 0o600))
	overlay, err := json.Marshal(map[string]any{"Replace": map[string]string{sourcePath: overlaySource}})
	require.NoError(t, err)
	overlayPath := filepath.Join(dir, "overlay.json")
	require.NoError(t, os.WriteFile(overlayPath, overlay, 0o600))
	binary := filepath.Join(dir, "waza")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.CommandContext(t.Context(), "go", "build", "-overlay", overlayPath, "-o", binary, "./cmd/waza")
	build.Dir = root
	output, err := build.CombinedOutput()
	require.NoError(t, err, "%s", output)

	run := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(t.Context(), binary, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "WAZA_NO_UPDATE_CHECK=1", "NO_COLOR=1")
		return cmd.CombinedOutput()
	}
	for _, args := range [][]string{{"--version"}, {"--help"}, {"check", "--help"}} {
		output, err := run(args...)
		require.NoError(t, err, "%s: %s", args, output)
		require.NotEmpty(t, output)
		require.NotContains(t, string(output), "failed to parse embedded")
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: startup-test\ndescription: A schema startup test skill.\n---\n\n# Startup\n"), 0o600))
	output, err = run("check", ".", "--format", "json")
	require.NoError(t, err, "%s", output)
	var report struct {
		Skills []struct {
			Schema any `json:"schema"`
		} `json:"skills"`
	}
	require.NoError(t, json.Unmarshal(output, &report))
	require.Len(t, report.Skills, 1)
	require.Nil(t, report.Skills[0].Schema)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "eval.yaml"), []byte(validEvalYAML), 0o600))
	output, err = run("check", ".", "--format", "json")
	require.Error(t, err)
	require.Contains(t, string(output), "panic: failed to parse embedded eval.schema.json:")

	build = exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/waza")
	build.Dir = root
	output, err = build.CombinedOutput()
	require.NoError(t, err, "%s", output)

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	eval := strings.Replace(validEvalYAML, "tasks/*.yaml", "task.yaml", 1) +
		"graders:\n  - type: json_schema\n    name: offline-schema\n    config:\n      schema:\n        $ref: " + server.URL + "/must-not-fetch\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "eval.yaml"), []byte(eval), 0o600))
	for _, tc := range []struct {
		name string
		task string
		want []string
	}{
		{"valid", validTaskYAML, nil},
		{"alternative-valid", "id: task-1\nname: File prompt\ninputs:\n  prompt_file: prompt.txt\n", nil},
		{"invalid", "null\n", []string{"/: got null, want object"}},
		{"missing-required", "{}\n", []string{"/: missing properties 'id', 'name', 'inputs'"}},
		{"repeated-valid", validTaskYAML, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(tc.task), 0o600))
			output, err := run("check", ".", "--format", "json")
			require.NoError(t, err, "%s", output)
			var report struct {
				Skills []struct {
					Schema *struct {
						Valid      bool                `json:"valid"`
						EvalErrors []string            `json:"evalErrors"`
						TaskErrors map[string][]string `json:"taskErrors"`
					} `json:"schema"`
				} `json:"skills"`
			}
			require.NoError(t, json.Unmarshal(output, &report), "%s", output)
			require.Len(t, report.Skills, 1)
			schema := report.Skills[0].Schema
			require.NotNil(t, schema)
			require.Empty(t, schema.EvalErrors)
			require.Equal(t, tc.want, schema.TaskErrors["task.yaml"])
			require.Equal(t, tc.want == nil, schema.Valid)
			if tc.want == nil {
				require.Empty(t, schema.TaskErrors)
			} else {
				require.Len(t, schema.TaskErrors, 1)
			}
		})
	}
	require.Zero(t, requests.Load(), "static validation must not fetch grader schema references")
}

func isolatedSchemaCLIDir(t *testing.T) string {
	t.Helper()
	base, err := os.UserCacheDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(base, 0o700))
	dir, err := os.MkdirTemp(base, "waza-schema-cli-")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.RemoveAll(dir), "removing owned schema CLI fixture")
	})
	physical, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	require.NoError(t, validateSchemaCLIAncestry(physical))
	return physical
}

func validateSchemaCLIAncestry(dir string) error {
	for {
		for _, marker := range []string{"skills", ".waza.yaml", ".git"} {
			path := filepath.Join(dir, marker)
			_, err := os.Lstat(path)
			if err == nil {
				return fmt.Errorf("schema CLI fixture inherits project marker %q", path)
			}
			if !os.IsNotExist(err) {
				return fmt.Errorf("checking schema CLI fixture marker %q: %w", path, err)
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

func TestSchemaCLIAncestryRejectsProjectMarkers(t *testing.T) {
	for _, marker := range []string{"skills", ".waza.yaml", ".git"} {
		t.Run(marker, func(t *testing.T) {
			parent := isolatedSchemaCLIDir(t)
			require.NoError(t, os.WriteFile(filepath.Join(parent, marker), nil, 0o600))
			require.ErrorContains(t, validateSchemaCLIAncestry(filepath.Join(parent, "nested")), marker)
		})
	}
}

func poisonSchemaCompiler(t *testing.T, source string) string {
	t.Helper()
	source = strings.ReplaceAll(source, "\r\n", "\n")
	needle := "func mustCompileSchema(raw string, name string) *jsonschema.Schema {\n"
	require.Equal(t, 1, strings.Count(source, needle))
	return strings.Replace(source, needle, needle+"\traw = \"{\"\n", 1)
}

func TestLazySchemaCompilerOverlayLineEndings(t *testing.T) {
	source, err := os.ReadFile("schema.go")
	require.NoError(t, err)
	lf := strings.ReplaceAll(string(source), "\r\n", "\n")
	crlf := strings.ReplaceAll(lf, "\n", "\r\n")
	require.Contains(t, crlf, "\r\n")
	require.NotContains(t, lf, "\r\n")
	for _, input := range []string{lf, crlf} {
		poisoned := poisonSchemaCompiler(t, input)
		require.NotContains(t, poisoned, "\r\n")
		require.Equal(t, 1, strings.Count(poisoned, "\traw = \"{\"\n"))
		require.Equal(t, lf, strings.Replace(poisoned, "\traw = \"{\"\n", "", 1))
	}
}
