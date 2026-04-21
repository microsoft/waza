package models

import (
	"os"
	"path/filepath"
	"strings"
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

func TestLoadTestCase_OutputContainsAny(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantLen int
		wantVal []string
	}{
		{
			name: "output_contains_any with multiple values",
			yaml: `id: tc-may-include
name: May Include
inputs:
  prompt: "test"
expected:
  output_contains_any:
    - "hello"
    - "world"
`,
			wantLen: 2,
			wantVal: []string{"hello", "world"},
		},
		{
			name: "output_contains_any omitted",
			yaml: `id: tc-no-may
name: No May Include
inputs:
  prompt: "test"
`,
			wantLen: 0,
		},
		{
			name: "output_contains_any single value",
			yaml: `id: tc-may-single
name: Single May
inputs:
  prompt: "test"
expected:
  output_contains_any:
    - "only"
`,
			wantLen: 1,
			wantVal: []string{"only"},
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

			if len(tc.Expectation.MayInclude) != tt.wantLen {
				t.Errorf("MayInclude length = %d, want %d", len(tc.Expectation.MayInclude), tt.wantLen)
			}
			for i, v := range tt.wantVal {
				if i < len(tc.Expectation.MayInclude) && tc.Expectation.MayInclude[i] != v {
					t.Errorf("MayInclude[%d] = %q, want %q", i, tc.Expectation.MayInclude[i], v)
				}
			}
		})
	}
}

