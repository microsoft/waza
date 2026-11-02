package models

import (
	"fmt"
	"regexp"
)

// UserInputResponseConfig scripts one answer to a native Copilot SDK
// user-input (ask_user) request. Entries are consumed strictly in order.
type UserInputResponseConfig struct {
	// Question, when set, is an RE2 pattern that must match the whole
	// question. When omitted the entry answers the next question.
	Question string `yaml:"question,omitempty" json:"question,omitempty"`
	// Answer selects the offered choice with the same text; otherwise it is
	// sent as a typed (freeform) answer.
	Answer *string `yaml:"answer" json:"answer"`
}

const (
	// UserInputSourceScript marks a request answered by a scripted entry.
	UserInputSourceScript = "script"
	// UserInputSourceResponder marks a request answered by the adaptive responder model.
	UserInputSourceResponder = "responder"
)

// UserInputRecord is evidence for one native user-input request observed
// through the SDK callback, in arrival order across all turns of a run.
// A rejected request has Error set and no Answer.
type UserInputRecord struct {
	Sequence  int    `json:"sequence"`
	SessionID string `json:"session_id"`
	Question  string `json:"question"`
	// Choices preserves the offered list: null when none were sent, [] when
	// an explicit empty list was sent.
	Choices     []string `json:"choices"`
	Answer      *string  `json:"answer,omitempty"`
	WasFreeform *bool    `json:"was_freeform,omitempty"`
	// Source is "script" or "responder"; omitted when nothing could answer.
	Source string `json:"source,omitempty"`
	Error  string `json:"error,omitempty"`
}

// ValidateUserInputResponses checks scripted native user-input answers.
func ValidateUserInputResponses(responses []UserInputResponseConfig) error {
	for i, response := range responses {
		if response.Answer == nil {
			return fmt.Errorf("user_input_responses[%d].answer is required", i)
		}
		if response.Question != "" {
			if _, err := CompileUserInputQuestion(response.Question); err != nil {
				return fmt.Errorf("user_input_responses[%d].question is not a valid regular expression: %w", i, err)
			}
		}
	}
	return nil
}

// CompileUserInputQuestion compiles a question pattern as a full-string match.
func CompileUserInputQuestion(pattern string) (*regexp.Regexp, error) {
	return regexp.Compile(`^(?:` + pattern + `)$`)
}

// ValidateUserInputResponsesSchemaVersion requires the scenario major version
// so executables that predate native user-input scripting reject the eval
// instead of silently ignoring the task field.
func ValidateUserInputResponsesSchemaVersion(version string) error {
	if version != ScenarioSchemaVersion {
		return fmt.Errorf("user_input_responses requires explicit enclosing scenario schemaVersion %s", ScenarioSchemaVersion)
	}
	return nil
}
