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
	var instrumentedEntries int
	if testing.CoverMode() != "" {
		dir := t.TempDir()
		profile := filepath.Join(dir, "baseline.txt")
		// A no-test child identifies instrumentation without executing transport code.
		command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^$", "-test.gocoverdir="+dir, "-test.coverprofile="+profile)
		command.Env = append(os.Environ(), runnerTransportActionEnv+"=")
		output, err := command.CombinedOutput()
		require.NoError(t, err, "%s", output)
		data, err := os.ReadFile(profile)
		require.NoError(t, err)
		var observed int
		instrumentedEntries, observed, err = runnerTransportCoverageCounts(string(data), testing.CoverMode())
		require.NoError(t, err)
		require.Zero(t, observed, "instrumentation baseline must not execute transport code")
	}
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
						transportEntries, observed, err := runnerTransportCoverageCounts(string(data), testing.CoverMode())
						require.NoError(t, err)
						require.Equal(t, instrumentedEntries, transportEntries, "profile must retain the binary's transport instrumentation")
						if instrumentedEntries == 0 {
							t.Log("transport coverage unavailable: webserver was not instrumented; child emission checks still apply")
							return
						}
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

func runnerTransportCoverageCounts(profile, mode string) (transportEntries, observed int, err error) {
	lines := strings.Split(profile, "\n")
	if lines[0] != "mode: "+mode {
		return 0, 0, fmt.Errorf("unexpected coverage mode: %q", lines[0])
	}
	location := regexp.MustCompile(`^.+:\d+\.\d+,\d+\.\d+$`)
	var entries int
	for _, line := range lines[1:] {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 || !location.MatchString(fields[0]) {
			return 0, 0, fmt.Errorf("invalid coverage entry: %q", line)
		}
		if _, err := strconv.ParseUint(fields[1], 10, 64); err != nil {
			return 0, 0, fmt.Errorf("parsing coverage statement count: %w", err)
		}
		count, err := strconv.ParseUint(fields[2], 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("parsing coverage execution count: %w", err)
		}
		entries++
		if strings.HasPrefix(fields[0], "github.com/microsoft/waza/internal/webserver/") {
			transportEntries++
			if count > 0 {
				observed++
			}
		}
	}
	if entries == 0 {
		return 0, 0, fmt.Errorf("instrumented coverage profile has no entries")
	}
	return transportEntries, observed, nil
}

func TestRunnerTransportCoverageCounts(t *testing.T) {
	const transport = "github.com/microsoft/waza/internal/webserver/server.go:37.1,38.2 1 "
	const other = "github.com/microsoft/waza/internal/runnercontract/negotiation.go:65.1,71.2 1 "
	for _, tc := range []struct {
		name, profile     string
		entries, observed int
		invalid           bool
	}{
		{"selected", transport + "2\n", 1, 1, false},
		{"skipped", transport + "0\n", 1, 0, false},
		{"other-only", other + "3\n", 0, 0, false},
		{"broad", transport + "0\n" + other + "3\n", 1, 0, false},
		{"near-namespace", strings.ReplaceAll(transport, "/webserver/", "/webserver-extra/") + "3\n", 0, 0, false},
		{"empty", "", 0, 0, true},
		{"malformed-location", "invalid 1 0\n", 0, 0, true},
		{"malformed-statements", strings.ReplaceAll(other, " 1 ", " invalid ") + "0\n", 0, 0, true},
		{"malformed-counter", other + "-1\n", 0, 0, true},
		{"missing-counter", other, 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries, observed, err := runnerTransportCoverageCounts("mode: atomic\n"+tc.profile, "atomic")
			if tc.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.entries, entries)
			require.Equal(t, tc.observed, observed)
		})
	}
	_, _, err := runnerTransportCoverageCounts("mode: set\n"+transport+"0\n", "atomic")
	require.Error(t, err)
}
