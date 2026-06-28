// Package argmatcher implements structured value matchers used by the
// tool_calls and tool_constraint graders to validate tool-call arguments.
//
// A Matcher is a tagged union of one of these kinds:
//
//	equals      — strict equality (after JSON round-trip normalization).
//	regex       — Go regexp matched against the value's string form.
//	contains    — substring match against the value's string form.
//	range       — numeric range check (gte / lte / gt / lt, inclusive bounds default).
//	json_schema — JSON Schema (draft-07+) validation of the value.
//
// Exactly one kind must be set per Matcher. The package decodes its YAML form
// from a single-key mapping like:
//
//	query:
//	  regex: "^auth"
//	count:
//	  range: { gte: 1, lte: 5 }
//
// Matchers are stable and replay-friendly: their decoded representation can be
// re-serialized to JSON without loss, which keeps `tool_events[]` snapshots
// deterministic across runs (see issue #366 / Wave 3 #367).
package argmatcher

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// Kind enumerates the matcher kinds supported by [Matcher].
type Kind string

const (
	KindEquals     Kind = "equals"
	KindRegex      Kind = "regex"
	KindContains   Kind = "contains"
	KindRange      Kind = "range"
	KindJSONSchema Kind = "json_schema"
)

// Matcher is a tagged union of one matcher kind. Use [Matcher.Match] to test a
// value. Construct directly or via YAML/JSON decoding.
//
// Exactly one of the kind-specific fields must be populated. The Compile
// method (called automatically after YAML decoding) validates this invariant
// and prepares any cached state (compiled regex, compiled JSON schema).
type Matcher struct {
	Kind Kind `json:"kind" yaml:"-"`

	// Equals holds the expected literal value when Kind == KindEquals.
	Equals any `json:"equals,omitempty" yaml:"-"`

	// Regex holds the regular expression source when Kind == KindRegex.
	Regex string `json:"regex,omitempty" yaml:"-"`

	// Contains holds the substring expected to appear in the value's
	// string form when Kind == KindContains.
	Contains string `json:"contains,omitempty" yaml:"-"`

	// Range carries the numeric bounds for Kind == KindRange.
	Range *RangeSpec `json:"range,omitempty" yaml:"-"`

	// JSONSchema carries the schema document for Kind == KindJSONSchema.
	JSONSchema map[string]any `json:"json_schema,omitempty" yaml:"-"`

	// compiled state (populated by Compile, not serialized).
	compiledRegex  *regexp.Regexp
	compiledSchema *jsonschema.Schema
}

// RangeSpec specifies an inclusive (gte/lte) or exclusive (gt/lt) numeric
// bound. Any combination of fields may be set; at least one is required.
type RangeSpec struct {
	GTE *float64 `json:"gte,omitempty" yaml:"gte,omitempty"`
	LTE *float64 `json:"lte,omitempty" yaml:"lte,omitempty"`
	GT  *float64 `json:"gt,omitempty" yaml:"gt,omitempty"`
	LT  *float64 `json:"lt,omitempty" yaml:"lt,omitempty"`
}

// UnmarshalYAML decodes a single-key mapping into a tagged Matcher.
func (m *Matcher) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("argmatcher: expected mapping (e.g. {regex: '^foo'}), got %s", yamlKindName(node.Kind))
	}
	if len(node.Content) == 0 {
		return fmt.Errorf("argmatcher: empty matcher; expected one of equals, regex, contains, range, json_schema")
	}
	if len(node.Content) > 2 {
		var keys []string
		for i := 0; i < len(node.Content); i += 2 {
			keys = append(keys, node.Content[i].Value)
		}
		return fmt.Errorf("argmatcher: exactly one matcher kind allowed, got %d (%s)", len(keys), strings.Join(keys, ", "))
	}

	key := node.Content[0].Value
	value := node.Content[1]

	switch Kind(key) {
	case KindEquals:
		var v any
		if err := value.Decode(&v); err != nil {
			return fmt.Errorf("argmatcher equals: %w", err)
		}
		m.Kind = KindEquals
		m.Equals = v
	case KindRegex:
		var s string
		if err := value.Decode(&s); err != nil {
			return fmt.Errorf("argmatcher regex: %w", err)
		}
		m.Kind = KindRegex
		m.Regex = s
	case KindContains:
		var s string
		if err := value.Decode(&s); err != nil {
			return fmt.Errorf("argmatcher contains: %w", err)
		}
		m.Kind = KindContains
		m.Contains = s
	case KindRange:
		var r RangeSpec
		if err := value.Decode(&r); err != nil {
			return fmt.Errorf("argmatcher range: %w", err)
		}
		m.Kind = KindRange
		m.Range = &r
	case KindJSONSchema:
		var schema map[string]any
		if err := value.Decode(&schema); err != nil {
			return fmt.Errorf("argmatcher json_schema: %w", err)
		}
		m.Kind = KindJSONSchema
		m.JSONSchema = schema
	default:
		return fmt.Errorf("argmatcher: unknown matcher kind %q (allowed: equals, regex, contains, range, json_schema)", key)
	}

	return m.Compile()
}

