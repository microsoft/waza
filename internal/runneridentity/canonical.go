package runneridentity

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/microsoft/waza/internal/runnercontract"
	"gopkg.in/yaml.v3"
)

var decimalFloat = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

// CanonicalSource encodes one captured YAML mapping without applying execution
// defaults. The versioned encoding is specified in docs/RUNNER_IDENTITIES.md.
func CanonicalSource(source string) ([]byte, error) {
	if len(source) > runnercontract.MaxRequestBytes {
		return nil, fmt.Errorf("identity source exceeds %d bytes", runnercontract.MaxRequestBytes)
	}
	if !utf8.ValidString(source) {
		return nil, errors.New("identity source is not UTF-8")
	}
	decoder := yaml.NewDecoder(strings.NewReader(source))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("parsing identity source: %w", err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("identity source must contain one YAML mapping")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("reading identity source end: %w", err)
		}
		return nil, errors.New("identity source contains multiple YAML documents")
	}
	var output bytes.Buffer
	if err := encodeNode(&output, document.Content[0], 0); err != nil {
		return nil, fmt.Errorf("canonicalizing identity source: %w", err)
	}
	return output.Bytes(), nil
}

func frame(output *bytes.Buffer, tag byte, value string) {
	output.WriteByte(tag)
	output.WriteString(strconv.Itoa(len(value)))
	output.WriteByte(':')
	output.WriteString(value)
}

func count(output *bytes.Buffer, tag byte, size int) {
	output.WriteByte(tag)
	output.WriteString(strconv.Itoa(size))
	output.WriteByte(':')
}

func encodeNode(output *bytes.Buffer, node *yaml.Node, depth int) error {
	if depth > 64 {
		return errors.New("identity source exceeds the supported nesting depth")
	}
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return errors.New("identity source cannot contain aliases or anchors")
	}
	switch node.Kind {
	case yaml.MappingNode:
		if node.Tag != "!!map" {
			return fmt.Errorf("unsupported identity mapping tag %q", node.Tag)
		}
		members := make(map[string]*yaml.Node, len(node.Content)/2)
		keys := make([]string, 0, len(node.Content)/2)
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Anchor != "" {
				return errors.New("identity mapping keys must be unanchored strings")
			}
			if _, duplicate := members[key.Value]; duplicate {
				return fmt.Errorf("duplicate identity mapping key %q", key.Value)
			}
			keys = append(keys, key.Value)
			members[key.Value] = node.Content[i+1]
		}
		slices.Sort(keys)
		count(output, 'm', len(keys))
		for _, key := range keys {
			frame(output, 's', key)
			if err := encodeNode(output, members[key], depth+1); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		if node.Tag != "!!seq" {
			return fmt.Errorf("unsupported identity sequence tag %q", node.Tag)
		}
		count(output, 'a', len(node.Content))
		for _, child := range node.Content {
			if err := encodeNode(output, child, depth+1); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		return encodeScalar(output, node)
	default:
		return errors.New("unsupported identity YAML node")
	}
	return nil
}

func encodeScalar(output *bytes.Buffer, node *yaml.Node) error {
	switch node.Tag {
	case "!!str":
		frame(output, 's', node.Value)
	case "!!null":
		var value any
		if err := node.Decode(&value); err != nil || value != nil {
			return errors.New("invalid identity null scalar")
		}
		output.WriteByte('n')
	case "!!bool":
		var value bool
		if err := node.Decode(&value); err != nil {
			return fmt.Errorf("invalid identity boolean: %w", err)
		}
		if value {
			output.WriteString("b1")
		} else {
			output.WriteString("b0")
		}
	case "!!int":
		value, ok := new(big.Int).SetString(strings.ReplaceAll(node.Value, "_", ""), 0)
		if !ok {
			return errors.New("invalid identity integer scalar")
		}
		frame(output, 'i', value.String())
	case "!!float":
		value, err := canonicalFloat(node.Value)
		if err != nil {
			return err
		}
		frame(output, 'f', value)
	default:
		return fmt.Errorf("unsupported identity scalar tag %q", node.Tag)
	}
	return nil
}

// Decimal arithmetic keeps all source digits, including values outside binary64.
// The exponent stays symbolic so a short source cannot expand into huge output.
func canonicalFloat(raw string) (string, error) {
	text := strings.ReplaceAll(raw, "_", "")
	if !decimalFloat.MatchString(text) {
		return "", errors.New("identity floats must be finite decimal scalars")
	}
	negative := strings.HasPrefix(text, "-")
	text = strings.TrimPrefix(strings.TrimPrefix(text, "-"), "+")
	mantissa, exponentText, hasExponent := strings.Cut(strings.ToLower(text), "e")
	exponent := new(big.Int)
	if hasExponent {
		if _, ok := exponent.SetString(exponentText, 10); !ok {
			return "", errors.New("invalid identity float exponent")
		}
	}
	whole, fraction, _ := strings.Cut(mantissa, ".")
	digits := strings.TrimLeft(whole+fraction, "0")
	if digits == "" {
		if negative {
			return "-0e0", nil
		}
		return "0e0", nil
	}
	trimmed := strings.TrimRight(digits, "0")
	exponent.Add(exponent, big.NewInt(int64(len(digits)-len(trimmed)-len(fraction))))
	if negative {
		trimmed = "-" + trimmed
	}
	return trimmed + "e" + exponent.String(), nil
}
