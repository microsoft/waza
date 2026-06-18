package failures

import (
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
)

func TestCaptureFailure(t *testing.T) {
	handler := NewHandler()

	result := &models.RunResult{
		RunNumber: 1,
		Status:    models.StatusFailed,
		ErrorMsg:  "validation failed",
		Validations: map[string]models.GraderResults{
			"validator1": {
				Name:   "validator1",
				Passed: false,
			},
			"validator2": {
				Name:   "validator2",
				Passed: true,
			},
		},
	}

	stderr := "Error: permission denied\nFailed to access file"
	stdout := "Starting evaluation...\nFailed at step 3"

	handler.CaptureFailure(result, 1, stderr, stdout)

	if result.FailureArtifacts == nil {
		t.Fatal("FailureArtifacts should not be nil")
	}

	if result.FailureArtifacts.StdErr != stderr {
		t.Errorf("Expected stderr to be captured, got %q", result.FailureArtifacts.StdErr)
	}

	if result.FailureArtifacts.StdOut != stdout {
		t.Errorf("Expected stdout to be captured, got %q", result.FailureArtifacts.StdOut)
	}

	if result.FailureArtifacts.ExitCode != 1 {
		t.Errorf("Expected ExitCode 1, got %d", result.FailureArtifacts.ExitCode)
	}

	if len(result.FailureArtifacts.FailedGraders) != 1 {
		t.Errorf("Expected 1 failed grader, got %d", len(result.FailureArtifacts.FailedGraders))
	}

	if result.FailureArtifacts.FailedGraders[0] != "validator1" {
		t.Errorf("Expected failed grader to be 'validator1', got %q", result.FailureArtifacts.FailedGraders[0])
	}

	if result.FailureArtifacts.TriageSummary == "" {
		t.Error("TriageSummary should not be empty")
	}

	if !strings.Contains(result.FailureArtifacts.TriageSummary, "Failed") {
		t.Error("TriageSummary should contain 'Failed'")
	}
}

func TestCaptureFailureNilResultDoesNotPanic(t *testing.T) {
	handler := NewHandler()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("CaptureFailure should not panic on nil result: %v", r)
		}
	}()

	handler.CaptureFailure(nil, 1, "stderr", "stdout")
}
func TestExtractErrorPatterns(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		stdout string
		errMsg string
		expect []string
	}{
		{
			name:   "permission denied",
			stderr: "permission denied",
			stdout: "",
			errMsg: "",
			expect: []string{"permission denied"},
		},
		{
			name:   "timeout",
			stderr: "timeout waiting for response",
			stdout: "",
			errMsg: "",
			expect: []string{"timeout"},
		},
		{
			name:   "multiple patterns",
			stderr: "Error: file not found",
			stdout: "Connection refused",
			errMsg: "",
			expect: []string{"file not found", "connection refused"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patterns := extractErrorPatterns(tt.stderr, tt.stdout, tt.errMsg)
			if len(patterns) == 0 && len(tt.expect) > 0 {
				t.Errorf("Expected patterns %v, got empty", tt.expect)
			}
			for _, expected := range tt.expect {
				found := false
				for _, p := range patterns {
					if strings.Contains(p, expected) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected pattern %q not found in %v", expected, patterns)
				}
			}
		})
	}
}

func TestGenerateTriageSummary(t *testing.T) {
	result := &models.RunResult{
		Status:   models.StatusFailed,
		ErrorMsg: "Test failed",
		Validations: map[string]models.GraderResults{
			"test": {Name: "test", Passed: false},
		},
	}

	artifacts := &models.FailureArtifacts{
		StdErr:        "Error in test",
		FailedGraders: []string{"test"},
		ErrorPatterns: []string{"error"},
		Context:       make(map[string]string),
	}

	summary := generateTriageSummary(artifacts, result)

	if summary == "" {
		t.Fatal("Summary should not be empty")
	}

	checks := []string{"Failed", "test", "error", "Recommendations"}
	for _, check := range checks {
		if !strings.Contains(summary, check) {
			t.Errorf("Expected summary to contain %q, got:\n%s", check, summary)
		}
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input   string
		maxLen  int
		wantLen int
	}{
		{
			input:   "short",
			maxLen:  100,
			wantLen: 5,
		},
		{
			input:   "this is a much longer string that should be truncated",
			maxLen:  30,
			wantLen: 30,
		},
		{
			input:   "this is a much longer string that should be truncated",
			maxLen:  10,
			wantLen: 10,
		},
		{
			input:   "this is a much longer string that should be truncated",
			maxLen:  0,
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		result := truncate(tt.input, tt.maxLen)
		if len(result) != tt.wantLen {
			t.Errorf("truncate(%q, %d): got len %d, want %d (result: %q)",
				tt.input, tt.maxLen, len(result), tt.wantLen, result)
		}
	}
}