// UnmarshalJSON mirrors the YAML decoder for results-file round trips.
func (m *Matcher) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	delete(raw, "kind")
	if len(raw) == 0 {
		return fmt.Errorf("argmatcher: empty matcher; expected one of equals, regex, contains, range, json_schema")
	}
	if len(raw) > 1 {
		keys := make([]string, 0, len(raw))
		for k := range raw {
			keys = append(keys, k)
		}
		return fmt.Errorf("argmatcher: exactly one matcher kind allowed, got %d (%s)", len(keys), strings.Join(keys, ", "))
	}
	for key, value := range raw {
		switch Kind(key) {
		case KindEquals:
			var v any
			if err := json.Unmarshal(value, &v); err != nil {
				return fmt.Errorf("argmatcher equals: %w", err)
			}
			m.Kind = KindEquals
			m.Equals = v
		case KindRegex:
			var s string
			if err := json.Unmarshal(value, &s); err != nil {
				return fmt.Errorf("argmatcher regex: %w", err)
			}
			m.Kind = KindRegex
			m.Regex = s
		case KindContains:
			var s string
			if err := json.Unmarshal(value, &s); err != nil {
				return fmt.Errorf("argmatcher contains: %w", err)
			}
			m.Kind = KindContains
			m.Contains = s
		case KindRange:
			var r RangeSpec
			if err := json.Unmarshal(value, &r); err != nil {
				return fmt.Errorf("argmatcher range: %w", err)
			}
			m.Kind = KindRange
			m.Range = &r
		case KindJSONSchema:
			var schema map[string]any
			if err := json.Unmarshal(value, &schema); err != nil {
				return fmt.Errorf("argmatcher json_schema: %w", err)
			}
			m.Kind = KindJSONSchema
			m.JSONSchema = schema
		default:
			return fmt.Errorf("argmatcher: unknown matcher kind %q", key)
		}
	}
	return m.Compile()
}

// MarshalJSON emits a single-key object matching the YAML/JSON input shape,
// keeping `tool_events[]` snapshots stable for replay.
func (m Matcher) MarshalJSON() ([]byte, error) {
	switch m.Kind {
	case KindEquals:
		return json.Marshal(map[string]any{"equals": m.Equals})
	case KindRegex:
		return json.Marshal(map[string]any{"regex": m.Regex})
	case KindContains:
		return json.Marshal(map[string]any{"contains": m.Contains})
	case KindRange:
		return json.Marshal(map[string]any{"range": m.Range})
	case KindJSONSchema:
		return json.Marshal(map[string]any{"json_schema": m.JSONSchema})
	case "":
		return []byte("null"), nil
	default:
		return nil, fmt.Errorf("argmatcher: cannot marshal unknown kind %q", m.Kind)
	}
}

// IsCompiled reports whether Compile() has cached state for the matcher's
// kind. Only KindRegex and KindJSONSchema have compiled state; other kinds
// return true because they need no precompilation. Primarily useful as a
// test/diagnostic hook to verify Compile() side-effects persisted across map
// reads (issue #366 review feedback).
func (m *Matcher) IsCompiled() bool {
	switch m.Kind {
	case KindRegex:
		return m.compiledRegex != nil
	case KindJSONSchema:
		return m.compiledSchema != nil
	default:
		return true
	}
}

// Compile validates the matcher invariants and pre-compiles any embedded
// regex/schema. It is safe to call multiple times.
func (m *Matcher) Compile() error {
	switch m.Kind {
	case KindEquals:
		// nothing to compile; presence of value is allowed to be nil (matches null).
	case KindRegex:
		if m.Regex == "" {
			return fmt.Errorf("argmatcher: regex matcher requires a non-empty pattern")
		}
		re, err := regexp.Compile(m.Regex)
		if err != nil {
			return fmt.Errorf("argmatcher: invalid regex %q: %w", m.Regex, err)
		}
		m.compiledRegex = re
	case KindContains:
		if m.Contains == "" {
			return fmt.Errorf("argmatcher: contains matcher requires a non-empty substring")
		}
	case KindRange:
		if m.Range == nil {
			return fmt.Errorf("argmatcher: range matcher requires at least one bound (gte, lte, gt, lt)")
		}
		if m.Range.GTE == nil && m.Range.LTE == nil && m.Range.GT == nil && m.Range.LT == nil {
			return fmt.Errorf("argmatcher: range matcher requires at least one bound (gte, lte, gt, lt)")
		}
	case KindJSONSchema:
		if len(m.JSONSchema) == 0 {
			return fmt.Errorf("argmatcher: json_schema matcher requires a non-empty schema")
		}
		schemaJSON, err := json.Marshal(m.JSONSchema)
		if err != nil {
			return fmt.Errorf("argmatcher: failed to serialize json_schema: %w", err)
		}
		var schemaVal any
		if err := json.Unmarshal(schemaJSON, &schemaVal); err != nil {
			return fmt.Errorf("argmatcher: failed to parse json_schema: %w", err)
		}
		compiler := jsonschema.NewCompiler()
		if err := compiler.AddResource("argmatcher.json", schemaVal); err != nil {
			return fmt.Errorf("argmatcher: failed to register json_schema: %w", err)
		}
		compiled, err := compiler.Compile("argmatcher.json")
		if err != nil {
			return fmt.Errorf("argmatcher: invalid json_schema: %w", err)
		}
		m.compiledSchema = compiled
	case "":
		return fmt.Errorf("argmatcher: matcher kind not set")
	default:
		return fmt.Errorf("argmatcher: unknown matcher kind %q", m.Kind)
	}
	return nil
}

