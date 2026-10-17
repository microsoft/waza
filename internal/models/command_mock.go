package models

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var commandMockNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
var commandMockWindowsPathPattern = regexp.MustCompile(`^[A-Za-z]:`)

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
	Sequence    []CommandMockStep `yaml:"sequence,omitempty" json:"sequence,omitempty"`
}

type CommandMockStep struct {
	Stdout   any    `yaml:"stdout,omitempty" json:"stdout,omitempty"`
	Stderr   string `yaml:"stderr,omitempty" json:"stderr,omitempty"`
	ExitCode int    `yaml:"exit_code" json:"exit_code"`
	Fixture  string `yaml:"fixture,omitempty" json:"fixture,omitempty"`
	DelayMS  int64  `yaml:"delay_ms,omitempty" json:"delay_ms,omitempty"`
}

// CommandInvocation records a sanitized invocation of a mocked executable.
type CommandInvocation struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	// A started attempt interrupted before completion has no observed exit code.
	ExitCode      *int `json:"exit_code,omitempty"`
	ResponseIndex int  `json:"response_index"`
}

// ValidateCommandMocks validates names, matchers, responses, and call expectations.
func ValidateCommandMocks(mocks []CommandMockConfig) error {
	seen := make(map[string]bool, len(mocks))
	for i, mock := range mocks {
		name := strings.TrimSpace(mock.Name)
		if name == "" || name != mock.Name || !commandMockNamePattern.MatchString(name) {
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
			if response.WorkDir != "" && (isCommandMockAbsolutePath(response.WorkDir) || containsTraversalSegmentInPath(response.WorkDir)) {
				return fmt.Errorf("%s workdir must be a relative path without '..' segments", prefix)
			}
			for key := range response.Environment {
				if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "=\x00") {
					return fmt.Errorf("%s environment contains an invalid variable name", prefix)
				}
			}
			if len(response.Sequence) > 0 {
				if response.Stdout != nil || response.Stderr != "" || response.ExitCode != 0 || response.Fixture != "" {
					return fmt.Errorf("%s cannot mix a sequence with response output fields", prefix)
				}
				if len(response.Sequence) > MaxMockFaultSequenceSteps {
					return fmt.Errorf("%s sequence must contain at most %d steps", prefix, MaxMockFaultSequenceSteps)
				}
				for k, step := range response.Sequence {
					if err := validateCommandMockOutput(step.Stdout, step.Stderr, step.ExitCode, step.Fixture, step.DelayMS); err != nil {
						return fmt.Errorf("%s sequence[%d]: %w", prefix, k, err)
					}
				}
			} else if err := validateCommandMockOutput(response.Stdout, response.Stderr, response.ExitCode, response.Fixture, 0); err != nil {
				return fmt.Errorf("%s: %w", prefix, err)
			}
		}
	}
	return nil
}

func validateCommandMockOutput(stdout any, stderr string, exitCode int, fixture string, delayMS int64) error {
	if exitCode < 0 || exitCode > 255 {
		return fmt.Errorf("exit_code must be between 0 and 255, got %d", exitCode)
	}
	if fixture != "" && stdout != nil {
		return fmt.Errorf("cannot specify both fixture and stdout")
	}
	if fixture != "" && (isCommandMockAbsolutePath(fixture) || containsTraversalSegmentInPath(fixture)) {
		return fmt.Errorf("fixture must be a relative path without '..' segments")
	}
	if delayMS < 0 || delayMS > MaxMockFaultDelayMS {
		return fmt.Errorf("delay_ms must be between 0 and %d", MaxMockFaultDelayMS)
	}
	return nil
}

// ValidateCommandMocksSchemaVersion checks that the enclosing eval schema
// supports command mocks.
func ValidateCommandMocksSchemaVersion(version string) error {
	major, minor, err := parseSchemaVersion(version)
	if err != nil {
		return err
	}
	if major < 1 || major == 1 && minor < 3 {
		return fmt.Errorf("command_mocks requires schemaVersion 1.3 or newer")
	}
	return nil
}

func isCommandMockAbsolutePath(path string) bool {
	return filepath.IsAbs(path) || strings.HasPrefix(path, `\`) || commandMockWindowsPathPattern.MatchString(path)
}
