package models

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	// CurrentSchemaVersion is the current MAJOR.MINOR schema version for public artifacts.
	//
	// 1.0 — initial public schema (PR #382 / issue #368).
	// 1.1 — additive: per-turn checkpoints (TestCase.Checkpoints / RunResult.Checkpoints, #358)
	//       and RunResult.tool_events[] for per-task tool metrics (#366). Purely additive
	//       over 1.0, so 1.0 artifacts continue to load without migration.
	CurrentSchemaVersion = "1.1"
)

func defaultSchemaVersion(version string) string {
	if strings.TrimSpace(version) == "" {
		return CurrentSchemaVersion
	}
	return version
}

func ValidateSchemaVersion(artifact, source, version string) (string, error) {
	version = defaultSchemaVersion(version)

	major, _, err := parseSchemaVersion(version)
	if err != nil {
		return "", fmt.Errorf("%s %s has invalid schemaVersion %q: %w", artifact, source, version, err)
	}
	currentMajor, _, err := parseSchemaVersion(CurrentSchemaVersion)
	if err != nil {
		return "", err
	}
	if major != currentMajor {
		return "", fmt.Errorf("%s %s uses schemaVersion %q (major %d), but this waza supports schema major %d (current schemaVersion %s); run \"waza migrate <file>\" to migrate the artifact file", artifact, source, version, major, currentMajor, CurrentSchemaVersion)
	}
	return version, nil
}

// ProbeEvaluationOutcomeSchemaVersion cheaply detects whether data has the
// top-level shape of a results.json artifact and returns its declared version.
func ProbeEvaluationOutcomeSchemaVersion(data []byte) (version string, ok bool, err error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return "", false, err
	}
	if !hasEvaluationOutcomeShape(fields) {
		return "", false, nil
	}
	if raw, exists := fields["schemaVersion"]; exists && string(raw) != "null" {
		if err := json.Unmarshal(raw, &version); err != nil {
			return "", false, fmt.Errorf("schemaVersion must be a string: %w", err)
		}
	}
	return version, true, nil
}

func hasEvaluationOutcomeShape(fields map[string]json.RawMessage) bool {
	for _, key := range []string{"eval_id", "runId", "eval_name", "summary", "tasks"} {
		if _, ok := fields[key]; ok {
			return true
		}
	}
	return false
}

func parseSchemaVersion(version string) (major int, minor int, err error) {
	parts := strings.Split(version, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return 0, 0, fmt.Errorf("expected MAJOR.MINOR")
	}
	major, err = strconv.Atoi(parts[0])
	if err != nil || major < 0 {
		return 0, 0, fmt.Errorf("major version must be a non-negative integer")
	}
	minor, err = strconv.Atoi(parts[1])
	if err != nil || minor < 0 {
		return 0, 0, fmt.Errorf("minor version must be a non-negative integer")
	}
	return major, minor, nil
}

func warnUnknownYAMLFields(data []byte, artifact, source string, target any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		if messages, ok := unknownYAMLFieldMessages(err); ok {
			for _, msg := range messages {
				slog.Warn("unknown schema field ignored for same-major compatibility", "artifact", artifact, "source", source, "detail", msg)
			}
			return nil
		}
		return err
	}
	return nil
}

func unknownYAMLFieldMessages(err error) ([]string, bool) {
	var typeErr *yaml.TypeError
	if !errors.As(err, &typeErr) {
		return nil, false
	}
	if len(typeErr.Errors) == 0 {
		return nil, false
	}
	messages := make([]string, 0, len(typeErr.Errors))
	for _, msg := range typeErr.Errors {
		if !strings.Contains(msg, "field ") || !strings.Contains(msg, " not found in type ") {
			return nil, false
		}
		messages = append(messages, msg)
	}
	return messages, true
}

func warnUnknownJSONFields(data []byte, artifact, source string, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if strings.Contains(err.Error(), "json: unknown field ") {
			slog.Warn("unknown schema field ignored for same-major compatibility", "artifact", artifact, "source", source, "detail", err.Error())
			return nil
		}
		return err
	}
	return nil
}

// LoadEvaluationOutcome loads a results.json file with schema-version compatibility checks.
func LoadEvaluationOutcome(path string) (*EvaluationOutcome, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseEvaluationOutcome(data, path)
}

// ParseEvaluationOutcome decodes an EvaluationOutcome and defaults missing schemaVersion to the current version.
func ParseEvaluationOutcome(data []byte, source string) (*EvaluationOutcome, error) {
	var header struct {
		SchemaVersion string `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, err
	}
	version, err := ValidateSchemaVersion("results.json", source, header.SchemaVersion)
	if err != nil {
		return nil, err
	}

	var outcome EvaluationOutcome
	if err := json.Unmarshal(data, &outcome); err != nil {
		return nil, err
	}
	outcome.SchemaVersion = version

	var strict EvaluationOutcome
	if err := warnUnknownJSONFields(data, "results.json", source, &strict); err != nil {
		return nil, err
	}

	return &outcome, nil
}
