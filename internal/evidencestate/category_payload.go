package evidencestate

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

var categorySchemas = sync.OnceValues(func() (map[Category]*jsonschema.Schema, error) {
	const count = `{"type":"integer","minimum":0,"maximum":9223372036854775807}`
	const number = `{"type":"number","minimum":0}`
	texts := map[Category]string{
		Conversation: `{"type":"array","items":{"type":"object","additionalProperties":false,
			"required":["type"],"properties":{"type":{"enum":["user.message","assistant.message"]},
			"content":{"type":["string","null"]}}}}`,
		Tool: `{"type":"array","items":{"type":"object","additionalProperties":false,
			"required":["turn","sequence","tool_name"],"properties":{
			"turn":` + count + `,"sequence":` + count + `,"tool_name":{"type":"string"},
			"tool_call_id":{"type":"string"},"args":{},"result":{},
			"success":{"type":"boolean"},"error":{"type":"string"},"duration_ms":` + count + `}}}`,
		Grader: `{"type":"object","maxProperties":128,"additionalProperties":{
			"type":"object","additionalProperties":false,
			"required":["identifier","type","score","weight","passed","feedback","duration_ms"],
			"properties":{"identifier":{"type":"string","minLength":1},"type":{"type":"string"},
			"score":` + number + `,"weight":` + number + `,"passed":{"type":"boolean"},
			"feedback":{"type":"string"},"duration_ms":` + count + `,"details":{"type":"object"}}}}`,
	}
	result := make(map[Category]*jsonschema.Schema, len(texts))
	for category, text := range texts {
		c := jsonschema.NewCompiler()
		c.UseLoader(schemaloader.Offline{})
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
		if err != nil {
			return nil, err
		}
		name := "recorded-" + string(category) + ".json"
		if err := c.AddResource(name, doc); err != nil {
			return nil, err
		}
		schema, err := c.Compile(name)
		if err != nil {
			return nil, err
		}
		result[category] = schema
	}
	return result, nil
})

// categoryShape checks original wire values. Missing content/completion remains
// incomplete; decoding native zero-value fields must not repair that evidence.
func categoryShape(category Category, data []byte, terminal *runnerterminal.Envelope, binding *runnerterminal.Binding) (State, int64, error) {
	value, err := jsonutil.Parse(data)
	if err != nil {
		return Unavailable, 0, fmt.Errorf("%w: recorded category JSON: %w", ErrInvalid, err)
	}
	schemas, err := categorySchemas()
	if err != nil {
		return Unavailable, 0, err
	}
	schema, ok := schemas[category]
	if !ok {
		return Unavailable, 0, fmt.Errorf("%w: unsupported recorded category shape", ErrInvalid)
	}
	if err := schema.Validate(value); err != nil {
		return Unavailable, 0, fmt.Errorf("%w: recorded category fields: %w", ErrInvalid, err)
	}
	state := Present
	switch category {
	case Conversation:
		events, ok := value.([]any)
		if !ok {
			return Unavailable, 0, fmt.Errorf("%w: recorded messages are not an array", ErrInvalid)
		}
		for _, event := range events {
			message, ok := event.(map[string]any)
			if !ok {
				return Unavailable, 0, fmt.Errorf("%w: recorded message is not an object", ErrInvalid)
			}
			content, exists := message["content"]
			if !exists || content == nil {
				state = Incomplete
			}
		}
		return state, int64(len(events)), nil
	case Tool:
		events, ok := value.([]any)
		if !ok {
			return Unavailable, 0, fmt.Errorf("%w: recorded tools are not an array", ErrInvalid)
		}
		var native []models.ToolEvent
		if err := json.Unmarshal(data, &native); err != nil {
			return Unavailable, 0, fmt.Errorf("%w: native tool fields: %w", ErrInvalid, err)
		}
		for _, event := range events {
			call, ok := event.(map[string]any)
			if !ok {
				return Unavailable, 0, fmt.Errorf("%w: recorded tool is not an object", ErrInvalid)
			}
			if _, exists := call["success"]; !exists || call["tool_name"] == "" {
				state = Incomplete
			}
			// A normalized false alone also represents an unfinished start.
			// Without completion/result/error evidence, withhold completeness.
			if call["success"] == false && call["result"] == nil && (call["error"] == nil || call["error"] == "") {
				state = Incomplete
			}
		}
		return state, int64(len(events)), nil
	case Grader:
		var results map[string]models.GraderResults
		if err := json.Unmarshal(data, &results); err != nil {
			return Unavailable, 0, fmt.Errorf("%w: native grader fields: %w", ErrInvalid, err)
		}
		expected := make(map[string]bool, len(binding.GraderIDs))
		for _, identifier := range binding.GraderIDs {
			expected[identifier] = true
		}
		verdicts := make(map[string]bool)
		if terminal.Observation != nil {
			for _, verdict := range terminal.Observation.Validations {
				verdicts[verdict.Identifier] = verdict.Passed
			}
		}
		for name, result := range results {
			passed, observed := verdicts[name]
			if name != result.Name || !expected[name] || !observed || passed != result.Passed {
				return Conflicting, int64(len(results)), nil
			}
		}
		if len(results) != len(expected) {
			state = Incomplete
		}
		return state, int64(len(results)), nil
	}
	return Unavailable, 0, fmt.Errorf("%w: recorded category is unsupported", ErrInvalid)
}
