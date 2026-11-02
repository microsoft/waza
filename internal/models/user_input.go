package models

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// UserInputResponseConfig scripts one answer to a native Copilot SDK
// user-input (ask_user) request. Entries are consumed strictly in order.
type UserInputResponseConfig struct {
	// Exactly one of Question (exact match) or QuestionRegex (anchored full
	// match) identifies the expected question.
	Question      string `yaml:"question,omitempty" json:"question,omitempty"`
	QuestionRegex string `yaml:"question_regex,omitempty" json:"question_regex,omitempty"`
	// Choices, when present, must exactly equal the offered choices in order.
	// An explicit empty list expects that no choices are offered.
	Choices *[]string `yaml:"choices,omitempty" json:"choices,omitempty"`
	// Exactly one of Choice (a selected offered choice) or Freeform (a typed
	// answer) is returned to the agent.
	Choice   *string `yaml:"choice,omitempty" json:"choice,omitempty"`
	Freeform *string `yaml:"freeform,omitempty" json:"freeform,omitempty"`
}

const (
	UserInputOutcomeAnswered = "answered"
	UserInputOutcomeRejected = "rejected"

	// UserInputSourceScript marks a request handled by a scripted entry.
	UserInputSourceScript = "script"
	// UserInputSourceResponder marks a request answered by the adaptive responder model.
	UserInputSourceResponder = "responder"
)

// UserInputRecord is evidence for one native user-input request observed
// through the SDK callback, in arrival order across all turns of a run.
type UserInputRecord struct {
	Sequence  int    `json:"sequence"`
	SessionID string `json:"session_id"`
	Question  string `json:"question"`
	// Choices preserves the offered list: null when none were sent, [] when
	// an explicit empty list was sent.
	Choices       []string `json:"choices"`
	AllowFreeform *bool    `json:"allow_freeform,omitempty"`
	Outcome       string   `json:"outcome"`
	// Source is "script" or "responder"; omitted when nothing could answer.
	Source string `json:"source,omitempty"`
	// ResponseIndex identifies the scripted entry that was matched against
	// this request; omitted when no scripted entry remained.
	ResponseIndex *int    `json:"response_index,omitempty"`
	Answer        *string `json:"answer,omitempty"`
	WasFreeform   *bool   `json:"was_freeform,omitempty"`
	Error         string  `json:"error,omitempty"`
}

// ValidateUserInputResponses checks scripted native user-input answers.
func ValidateUserInputResponses(responses []UserInputResponseConfig) error {
	for i, response := range responses {
		prefix := fmt.Sprintf("user_input_responses[%d]", i)
		hasQuestion := strings.TrimSpace(response.Question) != ""
		hasRegex := response.QuestionRegex != ""
		if response.Question != "" && !hasQuestion {
			return fmt.Errorf("%s.question must not be blank", prefix)
		}
		if hasQuestion == hasRegex {
			return fmt.Errorf("%s must specify exactly one of question or question_regex", prefix)
		}
		if hasRegex {
			if _, err := CompileUserInputQuestionRegex(response.QuestionRegex); err != nil {
				return fmt.Errorf("%s.question_regex is invalid: %w", prefix, err)
			}
		}
		if (response.Choice == nil) == (response.Freeform == nil) {
			return fmt.Errorf("%s must specify exactly one of choice or freeform", prefix)
		}
		if response.Choice != nil {
			if *response.Choice == "" {
				return fmt.Errorf("%s.choice must not be empty", prefix)
			}
			if response.Choices != nil && !slices.Contains(*response.Choices, *response.Choice) {
				return fmt.Errorf("%s.choice %q is not one of the expected choices", prefix, *response.Choice)
			}
		}
	}
	return nil
}

// CompileUserInputQuestionRegex compiles question_regex as a full-string match.
func CompileUserInputQuestionRegex(pattern string) (*regexp.Regexp, error) {
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
