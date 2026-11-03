package models

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strings"
	"sync"

	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

var capturedMetricSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	var document any
	if err := json.Unmarshal([]byte(schemas.EvalSchemaJSON), &document); err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaloader.Offline{})
	if err := compiler.AddResource("captured-eval.schema.json", document); err != nil {
		return nil, err
	}
	return compiler.Compile("captured-eval.schema.json#/$defs/metric")
})

func validateCapturedMetric(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("captured metric requires a mapping")
	}
	metric := make(map[string]any, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Anchor != "" || key.Value == "<<" {
			return fmt.Errorf("captured metric requires string keys without aliases or merges")
		}
		if _, exists := metric[key.Value]; exists {
			return fmt.Errorf("duplicate captured metric field %q", key.Value)
		}
		if value.Kind != yaml.ScalarNode || value.Anchor != "" {
			return fmt.Errorf("captured metric %q requires a scalar without aliases", key.Value)
		}
		switch value.Tag {
		case "!!int", "!!float":
			number, err := capturedMetricNumber(value)
			if err != nil {
				return fmt.Errorf("captured metric %q: %w", key.Value, err)
			}
			metric[key.Value] = number
		case "!!str", "!!bool", "!!null":
			var scalar any
			if err := value.Decode(&scalar); err != nil {
				return fmt.Errorf("decoding captured metric %q: %w", key.Value, err)
			}
			metric[key.Value] = scalar
		default:
			return fmt.Errorf("unsupported captured metric scalar tag %q", value.Tag)
		}
	}
	schema, err := capturedMetricSchema()
	if err != nil {
		return fmt.Errorf("compiling offline metric schema: %w", err)
	}
	if err := schema.Validate(metric); err != nil {
		return fmt.Errorf("invalid captured metric: %w", err)
	}
	return nil
}

func capturedMetricNumber(node *yaml.Node) (json.Number, error) {
	var native float64
	if err := node.Decode(&native); err != nil {
		return "", fmt.Errorf("decoding metric number: %w", err)
	}
	if math.IsInf(native, 0) || math.IsNaN(native) {
		return "", fmt.Errorf("metric numbers must be finite")
	}
	text := strings.ReplaceAll(node.Value, "_", "")
	if node.Tag == "!!int" {
		integer, ok := new(big.Int).SetString(text, 0)
		if !ok {
			return "", fmt.Errorf("invalid metric integer")
		}
		return json.Number(integer.String()), nil
	}
	// Bound exponent expansion before the schema's exact rational arithmetic,
	// and never round a near-boundary declaration into a valid default.
	mantissa, _, _ := strings.Cut(strings.ToLower(text), "e")
	if native == 0 {
		if strings.ContainsAny(mantissa, "123456789") {
			return "", fmt.Errorf("metric numbers must not underflow the native numeric range")
		}
		return json.Number("0"), nil
	}
	if _, ok := new(big.Rat).SetString(text); !ok {
		return "", fmt.Errorf("invalid decimal metric number")
	}
	return json.Number(text), nil
}
