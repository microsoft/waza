package webserver

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunnerTransportRunFilter(t *testing.T) {
	const name = "^TestRunnerProductionTransportAdmission$"
	for _, tc := range []struct {
		filter, want string
	}{
		{"", name},
		{"Transport", name},
		{"Other", "^$"},
		{name + "/^run$/^json-complete$", name + "/^run$/^json-complete$"},
		{"Other/a|Transport/run/json", name + "/run/json"},
		{"Transport/run/json|Other/a|Transport/validate/charset", name + "/run/json|" + name + "/validate/charset"},
		{"(Other|Transport)/r[u/]n/json", name + "/r[u/]n/json"},
		{"Transport/(run|validate)/(json|charset)-complete", name + "/(run|validate)/(json|charset)-complete"},
		{`Transport/r\/un/json`, name + `/r\/un/json`},
		{"]Transport/run/json", "^$"},
	} {
		t.Run(tc.filter, func(t *testing.T) {
			got, err := runnerTransportRunFilter(tc.filter)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
	for _, invalid := range []string{"[", "+|Transport"} {
		_, err := runnerTransportRunFilter(invalid)
		require.Error(t, err)
	}
}

func TestRunnerProductionTransportAdmissionFilters(t *testing.T) {
	const root = "TestRunnerProductionTransportAdmission"
	for _, tc := range []struct {
		name, run, skip string
		want            []string
		unitTest        bool
	}{
		{"nested-run", "^" + root + "$/^run$/^json-complete$", "", []string{"run/json-complete"}, false},
		{"nested-skip", "^" + root + "$/^(run|validate)$/^(json-complete|charset-complete)$",
			"^" + root + "$/^validate$|^" + root + "$/^run$/^charset-complete$", []string{"run/json-complete"}, false},
		{"case-skip", "^" + root + "$/^run$", "^" + root + "$/^run$/.*partial$",
			[]string{"run/missing-complete", "run/wrong-complete", "run/json-complete", "run/charset-complete"}, false},
		{"no-actions", "^" + root + "$", "^" + root + "$/^(run|validate|negotiate)$", nil, false},
		{"top-level-restriction", "^" + root + "$/^run$/^json-complete$|^TestRunnerTransportRunFilter$",
			"", []string{"run/json-complete"}, true},
		{"slash-in-brackets", "^" + root + "$/^r[u/]n$/^json-complete$", "", []string{"run/json-complete"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, covered := range []bool{false, true} {
				t.Run(fmt.Sprintf("coverage-%t", covered), func(t *testing.T) {
					if covered && testing.CoverMode() == "" {
						t.Skip("requires an instrumented test binary")
					}
					args := []string{"-test.run=" + tc.run, "-test.skip=" + tc.skip, "-test.v"}
					var profile string
					if covered {
						dir := t.TempDir()
						profile = filepath.Join(dir, "coverage.txt")
						args = append(args, "-test.gocoverdir="+dir, "-test.coverprofile="+profile)
					}
					command := exec.CommandContext(t.Context(), os.Args[0], args...)
					command.Env = append(os.Environ(), runnerTransportActionEnv+"=")
					output, err := command.CombinedOutput()
					require.NoError(t, err, "%s", output)
					var got []string
					for _, match := range regexp.MustCompile(`=== RUN   `+root+`/([^/\s]+/[^/\s]+)\s`).FindAllStringSubmatch(string(output), -1) {
						got = append(got, match[1])
					}
					require.ElementsMatch(t, tc.want, got, "%s", output)
					unitRuns := strings.Count(string(output), "=== RUN   TestRunnerTransportRunFilter\n")
					if tc.unitTest {
						require.Equal(t, 1, unitRuns, "unrelated top-level test must run only in the parent: %s", output)
					} else {
						require.Zero(t, unitRuns, "%s", output)
					}
					if covered {
						data, err := os.ReadFile(profile)
						require.NoError(t, err)
						require.True(t, strings.HasPrefix(string(data), "mode: "+testing.CoverMode()+"\n"))
						var transportEntries, observed int
						for _, line := range strings.Split(string(data), "\n")[1:] {
							fields := strings.Fields(line)
							if len(fields) == 0 {
								continue
							}
							require.Len(t, fields, 3)
							if !strings.HasPrefix(fields[0], "github.com/microsoft/waza/internal/webserver/") {
								continue
							}
							transportEntries++
							count, err := strconv.ParseUint(fields[2], 10, 64)
							require.NoError(t, err)
							if count > 0 {
								observed++
							}
						}
						require.Positive(t, transportEntries, "profile must include transport-package instrumentation")
						if len(tc.want) == 0 {
							require.Zero(t, observed, "no selected action must execute production code")
						} else {
							require.Positive(t, observed, "selected child production coverage must reach the exported profile")
						}
					}
				})
			}
		})
	}
}