// Match tests the value against the matcher. It returns ok=true when the
// value satisfies the matcher and an explanation string suitable for grader
// feedback.
func (m *Matcher) Match(value any) (bool, string) {
	switch m.Kind {
	case KindEquals:
		return matchEquals(m.Equals, value)
	case KindRegex:
		if m.compiledRegex == nil {
			if err := m.Compile(); err != nil {
				return false, err.Error()
			}
		}
		return matchRegex(m.compiledRegex, m.Regex, value)
	case KindContains:
		return matchContains(m.Contains, value)
	case KindRange:
		return matchRange(m.Range, value)
	case KindJSONSchema:
		if m.compiledSchema == nil {
			if err := m.Compile(); err != nil {
				return false, err.Error()
			}
		}
		return matchSchema(m.compiledSchema, value)
	default:
		return false, fmt.Sprintf("argmatcher: unsupported matcher kind %q", m.Kind)
	}
}

func matchEquals(expected, actual any) (bool, string) {
	expectedNorm, err := jsonNormalize(expected)
	if err != nil {
		return false, fmt.Sprintf("argmatcher equals: cannot normalise expected: %v", err)
	}
	actualNorm, err := jsonNormalize(actual)
	if err != nil {
		return false, fmt.Sprintf("argmatcher equals: cannot normalise actual: %v", err)
	}
	if reflect.DeepEqual(expectedNorm, actualNorm) {
		return true, fmt.Sprintf("equals %s", renderValue(expectedNorm))
	}
	return false, fmt.Sprintf("expected %s, got %s", renderValue(expectedNorm), renderValue(actualNorm))
}

func matchRegex(re *regexp.Regexp, pattern string, actual any) (bool, string) {
	s := toString(actual)
	if re.MatchString(s) {
		return true, fmt.Sprintf("regex %q matched", pattern)
	}
	return false, fmt.Sprintf("regex %q did not match %q", pattern, s)
}

func matchContains(needle string, actual any) (bool, string) {
	s := toString(actual)
	if strings.Contains(s, needle) {
		return true, fmt.Sprintf("contains %q", needle)
	}
	return false, fmt.Sprintf("expected substring %q, got %q", needle, s)
}

func matchRange(spec *RangeSpec, actual any) (bool, string) {
	n, ok := toFloat(actual)
	if !ok {
		return false, fmt.Sprintf("range: value %v is not numeric", actual)
	}
	if spec.GTE != nil && n < *spec.GTE {
		return false, fmt.Sprintf("range: %v < gte %v", n, *spec.GTE)
	}
	if spec.GT != nil && n <= *spec.GT {
		return false, fmt.Sprintf("range: %v <= gt %v", n, *spec.GT)
	}
	if spec.LTE != nil && n > *spec.LTE {
		return false, fmt.Sprintf("range: %v > lte %v", n, *spec.LTE)
	}
	if spec.LT != nil && n >= *spec.LT {
		return false, fmt.Sprintf("range: %v >= lt %v", n, *spec.LT)
	}
	return true, fmt.Sprintf("range: %v within bounds", n)
}

func matchSchema(schema *jsonschema.Schema, actual any) (bool, string) {
	norm, err := jsonNormalize(actual)
	if err != nil {
		return false, fmt.Sprintf("json_schema: %v", err)
	}
	if err := schema.Validate(norm); err != nil {
		return false, fmt.Sprintf("json_schema: %v", err)
	}
	return true, "json_schema: valid"
}

func jsonNormalize(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func toString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

func renderValue(v any) string {
	if v == nil {
		return "null"
	}
	if s, ok := v.(string); ok {
		return fmt.Sprintf("%q", s)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func yamlKindName(k yaml.Kind) string {
	switch k {
	case yaml.DocumentNode:
		return "document"
	case yaml.SequenceNode:
		return "sequence"
	case yaml.MappingNode:
		return "mapping"
	case yaml.ScalarNode:
		return "scalar"
	case yaml.AliasNode:
		return "alias"
	default:
		return fmt.Sprintf("kind(%d)", k)
	}
}
