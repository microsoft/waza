package commandmock

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSanitizeArgsRedactsSensitiveFlagAndValue(t *testing.T) {
	got := sanitizeArgs(
		[]string{"--client-secret", "secret-value", "group", "show"},
		[]string{"AZURE_CLIENT_SECRET=secret-value"},
	)
	want := []string{redactedArg, redactedArg, "group", "show"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sanitizeArgs() = %#v, want %#v", got, want)
	}
}

func TestHostCommandEnvironment(t *testing.T) {
	tests := []struct {
		name string
		env  []string
		want []string
	}{
		{
			name: "removes internal variables and preserves host environment",
			env: []string{
				"PATH=shims",
				rootEnv + "=private-root",
				executableEnv + "=private-executable",
				SessionEnvironmentVariable + "=private-session",
				"WAZA_COMMAND_MOCK_FUTURE=private-value",
				"WAZA_COMMAND_MOCK_ROOT=duplicate-root",
				"AZURE_CLIENT_SECRET=host-secret",
				"WAZA_OTHER=host-value",
			},
			want: []string{"PATH=host-path", "AZURE_CLIENT_SECRET=host-secret", "WAZA_OTHER=host-value"},
		},
		{
			name: "removes case variants",
			env:  []string{"waza_command_mock_root=private-root", "Waza_Command_Mock_Session=private-session"},
			want: []string{"PATH=host-path"},
		},
		{
			name: "adds missing path",
			env:  []string{"KEEP=value"},
			want: []string{"KEEP=value", "PATH=host-path"},
		},
		{
			name: "empty environment",
			want: []string{"PATH=host-path"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := append([]string(nil), tt.env...)
			if got := hostCommandEnvironment(tt.env, "host-path"); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("hostCommandEnvironment() = %#v, want %#v", got, tt.want)
			}
			if !reflect.DeepEqual(tt.env, original) {
				t.Fatalf("input environment changed: %#v, want %#v", tt.env, original)
			}
		})
	}
}

func TestRunHostCommandFiltersEnvironment(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	hostPath := filepath.Dir(executable)
	t.Setenv("PATH", filepath.Join(root, "bin")+string(os.PathListSeparator)+hostPath)
	t.Setenv(rootEnv, root)
	t.Setenv(executableEnv, executable)
	t.Setenv(SessionEnvironmentVariable, "private-session")
	t.Setenv("WAZA_TEST_HOST_COMMAND", "1")
	t.Setenv("WAZA_TEST_HOST_PATH", hostPath)

	for _, exitCode := range []string{"0", "7"} {
		t.Run(exitCode, func(t *testing.T) {
			want := 0
			if exitCode == "7" {
				want = 7
			}
			got := runHostCommand(root, filepath.Base(executable), []string{"-test.run=^TestRunHostCommandHelperProcess$", "--", exitCode})
			if got != want {
				t.Fatalf("runHostCommand() = %d, want %d", got, want)
			}
		})
	}
}

func TestRunHostCommandHelperProcess(t *testing.T) {
	if os.Getenv("WAZA_TEST_HOST_COMMAND") != "1" {
		return
	}
	for _, key := range []string{rootEnv, executableEnv, SessionEnvironmentVariable} {
		if _, present := os.LookupEnv(key); present {
			t.Fatalf("host command received internal variable %s", key)
		}
	}
	if got, want := os.Getenv("PATH"), os.Getenv("WAZA_TEST_HOST_PATH"); got != want {
		t.Fatalf("host PATH = %q, want %q", got, want)
	}
	if os.Args[len(os.Args)-1] == "7" {
		os.Exit(7)
	}
	os.Exit(0)
}
