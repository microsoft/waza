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
	output, _, err := p.redactJSONWithChanges(value)
	return output, err
}

func (p *Policy) redactJSONWithChanges(value any) (any, bool, error) {
	if err := checkJSONPayload(reflect.ValueOf(value), 0); err != nil {
		return nil, false, err
	}
	data, err := json.Marshal(value)
	if err != nil {
		// MarshalJSON errors may contain the original secret-bearing payload.
		return nil, false, errors.New("payload cannot be encoded as JSON")
	}
	normalized, err := jsonutil.Parse(data)
	if err != nil {
		return nil, false, errors.New("payload cannot be decoded as JSON")
	}
	return p.redactJSONValueWithChanges(normalized)
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

func (p *Policy) redactJSONValueWithChanges(value any) (any, bool, error) {
	changed := false
	switch value := value.(type) {
	case map[string]any:
		for key, item := range value {
			if p.RedactString(key) != key {
				return nil, false, errors.New("object key requires redaction; cannot preserve its identity safely")
			}
			if p.IsSensitiveKey(key) {
				text, ok := item.(string)
				changed = changed || !ok || text != RedactionPlaceholder
				value[key] = RedactionPlaceholder
				p.recordSensitiveMatch()
				continue
			}
			redacted, itemChanged, err := p.redactJSONValueWithChanges(item)
			if err != nil {
				return nil, false, err
			}
			changed = changed || itemChanged
			value[key] = redacted
		}
	case []any:
		for i, item := range value {
			redacted, itemChanged, err := p.redactJSONValueWithChanges(item)
			if err != nil {
				return nil, false, err
			}
			changed = changed || itemChanged
			value[i] = redacted
		}
	case string:
		redacted := p.RedactString(value)
		return redacted, redacted != value, nil
	}
	return value, changed, nil
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

func redactTypedWithChanges[T any](value T, policy *Policy) (T, bool, error) {
	var output T
	normalized, changed, err := policy.redactJSONWithChanges(value)
	if err != nil {
		return output, false, err
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return output, false, errors.New("normalized payload cannot be encoded")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&output); err != nil {
		return output, false, errors.New("redacted payload no longer matches its required shape")
	}
	return output, changed, nil
}
