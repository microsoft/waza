package scripts

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestPowerShellBuildNativeFree(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("production PowerShell entrypoint execution is covered on Windows")
	}
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Fatal("Windows build entrypoint coverage requires pwsh:", err)
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "build.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	source = []byte(strings.ReplaceAll(string(source), "\r\n", "\n"))
	for _, platform := range []string{"linux/amd64", ""} {
		t.Run(platformName(platform), func(t *testing.T) {
			dir := t.TempDir()
			record := filepath.Join(dir, "calls.jsonl")
			t.Setenv("PLATFORM", platform)
			t.Setenv("OUTPUT_DIR", filepath.Join(dir, "bin"))
			t.Setenv("WAZA_BUILD_CALLS", record)
			t.Setenv("WAZA_BUILD_SCRIPT", script)
			t.Setenv("CGO_ENABLED", "0")
			// Intercept only the compiler, executing the production script's loop,
			// platform selection, argument construction and environment handling.
			command := `
function global:go {
    [pscustomobject]@{ Args = @($args); OS = $env:GOOS; Arch = $env:GOARCH; Cgo = $env:CGO_ENABLED } |
        ConvertTo-Json -Compress | Add-Content -LiteralPath $env:WAZA_BUILD_CALLS
    $global:LASTEXITCODE = 0
}
& $env:WAZA_BUILD_SCRIPT
`
			run := func() []powerShellBuildCall {
				t.Helper()
				output, err := exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-Command", command).CombinedOutput()
				if err != nil {
					t.Fatalf("build.ps1: %v\n%s", err, output)
				}
				data, err := os.ReadFile(record)
				if err != nil {
					t.Fatal(err)
				}
				var calls []powerShellBuildCall
				for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
					var call powerShellBuildCall
					if err := json.Unmarshal([]byte(line), &call); err != nil {
						t.Fatal(err)
					}
					calls = append(calls, call)
				}
				return calls
			}
			expected := []string{platform}
			if platform == "" {
				expected = []string{"windows/amd64", "windows/arm64", "darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64"}
			}
			calls := run()
			if len(calls) != len(expected) {
				t.Fatalf("got %d build calls, want %d", len(calls), len(expected))
			}
			for i, call := range calls {
				if call.OS+"/"+call.Arch != expected[i] || call.Cgo != "0" {
					t.Fatalf("unexpected build environment: %+v", call)
				}
				if !call.nativeFree() {
					t.Fatalf("production entrypoint must exclude in-process FFI: %+v", call)
				}
			}
			// Prove the same invocation check rejects the original regression.
			mutated := strings.Replace(string(source), "            -tags copilot_no_inprocess `\n", "", 1)
			if mutated == string(source) {
				t.Fatal("negative control did not remove the tag")
			}
			negative := filepath.Join(dir, "untagged.ps1")
			if err := os.WriteFile(negative, []byte(mutated), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("WAZA_BUILD_SCRIPT", negative)
			if err := os.Remove(record); err != nil {
				t.Fatal(err)
			}
			for _, call := range run() {
				if call.nativeFree() {
					t.Fatal("untagged production invocation passed the native-free check")
				}
			}
		})
	}
}

type powerShellBuildCall struct {
	Args []string
	OS   string
	Arch string
	Cgo  string
}

func (c powerShellBuildCall) nativeFree() bool {
	tag := slices.Index(c.Args, "-tags")
	return len(c.Args) > 0 && c.Args[0] == "build" &&
		tag >= 0 && tag+1 < len(c.Args) && c.Args[tag+1] == "copilot_no_inprocess" &&
		c.Args[len(c.Args)-1] == "./cmd/waza"
}

func platformName(platform string) string {
	if platform == "" {
		return "all-platforms"
	}
	return platform
}
