//go:build unix

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPreflightWorkspaceRequiredSkillDefinitionsAreRegular(t *testing.T) {
	for _, tc := range []struct {
		name       string
		definition string
		skillsRoot string
		configured bool
		override   bool
		explicit   bool
	}{
		{name: "implicit skill", definition: "SKILL.md", skillsRoot: "skills"},
		{name: "configured skill", definition: "SKILL.md", skillsRoot: "configured-skills", configured: true},
		{name: "overridden skill", definition: "SKILL.md", skillsRoot: "selected-skills", override: true},
		{name: "explicit skill", definition: "SKILL.md", skillsRoot: "skills", explicit: true},
		{name: "explicit agent", definition: "helper.agent.md", skillsRoot: "skills", explicit: true},
	} {
		for _, fifo := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fifo=%t", tc.name, fifo), func(t *testing.T) {
				dir := t.TempDir()
				require.NoError(t, validateCLIIsolation(dir))
				config := "{}\n"
				if tc.configured {
					config = "paths:\n  skills: " + tc.skillsRoot + "\n"
				}
				require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte(config), 0o600))

				reviewed := filepath.Join(dir, tc.skillsRoot, "reviewed")
				require.NoError(t, os.MkdirAll(reviewed, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(reviewed, "SKILL.md"), []byte("---\nname: reviewed\ndescription: Valid\n---\n"), 0o600))
				other := filepath.Join(dir, tc.skillsRoot, "other")
				if tc.explicit {
					other = filepath.Join(dir, "extra", "other")
				}
				require.NoError(t, os.MkdirAll(other, 0o700))
				definition := filepath.Join(other, tc.definition)
				if fifo {
					require.NoError(t, syscall.Mkfifo(definition, 0o600))
					info, err := os.Stat(definition)
					require.NoError(t, err)
					require.NotZero(t, info.Mode()&os.ModeNamedPipe, "fixture must be a FIFO")
				} else {
					require.NoError(t, os.WriteFile(definition, []byte("---\nname: other\ndescription: Valid\n---\n"), 0o600))
				}

				eval := filepath.FromSlash(initCIWriteEvalAt(t, filepath.Join(dir, "evals", "reviewed"), "eval.yaml", ""))
				data, err := os.ReadFile(eval)
				require.NoError(t, err)
				before := string(data)
				extra := ", required_skills: [reviewed]"
				if tc.explicit {
					extra += ", skill_directories: [../../skills/reviewed, ../../extra/other]"
				}
				after := strings.Replace(before, "timeout_seconds: 5}", "timeout_seconds: 5"+extra+"}", 1)
				require.NotEqual(t, before, after, "fixture must exercise required-skill discovery")
				require.NoError(t, os.WriteFile(eval, []byte(after), 0o600))

				var invalidEval string
				if fifo {
					invalidEval = filepath.Join(dir, "evals", "invalid", "eval.yaml")
					require.NoError(t, os.MkdirAll(filepath.Dir(invalidEval), 0o700))
					require.NoError(t, os.WriteFile(invalidEval, []byte("config: [invalid]\n"), 0o600))
				}

				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				args := []string{"-test.run=^TestInitCIHelperProcess$", "-test.count=1"}
				if cover := flag.Lookup("test.gocoverdir"); cover != nil && cover.Value.String() != "" {
					args = append(args, "-test.gocoverdir="+cover.Value.String())
				}
				args = append(args, "--", "--no-telemetry", "--no-update-check", "preflight", "--workspace")
				if tc.override {
					args = append(args, "--skills-dir", tc.skillsRoot)
				}
				cmd := exec.CommandContext(ctx, os.Args[0], args...)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "WAZA_INIT_CI_HELPER=1", "WAZA_INIT_CI_MODE=reject", "NO_COLOR=1")
				out, runErr := cmd.CombinedOutput()
				require.NoError(t, ctx.Err(), "preflight blocked reading a required-skill definition: %s", out)
				require.Contains(t, string(out), "Offline preflight: "+eval)
				require.NoFileExists(t, filepath.Join(dir, "runtime-invoked"))
				if !fifo {
					require.NoError(t, runErr, string(out))
					require.NotContains(t, string(out), "invalid skill.required")
					return
				}

				var exitErr *exec.ExitError
				require.ErrorAs(t, runErr, &exitErr, string(out))
				if tc.explicit {
					require.Equal(t, 1, exitErr.ExitCode(), string(out))
				} else {
					require.Equal(t, ExitError, exitErr.ExitCode(), string(out))
				}
				require.Contains(t, string(out), definition)
				require.Contains(t, string(out), "invalid skill.required")
				require.Contains(t, string(out), "Offline preflight: "+invalidEval)
				require.Contains(t, string(out), "invalid eval.schema")
				require.NotContains(t, string(out), "workspace.empty")
			})
		}
	}
}
