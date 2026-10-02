package models

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var commandMockNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// CommandMockConfig defines deterministic responses for one executable.
type CommandMockConfig struct {
	Name        string                `yaml:"name" json:"name"`
	Responses   []CommandMockResponse `yaml:"responses" json:"responses"`
	ExpectCalls *int                  `yaml:"expect_calls,omitempty" json:"expect_calls,omitempty"`
}

// CommandMockResponse matches one invocation and supplies its process output.
type CommandMockResponse struct {
	Args        []string          `yaml:"args,omitempty" json:"args,omitempty"`
	ArgsRegex   []string          `yaml:"args_regex,omitempty" json:"args_regex,omitempty"`
	Environment map[string]string `yaml:"environment,omitempty" json:"environment,omitempty"`
	WorkDir     string            `yaml:"workdir,omitempty" json:"workdir,omitempty"`
	Stdout      any               `yaml:"stdout,omitempty" json:"stdout,omitempty"`
	Stderr      string            `yaml:"stderr,omitempty" json:"stderr,omitempty"`
	ExitCode    int               `yaml:"exit_code,omitempty" json:"exit_code,omitempty"`
	Fixture     string            `yaml:"fixture,omitempty" json:"fixture,omitempty"`
}

// CommandInvocation records a sanitized invocation of a mocked executable.
type CommandInvocation struct {
	Command       string   `json:"command"`
	Args          []string `json:"args,omitempty"`
	ExitCode      int      `json:"exit_code"`
	ResponseIndex int      `json:"response_index"`
}

// ValidateCommandMocks validates names, matchers, responses, and call expectations.
func ValidateCommandMocks(mocks []CommandMockConfig) error {
	seen := make(map[string]bool, len(mocks))
	for i, mock := range mocks {
		name := strings.TrimSpace(mock.Name)
		if name == "" || !commandMockNamePattern.MatchString(name) {
			return fmt.Errorf("command_mocks[%d].name must be an executable name containing only letters, numbers, '.', '_' or '-'", i)
		}
		key := strings.ToLower(name)
		if seen[key] {
			return fmt.Errorf("command_mocks[%d].name %q is duplicated", i, name)
		}
		seen[key] = true
		if len(mock.Responses) == 0 {
			return fmt.Errorf("command_mocks[%d] %q must define at least one response", i, name)
		}
		if mock.ExpectCalls != nil && *mock.ExpectCalls < 0 {
			return fmt.Errorf("command_mocks[%d] %q expect_calls must not be negative", i, name)
		}
		for j, response := range mock.Responses {
			prefix := fmt.Sprintf("command_mocks[%d] %q responses[%d]", i, name, j)
			if (response.Args == nil) == (response.ArgsRegex == nil) {
				return fmt.Errorf("%s must specify exactly one of args or args_regex", prefix)
			}
			for k, pattern := range response.ArgsRegex {
				if _, err := regexp.Compile("^(?:" + pattern + ")$"); err != nil {
					return fmt.Errorf("%s args_regex[%d] %q is invalid: %w", prefix, k, pattern, err)
				}
			}
			if response.ExitCode < 0 || response.ExitCode > 255 {
				return fmt.Errorf("%s exit_code must be between 0 and 255, got %d", prefix, response.ExitCode)
			}
			if response.Fixture != "" && response.Stdout != nil {
				return fmt.Errorf("%s cannot specify both fixture and stdout", prefix)
			}
			if response.Fixture != "" && (filepath.IsAbs(response.Fixture) || containsTraversalSegmentInPath(response.Fixture)) {
				return fmt.Errorf("%s fixture must be a relative path without '..' segments", prefix)
			}
			if response.WorkDir != "" && (filepath.IsAbs(response.WorkDir) || strings.HasPrefix(response.WorkDir, `\`) || containsTraversalSegmentInPath(response.WorkDir)) {
				return fmt.Errorf("%s workdir must be a relative path without '..' segments", prefix)
			}
			for key := range response.Environment {
				if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "=\x00") {
					return fmt.Errorf("%s environment contains an invalid variable name", prefix)
				}
			}
		}
	}
	return nil
}
