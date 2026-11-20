package scripts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The Docker harness supplies exactly one extracted bundle, so it cannot detect
// a positive control that silently picks one of several candidates. These
// controls execute the real probe against synthetic caches and a recording CLI.
func TestNativeBundleProbeSelectsSingleBundle(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	buildDir := t.TempDir()
	probe := filepath.Join(buildDir, "sdk-positive")
	if runtime.GOOS == "windows" {
		probe += ".exe"
	}
	build := exec.CommandContext(t.Context(), "go", "build", "-o", probe, "./.github/scripts/testdata/native-bundle-probe/positive")
	build.Dir = root
	output, err := build.CombinedOutput()
	require.NoError(t, err, "build positive probe: %s", output)

	marker := filepath.Join(buildDir, "launches.log")
	fakeSource := filepath.Join(buildDir, "fakecli")
	require.NoError(t, os.MkdirAll(fakeSource, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(fakeSource, "go.mod"), []byte("module fakecli\n\ngo 1.26\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(fakeSource, "main.go"), []byte(fmt.Sprintf(`package main

import "os"

func main() {
	f, err := os.OpenFile(%q, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		_, _ = f.WriteString(os.Args[0] + "\n")
		_ = f.Close()
	}
	os.Exit(3)
}
`, marker)), 0o600))
	fakeCLI := filepath.Join(buildDir, "fakecli.bin")
	build = exec.CommandContext(t.Context(), "go", "build", "-o", fakeCLI, ".")
	build.Dir = fakeSource
	build.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
	output, err = build.CombinedOutput()
	require.NoError(t, err, "build recording CLI: %s", output)
	fakeBinary, err := os.ReadFile(fakeCLI)
	require.NoError(t, err)

	install := func(t *testing.T, cache, version, directory string) string {
		t.Helper()
		dir := filepath.Join(cache, version, directory)
		require.NoError(t, os.MkdirAll(dir, 0o700))
		path := filepath.Join(dir, "copilot")
		require.NoError(t, os.WriteFile(path, fakeBinary, 0o700))
		if runtime.GOOS == "windows" {
			// os/exec resolves an extensionless Windows path through PATHEXT.
			require.NoError(t, os.WriteFile(path+".exe", fakeBinary, 0o700))
		}
		return path
	}

	for _, control := range []struct {
		name    string
		setup   func(t *testing.T, cache string) []string
		wantErr string
	}{
		{
			name: "missing cache",
			setup: func(t *testing.T, cache string) []string {
				return nil
			},
			wantErr: "read bundled CLI cache:",
		},
		{
			name: "no matching bundle",
			setup: func(t *testing.T, cache string) []string {
				require.NoError(t, os.MkdirAll(cache, 0o700))
				install(t, cache, "7.3.1-arbitrary", "linuxmusl")
				return nil
			},
			wantErr: "ambiguity",
		},
		{
			name: "multiple matching bundles",
			setup: func(t *testing.T, cache string) []string {
				return []string{
					install(t, cache, "0.0.1-first", ""),
					install(t, cache, "9.8.7-second", ""),
				}
			},
			wantErr: "ambiguity",
		},
		{
			name: "single arbitrary version",
			setup: func(t *testing.T, cache string) []string {
				install(t, cache, "3.14.15-arbitrary", "linuxmusl")
				return []string{install(t, cache, "3.14.15-arbitrary", "")}
			},
		},
	} {
		t.Run(control.name, func(t *testing.T) {
			if err := os.Remove(marker); err != nil {
				require.ErrorIs(t, err, os.ErrNotExist, "reset launch log")
			}
			probeRoot := filepath.Join(t.TempDir(), "probe root with spaces")
			cache := filepath.Join(probeRoot, "home", ".copilot", "cache", "copilot-sdk")
			candidates := control.setup(t, cache)

			cmd := exec.CommandContext(t.Context(), probe)
			cmd.WaitDelay = 2 * time.Second
			cmd.Env = []string{
				"PROBE_ROOT=" + probeRoot,
				"PROBE_LIBC=linux",
				"COPILOT_BASE_URL=http://127.0.0.1:1",
				"PATH=" + os.Getenv("PATH"),
				"SYSTEMROOT=" + os.Getenv("SYSTEMROOT"),
			}
			output, err := cmd.CombinedOutput()
			var exitErr *exec.ExitError
			require.ErrorAs(t, err, &exitErr, "probe output: %s", output)
			require.Equal(t, 1, exitErr.ExitCode(), "probe output: %s", output)

			launches, readErr := os.ReadFile(marker)
			if control.wantErr != "" {
				if control.wantErr == "ambiguity" {
					want := fmt.Sprintf("expected exactly one extracted bundled CLI under %s, found %q\n", probeRoot+"/home/.copilot/cache/copilot-sdk", candidates)
					require.Equal(t, want, string(output))
				} else {
					require.True(t, strings.HasPrefix(string(output), control.wantErr), "probe output: %s", output)
				}
				require.ErrorIs(t, readErr, os.ErrNotExist, "probe launched a CLI: %s", launches)
				return
			}
			require.NoError(t, readErr, "probe did not launch the selected CLI: %s", output)
			require.NotEmpty(t, launches)
			for _, launched := range strings.Split(strings.TrimSuffix(string(launches), "\n"), "\n") {
				if runtime.GOOS == "windows" && launched == candidates[0]+".exe" {
					continue
				}
				require.Equal(t, candidates[0], launched)
			}
		})
	}
}
