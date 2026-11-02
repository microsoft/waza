package models

import (
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"
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

	// invalidQuestion records an authored question that is null, empty, or
	// not a string. Omission is the only way to answer any question.
	invalidQuestion bool
}

// UnmarshalYAML keeps an explicit empty or null question distinct from an
// omitted one, so it cannot silently become a wildcard.
func (c *UserInputResponseConfig) UnmarshalYAML(node *yaml.Node) error {
	type plain UserInputResponseConfig
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*c = UserInputResponseConfig(decoded)
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value != "question" {
			continue
		}
		value := node.Content[i+1]
		for value.Kind == yaml.AliasNode && value.Alias != nil {
			value = value.Alias
		}
		if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || value.Value == "" {
			c.invalidQuestion = true
		}
	}
	return nil
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
		if response.invalidQuestion {
			return fmt.Errorf("user_input_responses[%d].question must be a non-empty string; omit it to answer the next question", i)
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
// The pattern is checked on its own first so that unbalanced groups cannot
// escape the anchors added around it.
func CompileUserInputQuestion(pattern string) (*regexp.Regexp, error) {
	if _, err := regexp.Compile(pattern); err != nil {
		return nil, err
	}
	return regexp.Compile(`^(?:` + pattern + `)$`)
}

// ValidateNativeUserInput checks where config.native_user_input may be used.
// It is limited to scenario evals, whose readers reject unknown config
// fields, so executables without this feature refuse the eval.
func (c Config) ValidateNativeUserInput(schemaVersion string) error {
	if !c.NativeUserInput {
		return nil
	}
	if schemaVersion != ScenarioSchemaVersion {
		return fmt.Errorf("native_user_input requires a scenario eval with schemaVersion %s", ScenarioSchemaVersion)
	}
	if c.EngineType != "copilot-sdk" {
		return fmt.Errorf("native_user_input requires executor copilot-sdk")
	}
	return nil
}

// ValidateTaskUserInput checks a task's scripted answers against the
// enclosing eval configuration.
func ValidateTaskUserInput(cfg Config, tc *TestCase) error {
	if tc == nil || tc.UserInputResponses == nil {
		return nil
	}
	if !cfg.NativeUserInput {
		return fmt.Errorf("test case %q: user_input_responses requires config.native_user_input: true", tc.TestID)
	}
	if err := ValidateUserInputResponses(*tc.UserInputResponses); err != nil {
		return fmt.Errorf("test case %q: %w", tc.TestID, err)
	}
	return nil
}
