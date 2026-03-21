package models

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadTestCase_GitResource(t *testing.T) {
	tests := []struct {
		name       string
		yaml       string
		wantErr    bool
		wantGitRes bool
		wantStrat  GitType
	}{
		{
			name:       "git resource with worktree strategy",
			yaml:       mustReadTestFile(t, "git-resources-task-example.yaml"),
			wantGitRes: true,
			wantStrat:  GitTypeWorktree,
		},
		{
			name:       "no git resource - path only",
			yaml:       mustReadTestFile(t, "file-resources-task-example.yaml"),
			wantGitRes: false,
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
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadTestCase: %v", err)
			}

			hasGit := false
			for _, repo := range tc.Stimulus.Repos {
				if repo.Type != "" {
					hasGit = true
					if repo.Type != tt.wantStrat {
						t.Errorf("Type = %q, want %q", repo.Type, tt.wantStrat)
					}
				}
			}
			if hasGit != tt.wantGitRes {
				t.Errorf("hasGit = %v, want %v", hasGit, tt.wantGitRes)
			}
		})
	}
}

func TestResourceRef_Validate(t *testing.T) {
	tests := []struct {
		name    string
		ref     ResourceRef
		wantErr bool
	}{
		{
			name: "valid path",
			ref:  ResourceRef{Location: "file.txt"},
		},
		{
			name: "valid content",
			ref:  ResourceRef{Body: "inline"},
		},
		{
			name:    "empty resource",
			ref:     ResourceRef{},
			wantErr: true,
		},
		{
			name: "path and content",
			ref:  ResourceRef{Location: "f.txt", Body: "inline"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.ref.Validate()
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoadTestCase_ShouldTriggerField(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantNil bool
		wantVal bool
	}{
		{
			name:    "should_trigger true",
			yaml:    mustReadTestFile(t, "trigger-true-task-example.yaml"),
			wantNil: false,
			wantVal: true,
		},
		{
			name:    "should_trigger false",
			yaml:    mustReadTestFile(t, "trigger-false-task-example.yaml"),
			wantNil: false,
			wantVal: false,
		},
		{
			name:    "should_trigger omitted",
			yaml:    mustReadTestFile(t, "trigger-omit-task-example.yaml"),
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

// path is a path within 'testdata'
func mustReadTestFile(t *testing.T, path string) string {
	buff, err := os.ReadFile(filepath.Join("testdata", path))
	require.NoError(t, err)

	return string(buff)
}
