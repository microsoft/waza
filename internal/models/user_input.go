package models

import (
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"
)

// ValidateUserInputResponsesDocument validates field shape without decoding
// graders, so blocked offline schema resolution cannot hide a null declaration.
func ValidateUserInputResponsesDocument(data []byte) error {
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return err
	}
	return validateUserInputResponsesNode(&node)
}

func validateUserInputResponsesNode(node *yaml.Node) error {
	if node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
		node = node.Content[0]
	}
	value, err := effectiveStringNode(node, "user_input_responses")
	if err != nil {
		return err
	}
	if value != nil && value.Kind != yaml.SequenceNode {
		return fmt.Errorf("user_input_responses must be an array")
	}
	return nil
}

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
	// invalidAnswer records an authored answer that is not a string.
	invalidAnswer bool
}

// UnmarshalYAML keeps an explicit empty or null question distinct from an
// omitted one, so it cannot silently become a wildcard, and rejects non-string
// answers. Effective values are resolved through YAML merges.
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
	question, err := effectiveStringNode(node, "question")
	if err != nil {
		return err
	}
	if question != nil && (question.Kind != yaml.ScalarNode || question.Tag != "!!str" || question.Value == "") {
		c.invalidQuestion = true
	}
	answer, err := effectiveStringNode(node, "answer")
	if err != nil {
		return err
	}
	if answer != nil && answer.Tag != "!!null" && (answer.Kind != yaml.ScalarNode || answer.Tag != "!!str") {
		c.invalidAnswer = true
	}
	return nil
}

// effectiveStringNode returns the effective value of key, following merges
// and aliases, or nil when the key is absent.
func effectiveStringNode(node *yaml.Node, key string) (*yaml.Node, error) {
	value, err := faultDocumentValue(node, key)
	if err != nil || value == nil {
		return nil, err
	}
	return faultAliasTarget(value)
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

// UserInputRecordsOf returns the recorded requests, or nil when none were
// recorded because the native callback was not registered.
func UserInputRecordsOf(records *[]UserInputRecord) []UserInputRecord {
	if records == nil {
		return nil
	}
	return *records
}

// ValidateUserInputResponses checks scripted native user-input answers.
func ValidateUserInputResponses(responses []UserInputResponseConfig) error {
	for i, response := range responses {
		if response.invalidAnswer {
			return fmt.Errorf("user_input_responses[%d].answer must be a string", i)
		}
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
