package commandmock

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func TestSanitizeArgsSensitiveNames(t *testing.T) {
	tests := []struct {
		name string
		args []string
		env  []string
		want []string
	}{
		{
			name: "nonsecret flags do not hide following arguments",
			args: []string{"--monkey", "banana", "--turkey", "bird", "--keyboard-layout", "us", "--authentication-type", "managed"},
			want: []string{"--monkey", "banana", "--turkey", "bird", "--keyboard-layout", "us", "--authentication-type", "managed"},
		},
		{
			name: "nonsecret environment names",
			args: []string{"us", "alice", "tenant", "cache-count"},
			env:  []string{"KEYBOARD_LAYOUT=us", "AUTHOR=alice", "AUTHORITY=tenant", "TOKEN_COUNT=cache-count"},
			want: []string{"us", "alice", "tenant", "cache-count"},
		},
		{
			name: "positional words are not flags",
			args: []string{"auth", "token", "key", "ordinary"},
			want: []string{"auth", "token", "key", "ordinary"},
		},
		{
			name: "sensitive flags with separators and case variants",
			args: []string{"--PASSWORD", "password-value", "--access-token=token-value", "--api_key", "key-value", "show"},
			want: []string{redactedArg, redactedArg, "--access-token=" + redactedArg, redactedArg, redactedArg, "show"},
		},
		{
			name: "known camel case flags",
			args: []string{"--clientSecret", "secret-value", "--accessToken=token-value", "--connectionString=connection-value"},
			want: []string{redactedArg, redactedArg, "--accessToken=" + redactedArg, "--connectionString=" + redactedArg},
		},
		{
			name: "sensitive environment suffixes",
			args: []string{"prefix-secret-value-suffix", "token-value", "password-value", "api-value", "access-value", "account-value"},
			env: []string{
				"AZURE_CLIENT_SECRET=secret-value", "GITHUB_TOKEN=token-value", "DB_PASSWORD=password-value",
				"OPENAI_API_KEY=api-value", "AWS_SECRET_ACCESS_KEY=access-value", "AZURE_STORAGE_ACCOUNT_KEY=account-value",
			},
			want: []string{"prefix-" + redactedArg + "-suffix", redactedArg, redactedArg, redactedArg, redactedArg, redactedArg},
		},
		{
			name: "short secrets are still protected",
			args: []string{"x"},
			env:  []string{"CLIENT_SECRET=x"},
			want: []string{redactedArg},
		},
		{
			name: "token-shaped positional value",
			args: []string{"ghp_" + strings.Repeat("a", 25)},
			want: []string{redactedArg},
		},
		{
			name: "empty and malformed environment values",
			args: []string{"ordinary"},
			env:  []string{"TOKEN=", "SECRET"},
			want: []string{"ordinary"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeArgs(tt.args, tt.env); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("sanitizeArgs() = %#v, want %#v", got, tt.want)
			}
		})
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
