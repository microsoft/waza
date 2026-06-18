package hooks

import (
	"context"
	"runtime"
	"testing"
	"time"
)

func TestRunHook(t *testing.T) {
	// Determine a portable true/false command
	trueCmd := "true"
	falseCmd := "false"
	if runtime.GOOS == "windows" {
		trueCmd = "cmd /c exit 0"
		falseCmd = "cmd /c exit 1"
	}

	tests := []struct {
		name      string
		hook      HookConfig
		wantErr   bool
		errSubstr string
	}{
		{
			name:    "happy path - command succeeds",
			hook:    HookConfig{Command: trueCmd},
			wantErr: false,
		},
		{
			name:      "empty command returns error",
			hook:      HookConfig{Command: ""},
			wantErr:   true,
			errSubstr: "empty command",
		},
		{
			name:      "whitespace-only command returns error",
			hook:      HookConfig{Command: "   "},
			wantErr:   true,
			errSubstr: "empty command",
		},
		{
			name:    "non-zero exit with error_on_fail true returns error",
			hook:    HookConfig{Command: falseCmd, ErrorOnFail: true},
			wantErr: true,
		},
		{
			name:    "non-zero exit with error_on_fail false continues",
			hook:    HookConfig{Command: falseCmd, ErrorOnFail: false},
			wantErr: false,
		},
		{
			name:    "custom acceptable exit codes",
			hook:    HookConfig{Command: falseCmd, ExitCodes: []int{1}, ErrorOnFail: true},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &Runner{Verbose: false}
			err := r.runHook(context.Background(), "test", 0, tc.hook)

			if tc.wantErr && err == nil {
				t.Fatalf("expected error but got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.errSubstr != "" && err != nil {
				if got := err.Error(); !contains(got, tc.errSubstr) {
					t.Errorf("error %q does not contain %q", got, tc.errSubstr)
				}
			}
		})
	}
}

func TestExecute_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	r := &Runner{Verbose: false}
	hooks := []HookConfig{
		{Command: "echo hello"},
	}

	err := r.Execute(ctx, "test", hooks)
	if err == nil {
		t.Fatal("expected context cancellation error but got nil")
	}

	if got := err.Error(); !contains(got, "context canceled") {
		t.Errorf("error %q does not mention context cancellation", got)
	}
}

func TestExecute_ContextTimeout(t *testing.T) {
	// Use a deadline already in the past so the context is canceled
	// synchronously by WithDeadline (dur <= 0), avoiding a race against the
	// deadline timer goroutine that could otherwise let Execute observe a
	// not-yet-expired context on a loaded runner (flaky on Windows CI).
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel()

	r := &Runner{Verbose: false}
	hooks := []HookConfig{
		{Command: "echo hello"},
	}

	err := r.Execute(ctx, "test", hooks)
	if err == nil {
		t.Fatal("expected context timeout error but got nil")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
