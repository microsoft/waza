package snapshot

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/microsoft/waza/internal/jsonutil"
)

// redactJSON normalizes typed payloads before redaction without rounding large
// integers or retaining references to the execution's original data.
func (p *Policy) redactJSON(value any) (any, error) {
	if err := checkJSONPayload(reflect.ValueOf(value), 0); err != nil {
		return nil, err
	}
	data, err := json.Marshal(value)
	if err != nil {
		// MarshalJSON errors may contain the original secret-bearing payload.
		return nil, errors.New("payload cannot be encoded as JSON")
	}
	normalized, err := jsonutil.Parse(data)
	if err != nil {
		return nil, errors.New("payload cannot be decoded as JSON")
	}
	return p.redactJSONValue(normalized)
}

func checkJSONPayload(value reflect.Value, depth int) error {
	if !value.IsValid() {
		return nil
	}
	if depth > 64 {
		return errors.New("payload is cyclic or exceeds the supported nesting depth")
	}
	if value.Type() == reflect.TypeFor[json.RawMessage]() {
		return nil
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if !value.IsNil() {
			return checkJSONPayload(value.Elem(), depth+1)
		}
	case reflect.Slice, reflect.Array:
		if value.Type().Elem().Kind() == reflect.Uint8 {
			return errors.New("opaque byte payload is unsupported; provide decoded JSON or text")
		}
		for i := 0; i < value.Len(); i++ {
			if err := checkJSONPayload(value.Index(i), depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		iter := value.MapRange()
		for iter.Next() {
			if err := checkJSONPayload(iter.Value(), depth+1); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			field := value.Type().Field(i)
			if !field.IsExported() || field.Tag.Get("json") == "-" {
				continue
			}
			if err := checkJSONPayload(value.Field(i), depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *Policy) redactJSONValue(value any) (any, error) {
	switch value := value.(type) {
	case map[string]any:
		for key, item := range value {
			if p.RedactString(key) != key {
				return nil, errors.New("object key requires redaction; cannot preserve its identity safely")
			}
			if p.IsSensitiveKey(key) {
				value[key] = RedactionPlaceholder
				p.recordSensitiveMatch()
				continue
			}
			redacted, err := p.redactJSONValue(item)
			if err != nil {
				return nil, err
			}
			value[key] = redacted
		}
	case []any:
		for i, item := range value {
			redacted, err := p.redactJSONValue(item)
			if err != nil {
				return nil, err
			}
			value[i] = redacted
		}
	case string:
		return p.RedactString(value), nil
	}
	return value, nil
}

func (p *Policy) recordSensitiveMatch() {
	if p.matchedRules == nil {
		p.matchedRules = make(map[string]bool)
	}
	p.matchedRules["sensitive_key"] = true
	p.matchCount++
}

func (p *Policy) checkIdentity(value, field string) error {
	if p.RedactString(value) != value {
		return fmt.Errorf("snapshot: %s requires redaction; cannot preserve its identity safely", field)
	}
	return nil
}

func redactTyped[T any](value T, policy *Policy) (T, error) {
	var output T
	normalized, err := policy.redactJSON(value)
	if err != nil {
		return output, err
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return output, errors.New("normalized payload cannot be encoded")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&output); err != nil {
		return output, errors.New("redacted payload no longer matches its required shape")
	}
	return output, nil
}
