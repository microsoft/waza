package commandmock

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/microsoft/waza/internal/models"
)

func TestInvokeExactAndNonZeroResponse(t *testing.T) {
	root := commandMockTestRoot(t)
	workspace := t.TempDir()
	secret := "test-secret-value"
	accountKey := "test-account-key-value"
	t.Setenv("AZURE_CLIENT_SECRET", secret)
	t.Setenv("AZURE_STORAGE_ACCOUNT_KEY", accountKey)
	expectedCalls := 3
	session, err := NewSession(workspace, []models.CommandMockConfig{{
		Name:        "az",
		ExpectCalls: &expectedCalls,
		Responses: []models.CommandMockResponse{
			{Args: []string{"account", "show"}, Stdout: map[string]string{"name": "Test Subscription"}},
			{Args: []string{"--client-secret", secret}, Stderr: "deployment failed", ExitCode: 2},
			{Args: []string{"--account-key", accountKey}},
		},
	}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	result, err := Invoke(root, session.ID(), "az", []string{"account", "show"}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(result.Stdout), `{"name":"Test Subscription"}`; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", result.ExitCode)
	}

	result, err = Invoke(root, session.ID(), "az", []string{"--client-secret", secret}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stderr != "deployment failed" || result.ExitCode != 2 {
		t.Fatalf("unexpected non-zero response: %+v", result)
	}

	if _, err := Invoke(root, session.ID(), "az", []string{"--account-key", accountKey}, workspace); err != nil {
		t.Fatal(err)
	}

	invocations, err := session.Close()
	if err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if len(invocations) != expectedCalls {
		t.Fatalf("invocation count = %d, want %d", len(invocations), expectedCalls)
	}
	if strings.Contains(strings.Join(invocations[1].Args, " "), secret) {
		t.Fatalf("secret was recorded in invocation: %+v", invocations[1])
	}
	if strings.Contains(strings.Join(invocations[2].Args, " "), accountKey) {
		t.Fatalf("account key was recorded in invocation: %+v", invocations[2])
	}
	if _, err := os.Stat(session.dir); !os.IsNotExist(err) {
		t.Fatalf("task mock state was not removed: %v", err)
	}
}

func TestInvokeRegexEnvironmentWorkDirAndFixture(t *testing.T) {
	root := commandMockTestRoot(t)
	baseDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(baseDir, "fixtures"), 0700); err != nil {
		t.Fatal(err)
	}
	fixture := []byte("{\"id\":\"rg-test\"}\n")
	if err := os.WriteFile(filepath.Join(baseDir, "fixtures", "group.json"), fixture, 0600); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	workdir := filepath.Join(workspace, "deploy")
	if err := os.Mkdir(workdir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WAZA_FIXTURE_ENV", "enabled")

	session, err := NewSession(workspace, []models.CommandMockConfig{{
		Name: "az",
		Responses: []models.CommandMockResponse{{
			ArgsRegex:   []string{"group", "show", ".+"},
			Environment: map[string]string{"WAZA_FIXTURE_ENV": "enabled"},
			WorkDir:     "deploy",
			Fixture:     "fixtures/group.json",
		}, {
			Args:    []string{"root"},
			WorkDir: ".",
			Stdout:  "workspace root",
		}, {
			Args:   []string{"anywhere"},
			Stdout: "unconstrained",
		}},
	}}, baseDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := session.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	result, err := Invoke(root, session.ID(), "az", []string{"group", "show", "rg-test"}, workdir)
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Stdout) != string(fixture) {
		t.Fatalf("fixture output = %q, want %q", result.Stdout, fixture)
	}

	result, err = Invoke(root, session.ID(), "az", []string{"group", "show", "rg-test"}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 127 || !strings.Contains(result.Stderr, "command_mocks.responses fixture") {
		t.Fatalf("unmatched working directory should fail closed, got %+v", result)
	}

	result, err = Invoke(root, session.ID(), "az", []string{"root"}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(result.Stdout); got != "workspace root" {
		t.Fatalf("root response = %q, want %q", got, "workspace root")
	}
	result, err = Invoke(root, session.ID(), "az", []string{"root"}, workdir)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 127 {
		t.Fatalf("explicit root workdir matched a subdirectory: %+v", result)
	}

	result, err = Invoke(root, session.ID(), "az", []string{"anywhere"}, workdir)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(result.Stdout); got != "unconstrained" {
		t.Fatalf("omitted workdir response = %q, want %q", got, "unconstrained")
	}
}

func TestInvokeUsesSessionIdentityOutsideWorkspace(t *testing.T) {
	root := commandMockTestRoot(t)
	workspace := t.TempDir()
	session, err := NewSession(workspace, []models.CommandMockConfig{{
		Name: "az",
		Responses: []models.CommandMockResponse{{
			Args:   []string{"account", "show"},
			Stdout: "mocked",
		}},
	}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := session.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	result, err := Invoke(root, session.ID(), "az", []string{"account", "show"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := string(result.Stdout); got != "mocked" {
		t.Fatalf("stdout = %q, want %q", got, "mocked")
	}

	result, err = Invoke(root, session.ID(), "gh", []string{"repo", "view"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !result.PassThrough {
		t.Fatalf("unmocked command should pass through: %+v", result)
	}
}

func TestUnmatchedInvocationDiagnosticIncludesSanitizedContext(t *testing.T) {
	root := commandMockTestRoot(t)
	workspace := t.TempDir()
	secret := "diagnostic-secret-value"
	t.Setenv("AZURE_CLIENT_SECRET", secret)
	cwd := filepath.Join(workspace, secret)
	session, err := NewSession(workspace, []models.CommandMockConfig{{
		Name: "az",
		Responses: []models.CommandMockResponse{{
			Args:    []string{"group", "show"},
			WorkDir: ".",
		}},
	}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := session.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	for _, tt := range []struct {
		args []string
		want []string
	}{
		{args: []string{"group", "show"}, want: []string{"group", "show"}},
		{args: []string{"login", "--client-secret", secret}, want: []string{"login", redactedArg, redactedArg}},
		{},
	} {
		t.Run(fmt.Sprintf("%q", tt.want), func(t *testing.T) {
			result, err := Invoke(root, session.ID(), "az", tt.args, cwd)
			if err != nil {
				t.Fatal(err)
			}
			if result.ExitCode != 127 || result.ResponseIndex != -1 {
				t.Fatalf("unmatched invocation should fail closed: %+v", result)
			}
			wantArgs := fmt.Sprintf("arguments %q", tt.want)
			wantCWD := fmt.Sprintf("working directory %q", strings.ReplaceAll(cwd, secret, redactedArg))
			if !strings.Contains(result.Stderr, wantArgs) || !strings.Contains(result.Stderr, wantCWD) {
				t.Fatalf("missing diagnostic context: %s", result.Stderr)
			}
			if strings.Contains(result.Stderr, secret) {
				t.Fatalf("diagnostic leaked a secret: %s", result.Stderr)
			}
		})
	}
}

func TestInvokeDoesNotCrossTaskStateInParallel(t *testing.T) {
	root := commandMockTestRoot(t)
	const calls = 20
	type taskState struct {
		workspace string
		session   *Session
		want      string
	}
	tasks := []taskState{
		{workspace: t.TempDir(), want: "first"},
		{workspace: t.TempDir(), want: "second"},
	}
	for i := range tasks {
		session, err := NewSession(tasks[i].workspace, []models.CommandMockConfig{{
			Name:        "gh",
			ExpectCalls: intPointer(calls),
			Responses: []models.CommandMockResponse{{
				Args:   []string{"repo", "view"},
				Stdout: tasks[i].want,
			}},
		}}, t.TempDir())
		if err != nil {
			t.Fatalf("NewSession() error = %v", err)
		}
		tasks[i].session = session
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(tasks)*calls)
	for _, task := range tasks {
		for range calls {
			wg.Add(1)
			go func(task taskState) {
				defer wg.Done()
				result, err := Invoke(root, task.session.ID(), "gh", []string{"repo", "view"}, task.workspace)
				if err != nil {
					errs <- err
				} else if got := string(result.Stdout); got != task.want {
					errs <- fmt.Errorf("workspace %s got %q, want %q", task.workspace, got, task.want)
				}
			}(task)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	for _, task := range tasks {
		invocations, err := task.session.Close()
		if err != nil {
			t.Errorf("Close() error = %v", err)
		}
		if len(invocations) != calls {
			t.Errorf("workspace %s recorded %d calls, want %d", task.workspace, len(invocations), calls)
		}
	}
}

func TestRenderShimForSupportedPlatforms(t *testing.T) {
	unix := renderShimFor("linux", "az")
	if !strings.Contains(unix, `exec "$WAZA_COMMAND_MOCK_EXECUTABLE"`) || !strings.Contains(unix, `"$@"`) {
		t.Fatalf("unexpected Unix shim: %q", unix)
	}
	windows := renderShimFor("windows", "az")
	if !strings.Contains(windows, "%WAZA_COMMAND_MOCK_EXECUTABLE%") || !strings.Contains(windows, "%*") {
		t.Fatalf("unexpected Windows shim: %q", windows)
	}
	if got := filepath.Base(shimPathFor("windows", t.TempDir(), "az")); got != "az.cmd" {
		t.Fatalf("Windows shim name = %q, want az.cmd", got)
	}
	for _, goos := range []string{"darwin", "linux"} {
		if got := filepath.Base(shimPathFor(goos, t.TempDir(), "az")); got != "az" {
			t.Errorf("%s shim name = %q, want az", goos, got)
		}
	}
}

func commandMockTestRoot(t *testing.T) string {
	t.Helper()
	_, err := RuntimeEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	root, _, err := runtimePaths()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := CloseRuntime(); err != nil {
			t.Errorf("CloseRuntime() error = %v", err)
		}
	})
	return root
}

func intPointer(value int) *int {
	return &value
}

func TestNilSessionInvocations(t *testing.T) {
	var session *Session
	if got := session.Invocations(); got != nil {
		t.Fatalf("nil session invocations = %#v, want nil", got)
	}
}
