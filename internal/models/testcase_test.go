package models

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadTestCase_ShouldTriggerField(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantNil bool
		wantVal bool
	}{
		{
			name: "should_trigger true",
			yaml: `id: tc-trigger-true
name: Trigger True
inputs:
  prompt: "test prompt"
expected:
  should_trigger: true
`,
			wantNil: false,
			wantVal: true,
		},
		{
			name: "should_trigger false",
			yaml: `id: tc-trigger-false
name: Trigger False
inputs:
  prompt: "test prompt"
expected:
  should_trigger: false
`,
			wantNil: false,
			wantVal: false,
		},
		{
			name: "should_trigger omitted",
			yaml: `id: tc-trigger-omit
name: Trigger Omitted
inputs:
  prompt: "test prompt"
`,
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "tc.yaml")
			if err := os.WriteFile(p, []byte(tt.yaml), 0o644); err != nil {
				t.Fatalf("write file: %v", err)
			}

			tc, err := LoadTestCase(p)
			if err != nil {
				t.Fatalf("LoadTestCase: %v", err)
			}

			if tt.wantNil {
				if tc.Expectation.ExpectedTrigger != nil {
					t.Errorf("expected ExpectedTrigger nil, got %v", *tc.Expectation.ExpectedTrigger)
				}
				return
			}

			if tc.Expectation.ExpectedTrigger == nil {
				t.Fatal("expected ExpectedTrigger non-nil, got nil")
			}
			if *tc.Expectation.ExpectedTrigger != tt.wantVal {
				t.Errorf("ExpectedTrigger = %v, want %v", *tc.Expectation.ExpectedTrigger, tt.wantVal)
			}
		})
	}
}
