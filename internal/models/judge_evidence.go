package models

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

const (
	JudgeEvidenceVersion      = "1.0"
	JudgeEvidenceMaxFiles     = 64
	JudgeEvidenceMaxFileBytes = 64 << 10
	JudgeEvidenceMaxBytes     = 256 << 10
)

type PromptEvidenceContext struct {
	Files         []string `yaml:"files,omitempty" json:"files,omitempty"`
	EvaluatorOnly []string `yaml:"evaluator_only,omitempty" json:"evaluator_only,omitempty"`
}

func ValidateJudgeEvidencePath(name string) error {
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 1024 ||
		strings.ContainsAny(name, "\\:\x00*?[]") || path.IsAbs(name) {
		return errors.New("evidence_context paths must be exact slash-relative paths of at most 1024 characters")
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("evidence_context paths must not contain empty or traversal segments")
		}
	}
	return nil
}

func (p PromptGraderParameters) ValidateEvidenceContext() error {
	if p.EvidenceContext == nil {
		return nil
	}
	if p.ContinueSession || p.Mode != "" && p.Mode != PromptGraderModeIndependent {
		return errors.New("evidence_context requires independent mode and continue_session: false")
	}
	for _, paths := range [][]string{p.EvidenceContext.Files, p.EvidenceContext.EvaluatorOnly} {
		if len(paths) > JudgeEvidenceMaxFiles {
			return errors.New("evidence_context supports at most 64 files or exclusions")
		}
		seen := map[string]bool{}
		for _, name := range paths {
			if err := ValidateJudgeEvidencePath(name); err != nil {
				return err
			}
			if seen[name] {
				return errors.New("evidence_context paths must be unique")
			}
			seen[name] = true
		}
	}
	return nil
}

func validatePromptEvidenceNode(node *yaml.Node) error {
	if node == nil {
		return nil
	}
	if hasPromptEvidenceNode(node, map[*yaml.Node]bool{}) {
		if err := validateEvidenceYAMLStructure(node, 0); err != nil {
			return err
		}
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value != "evidence_context" && strings.EqualFold(node.Content[i].Value, "evidence_context") {
			return errors.New("evidence_context must use its exact spelling")
		}
		if node.Content[i].Value != "evidence_context" {
			continue
		}
		value := node.Content[i+1]
		if value.Kind != yaml.MappingNode || value.Tag != "!!map" {
			return errors.New("evidence_context must be an object, not null or a scalar")
		}
		for j := 0; j+1 < len(value.Content); j += 2 {
			key, list := value.Content[j], value.Content[j+1]
			if key.Tag != "!!str" || key.Value != "files" && key.Value != "evaluator_only" {
				return errors.New("evidence_context only supports files and evaluator_only")
			}
			if list.Kind != yaml.SequenceNode || list.Tag != "!!seq" {
				return fmt.Errorf("evidence_context.%s must be an array of strings", key.Value)
			}
			for _, item := range list.Content {
				if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
					return errors.New("evidence_context paths must be strings, not aliases or coerced scalars")
				}
			}
		}
	}
	return nil
}

func hasPromptEvidenceNode(node *yaml.Node, seen map[*yaml.Node]bool) bool {
	if node == nil || seen[node] {
		return false
	}
	seen[node] = true
	if node.Kind == yaml.AliasNode {
		return hasPromptEvidenceNode(node.Alias, seen)
	}
	switch node.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			if strings.EqualFold(node.Content[i].Value, "evidence_context") {
				return true
			}
			if node.Content[i].Tag == "!!merge" && hasPromptEvidenceNode(node.Content[i+1], seen) {
				return true
			}
		}
	case yaml.SequenceNode, yaml.DocumentNode:
		for _, child := range node.Content {
			if hasPromptEvidenceNode(child, seen) {
				return true
			}
		}
	}
	return false
}

func validateEvidenceYAMLStructure(node *yaml.Node, depth int) error {
	if node == nil || depth > 64 {
		return errors.New("evidence_context exceeds the supported YAML nesting depth")
	}
	if node.Kind == yaml.AliasNode || node.Tag == "!!merge" {
		return errors.New("evidence_context does not support YAML aliases or merges")
	}
	for _, child := range node.Content {
		if err := validateEvidenceYAMLStructure(child, depth+1); err != nil {
			return err
		}
	}
	return nil
}
func (p *PromptGraderParameters) UnmarshalJSON(data []byte) error {
	type plain PromptGraderParameters
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for key := range fields {
		if key != "evidence_context" && strings.EqualFold(key, "evidence_context") {
			return errors.New("evidence_context must use its exact spelling")
		}
	}
	if raw, present := fields["evidence_context"]; present {
		if _, err := jsonutil.Parse(data); err != nil {
			return err
		}
		var config PromptEvidenceContext
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return errors.New("evidence_context must be an object")
		}
		if err := decoder.Decode(&config); err != nil {
			return err
		}
		if err := ValidateNativeJSONKeys(raw, PromptEvidenceContext{}); err != nil {
			return err
		}
		var members map[string]json.RawMessage
		if err := json.Unmarshal(raw, &members); err != nil {
			return err
		}
		for _, name := range []string{"files", "evaluator_only"} {
			if value, ok := members[name]; ok && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return fmt.Errorf("evidence_context.%s must be an array", name)
			}
		}
	}
	var result plain
	if err := json.Unmarshal(data, &result); err != nil {
		return err
	}
	*p = PromptGraderParameters(result)
	return p.ValidateEvidenceContext()
}

type JudgeEvidenceMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type JudgeEvidenceFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	SHA256  string `json:"sha256"`
}

type JudgeEvidenceSection[T any] struct {
	Availability string `json:"availability"`
	Completeness string `json:"completeness"`
	Items        *T     `json:"items"`
	Reason       string `json:"reason,omitempty"`
}

// JudgeEvidence is independent of snapshot manifests. Its digest is integrity
// metadata, not authentication of the producer or truth of the contents.
type JudgeEvidence struct {
	Version       string                                       `json:"version"`
	SHA256        string                                       `json:"sha256"`
	Origin        EvidenceOrigin                               `json:"origin"`
	Boundary      string                                       `json:"boundary"`
	ExecutionMode string                                       `json:"execution_mode"`
	Task          JudgeEvidenceSection[string]                 `json:"task"`
	Output        JudgeEvidenceSection[string]                 `json:"output"`
	Messages      JudgeEvidenceSection[[]JudgeEvidenceMessage] `json:"messages"`
	ToolEvents    JudgeEvidenceSection[[]ToolEvent]            `json:"tool_events"`
	Files         JudgeEvidenceSection[[]JudgeEvidenceFile]    `json:"files"`
	Selection     PromptEvidenceContext                        `json:"selection"`
	Redaction     EvidenceRedaction                            `json:"redaction"`
	Limits        JudgeEvidenceLimits                          `json:"limits"`
	Diagnostics   []EvidenceDiagnostic                         `json:"diagnostics,omitempty"`
}

var judgeEvidenceSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	return compileEvidenceManifestSchema(schemas.JudgeEvidenceSchemaJSON)
})

func (e *JudgeEvidence) UnmarshalJSON(data []byte) error {
	if len(data) > JudgeEvidenceMaxBytes {
		return errors.New("judge evidence exceeds the 256 KiB encoded limit")
	}
	instance, err := jsonutil.Parse(data)
	if err != nil {
		return errors.New("judge evidence has invalid or ambiguous JSON")
	}
	if err := ValidateNativeJSONKeys(data, JudgeEvidence{}); err != nil {
		return errors.New("judge evidence keys must use their exact spelling")
	}
	schema, err := judgeEvidenceSchema()
	if err != nil {
		return err
	}
	if err := schema.Validate(instance); err != nil {
		return errors.New("judge evidence does not match its versioned schema")
	}
	type plain JudgeEvidence
	var result plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return errors.New("judge evidence has invalid or unknown fields")
	}
	*e = JudgeEvidence(result)
	return nil
}

type JudgeFileCapture struct {
	Files       []JudgeEvidenceFile
	Diagnostics []EvidenceDiagnostic
	Redaction   EvidenceRedaction
}

type JudgeEvidenceLimits struct {
	MaxFiles       int `json:"max_files"`
	MaxFileBytes   int `json:"max_file_bytes"`
	MaxBundleBytes int `json:"max_bundle_bytes"`
}

type PromptJudgeObservation struct {
	Grader             string                `json:"grader"`
	BundleSHA256       string                `json:"bundle_sha256,omitempty"`
	DeliverySHA256     string                `json:"delivery_sha256,omitempty"`
	Context            string                `json:"context"`
	State              string                `json:"state"`
	SubmissionObserved bool                  `json:"submission_observed"`
	Inspection         string                `json:"inspection"`
	EvidenceContext    PromptEvidenceContext `json:"evidence_context"`
	Purpose            string                `json:"purpose"`
	SessionID          string                `json:"session_id,omitempty"`
	Model              string                `json:"model,omitempty"`
	Usage              *UsageStats           `json:"usage,omitempty"`
	DurationMs         int64                 `json:"duration_ms"`
	Diagnostics        []EvidenceDiagnostic  `json:"diagnostics,omitempty"`
}

func UsesJudgeEvidence(graders []GraderConfig, task *TestCase) bool {
	for _, grader := range graders {
		if params, ok := grader.Parameters.(PromptGraderParameters); ok && params.EvidenceContext != nil {
			return true
		}
	}
	if task != nil {
		for _, grader := range task.Validators {
			if params, ok := grader.Parameters.(PromptGraderParameters); ok && params.EvidenceContext != nil {
				return true
			}
		}
	}
	return false
}