func TestLoadTestCase_PromptFile(t *testing.T) {
	t.Run("loads prompt from file", func(t *testing.T) {
		dir := t.TempDir()

		promptContent := "Explain the Go concurrency model in detail."
		if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte(promptContent), 0o644); err != nil {
			t.Fatalf("write prompt file: %v", err)
		}

		yamlContent := `id: tc-prompt-file
name: Prompt From File
inputs:
  prompt_file: prompt.md
`
		tcPath := filepath.Join(dir, "tc.yaml")
		if err := os.WriteFile(tcPath, []byte(yamlContent), 0o644); err != nil {
			t.Fatalf("write test case: %v", err)
		}

		tc, err := LoadTestCase(tcPath)
		if err != nil {
			t.Fatalf("LoadTestCase: %v", err)
		}

		if tc.Stimulus.Message != promptContent {
			t.Errorf("Message = %q, want %q", tc.Stimulus.Message, promptContent)
		}
	})

	t.Run("loads prompt from subdirectory", func(t *testing.T) {
		dir := t.TempDir()

		promptDir := filepath.Join(dir, "prompts")
		if err := os.MkdirAll(promptDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		promptContent := "Analyze the code in the workspace."
		if err := os.WriteFile(filepath.Join(promptDir, "analyze.md"), []byte(promptContent), 0o644); err != nil {
			t.Fatalf("write prompt file: %v", err)
		}

		yamlContent := `id: tc-subdir
name: Prompt From Subdirectory
inputs:
  prompt_file: prompts/analyze.md
`
		tcPath := filepath.Join(dir, "tc.yaml")
		if err := os.WriteFile(tcPath, []byte(yamlContent), 0o644); err != nil {
			t.Fatalf("write test case: %v", err)
		}

		tc, err := LoadTestCase(tcPath)
		if err != nil {
			t.Fatalf("LoadTestCase: %v", err)
		}

		if tc.Stimulus.Message != promptContent {
			t.Errorf("Message = %q, want %q", tc.Stimulus.Message, promptContent)
		}
	})

	t.Run("error when both prompt and prompt_file set", func(t *testing.T) {
		dir := t.TempDir()

		if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("file prompt"), 0o644); err != nil {
			t.Fatalf("write prompt file: %v", err)
		}

		yamlContent := `id: tc-both
name: Both Set
inputs:
  prompt: "inline prompt"
  prompt_file: prompt.md
`
		tcPath := filepath.Join(dir, "tc.yaml")
		if err := os.WriteFile(tcPath, []byte(yamlContent), 0o644); err != nil {
			t.Fatalf("write test case: %v", err)
		}

		_, err := LoadTestCase(tcPath)
		if err == nil {
			t.Fatal("expected error when both prompt and prompt_file are set")
		}
		if !strings.Contains(err.Error(), "cannot specify both prompt and prompt_file") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("error when prompt_file does not exist", func(t *testing.T) {
		dir := t.TempDir()

		yamlContent := `id: tc-missing
name: Missing File
inputs:
  prompt_file: nonexistent.md
`
		tcPath := filepath.Join(dir, "tc.yaml")
		if err := os.WriteFile(tcPath, []byte(yamlContent), 0o644); err != nil {
			t.Fatalf("write test case: %v", err)
		}

		_, err := LoadTestCase(tcPath)
		if err == nil {
			t.Fatal("expected error when prompt_file does not exist")
		}
		if !strings.Contains(err.Error(), "reading prompt_file") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("inline prompt still works", func(t *testing.T) {
		dir := t.TempDir()

		yamlContent := `id: tc-inline
name: Inline Prompt
inputs:
  prompt: "inline prompt text"
`
		tcPath := filepath.Join(dir, "tc.yaml")
		if err := os.WriteFile(tcPath, []byte(yamlContent), 0o644); err != nil {
			t.Fatalf("write test case: %v", err)
		}

		tc, err := LoadTestCase(tcPath)
		if err != nil {
			t.Fatalf("LoadTestCase: %v", err)
		}

		if tc.Stimulus.Message != "inline prompt text" {
			t.Errorf("Message = %q, want %q", tc.Stimulus.Message, "inline prompt text")
		}
	})

	t.Run("neither prompt nor prompt_file is valid", func(t *testing.T) {
		dir := t.TempDir()

		yamlContent := `id: tc-empty
name: No Prompt
inputs: {}
`
		tcPath := filepath.Join(dir, "tc.yaml")
		if err := os.WriteFile(tcPath, []byte(yamlContent), 0o644); err != nil {
			t.Fatalf("write test case: %v", err)
		}

		tc, err := LoadTestCase(tcPath)
		if err != nil {
			t.Fatalf("LoadTestCase: %v", err)
		}

		// Empty prompt is allowed — the runner or engine can handle validation
		if tc.Stimulus.Message != "" {
			t.Errorf("Message = %q, want empty", tc.Stimulus.Message)
		}
	})

	t.Run("prompt_file with multiline content", func(t *testing.T) {
		dir := t.TempDir()

		promptContent := "# Task\n\nPlease do the following:\n1. Read the code\n2. Explain it\n3. Suggest improvements\n"
		if err := os.WriteFile(filepath.Join(dir, "long-prompt.md"), []byte(promptContent), 0o644); err != nil {
			t.Fatalf("write prompt file: %v", err)
		}

		yamlContent := `id: tc-multiline
name: Multiline Prompt
inputs:
  prompt_file: long-prompt.md
`
		tcPath := filepath.Join(dir, "tc.yaml")
		if err := os.WriteFile(tcPath, []byte(yamlContent), 0o644); err != nil {
			t.Fatalf("write test case: %v", err)
		}

		tc, err := LoadTestCase(tcPath)
		if err != nil {
			t.Fatalf("LoadTestCase: %v", err)
		}

		if tc.Stimulus.Message != promptContent {
			t.Errorf("Message = %q, want %q", tc.Stimulus.Message, promptContent)
		}
	})

	t.Run("rejects absolute path", func(t *testing.T) {
		dir := t.TempDir()

		yamlContent := `id: tc-abs
name: Absolute Path
inputs:
  prompt_file: /etc/passwd
`
		tcPath := filepath.Join(dir, "tc.yaml")
		if err := os.WriteFile(tcPath, []byte(yamlContent), 0o644); err != nil {
			t.Fatalf("write test case: %v", err)
		}

		_, err := LoadTestCase(tcPath)
		if err == nil {
			t.Fatal("expected error for absolute path")
		}
		if !strings.Contains(err.Error(), "relative path") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("rejects path traversal", func(t *testing.T) {
		dir := t.TempDir()

		yamlContent := `id: tc-traversal
name: Path Traversal
inputs:
  prompt_file: ../../../etc/passwd
`
		tcPath := filepath.Join(dir, "tc.yaml")
		if err := os.WriteFile(tcPath, []byte(yamlContent), 0o644); err != nil {
			t.Fatalf("write test case: %v", err)
		}

		_, err := LoadTestCase(tcPath)
		if err == nil {
			t.Fatal("expected error for path traversal")
		}
		if !strings.Contains(err.Error(), "path traversal") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("clears MessageFile after resolve", func(t *testing.T) {
		dir := t.TempDir()

		if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("content"), 0o644); err != nil {
			t.Fatalf("write prompt file: %v", err)
		}

		yamlContent := `id: tc-clear
name: Clear MessageFile
inputs:
  prompt_file: prompt.md
`
		tcPath := filepath.Join(dir, "tc.yaml")
		if err := os.WriteFile(tcPath, []byte(yamlContent), 0o644); err != nil {
			t.Fatalf("write test case: %v", err)
		}

		tc, err := LoadTestCase(tcPath)
		if err != nil {
			t.Fatalf("LoadTestCase: %v", err)
		}

		if tc.Stimulus.MessageFile != "" {
			t.Errorf("MessageFile should be cleared after resolve, got %q", tc.Stimulus.MessageFile)
		}
	})
}
