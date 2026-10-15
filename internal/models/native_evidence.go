package models

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/microsoft/waza/internal/jsonutil"
)

// ValidateNativeEvidenceProfile prevents finite authored inputs from being
// attributed to historical runs. Full digest validation belongs to evidence.
func ValidateNativeEvidenceProfile(manifest *EvidenceManifest) error {
	if manifest == nil || manifest.Version != "1.0" || strings.HasPrefix(manifest.Origin.EvalID, "reference:") {
		return errors.New("evidence: native execution requires snapshot manifest version 1.0 and a nonsynthetic origin")
	}
	for _, artifact := range manifest.Artifacts {
		if artifact.Document == "supplied-finite-input" {
			return errors.New("evidence: authored input cannot be admitted as native execution evidence")
		}
	}
	return nil
}

func (run *RunResult) UnmarshalJSON(data []byte) error {
	return run.unmarshalJSON(data, false)
}

// UnmarshalJSONExact preserves dynamic numeric tokens for explicit evidence readers.
// Ordinary JSON decoding retains its legacy float64 representation.
func (run *RunResult) UnmarshalJSONExact(data []byte) error {
	return run.unmarshalJSON(data, true)
}

func (run *RunResult) unmarshalJSON(data []byte, exact bool) error {
	validate := ValidateNativeJSONAdmission
	if exact {
		validate = ValidateNativeJSONKeys
	}
	if err := validate(data, RunResult{}); err != nil {
		return err
	}
	type alias RunResult
	var decoded alias
	decoder := json.NewDecoder(bytes.NewReader(data))
	if exact {
		decoder.UseNumber()
	}
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if decoded.Evidence != nil {
		if err := ValidateNativeEvidenceProfile(decoded.Evidence); err != nil {
			return err
		}
	}
	*run = RunResult(decoded)
	return nil
}

// ValidateNativeJSONAdmission preserves the typed-key fence for legacy DTOs,
// but applies whole-document strictness only when evidence is consumed.
func ValidateNativeJSONAdmission(data []byte, target any) error {
	kind := reflect.TypeOf(target)
	if kind == nil {
		return errors.New("evidence: native JSON admission requires a target type")
	}
	if !utf8.Valid(data) {
		return errors.New("JSON is not UTF-8")
	}
	if err := jsonutil.ValidateUnicodeEscapes(data); err != nil {
		return err
	}
	if !json.Valid(data) {
		return errors.New("invalid JSON")
	}
	profile, err := inspectNativeJSONMembers(data, kind, 0)
	if err != nil {
		return err
	}
	if profile {
		return ValidateNativeJSONKeys(data, target)
	}
	return nil
}

// Raw members retain every occurrence; a last-key-wins header could hide
// evidence behind a later null, empty task list or baseline overwrite.
func inspectNativeJSONMembers(data []byte, kind reflect.Type, depth int) (bool, error) {
	for kind.Kind() == reflect.Pointer {
		kind = kind.Elem()
	}
	if depth > 64 {
		return false, errors.New("JSON exceeds the supported nesting depth")
	}
	if kind.Kind() != reflect.Struct && kind.Kind() != reflect.Slice && kind.Kind() != reflect.Array && kind.Kind() != reflect.Map {
		return false, nil
	}
	if kind.Kind() == reflect.Map && kind.Elem().Kind() == reflect.Interface {
		return false, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	open, err := decoder.Token()
	if err != nil {
		return false, err
	}
	if open == nil {
		return false, nil
	}
	object := kind.Kind() == reflect.Struct || kind.Kind() == reflect.Map
	expected := json.Delim('[')
	if object {
		expected = '{'
	}
	if open != expected {
		return false, nil // The typed decoder reports shape errors.
	}
	var fields map[string]reflect.Type
	if kind.Kind() == reflect.Struct {
		fields = nativeJSONFields(kind)
	}
	seen := make(map[string]bool)
	profile := false
	for decoder.More() {
		var memberKind reflect.Type
		if object {
			token, err := decoder.Token()
			if err != nil {
				return false, err
			}
			key, ok := token.(string)
			if !ok {
				return false, errors.New("invalid JSON object key")
			}
			if kind.Kind() == reflect.Struct {
				memberKind = fields[key]
				if memberKind == nil {
					for name := range fields {
						if strings.EqualFold(key, name) {
							return false, fmt.Errorf("evidence: native JSON key %q must use exact spelling %q", key, name)
						}
					}
				}
			} else {
				memberKind = kind.Elem()
			}
			if memberKind != nil {
				if seen[key] {
					return false, errors.New("duplicate JSON object key")
				}
				seen[key] = true
			}
		} else {
			memberKind = kind.Elem()
		}
		var member json.RawMessage
		if err := decoder.Decode(&member); err != nil {
			return false, err
		}
		if memberKind == nil {
			continue
		}
		if memberKind == reflect.TypeFor[*EvidenceManifest]() && !bytes.Equal(bytes.TrimSpace(member), []byte("null")) {
			profile = true
		}
		nested, err := inspectNativeJSONMembers(member, memberKind, depth+1)
		if err != nil {
			return false, err
		}
		profile = profile || nested
	}
	if _, err := decoder.Token(); err != nil {
		return false, err
	}
	return profile, nil
}

// ValidateNativeJSONKeys permits same-major unknown fields but never aliases
// of known native DTO keys. encoding/json otherwise accepts case-insensitive
// aliases that can overwrite already-seen provenance before profile admission.
func ValidateNativeJSONKeys(data []byte, target any) error {
	value, err := jsonutil.Parse(data)
	if err != nil {
		return err
	}
	kind := reflect.TypeOf(target)
	if kind == nil {
		return errors.New("evidence: native JSON admission requires a target type")
	}
	return validateNativeJSONKeys(value, kind)
}

func validateNativeJSONKeys(value any, kind reflect.Type) error {
	for kind.Kind() == reflect.Pointer {
		kind = kind.Elem()
	}
	switch kind.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		fields := nativeJSONFields(kind)
		for key, member := range object {
			field, exact := fields[key]
			if exact {
				if err := validateNativeJSONKeys(member, field); err != nil {
					return err
				}
				continue
			}
			for name := range fields {
				if strings.EqualFold(key, name) {
					return fmt.Errorf("evidence: native JSON key %q must use exact spelling %q", key, name)
				}
			}
		}
	case reflect.Slice, reflect.Array:
		if array, ok := value.([]any); ok {
			for _, member := range array {
				if err := validateNativeJSONKeys(member, kind.Elem()); err != nil {
					return err
				}
			}
		}
	case reflect.Map:
		if object, ok := value.(map[string]any); ok {
			for _, member := range object {
				if err := validateNativeJSONKeys(member, kind.Elem()); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

type nativeJSONField struct {
	kind      reflect.Type
	depth     int
	tagged    bool
	ambiguous bool
}

// JSON promotion uses depth and explicit tags, not reflect.VisibleFields' Go names.
func nativeJSONFields(kind reflect.Type) map[string]reflect.Type {
	if kind == reflect.TypeFor[TranscriptEvent]() {
		kind = reflect.TypeFor[transcriptEventJSON]()
	}
	candidates := map[string]nativeJSONField{}
	visiting := map[reflect.Type]bool{}
	var collect func(reflect.Type, int)
	collect = func(kind reflect.Type, depth int) {
		if visiting[kind] {
			return
		}
		visiting[kind] = true
		defer delete(visiting, kind)
		for i := 0; i < kind.NumField(); i++ {
			field := kind.Field(i)
			embedded := field.Type
			if embedded.Kind() == reflect.Pointer {
				embedded = embedded.Elem()
			}
			if !field.IsExported() && (!field.Anonymous || embedded.Kind() != reflect.Struct) {
				continue
			}
			tag := field.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name := nativeJSONTagName(tag)
			if field.Anonymous && embedded.Kind() == reflect.Struct && name == "" {
				collect(embedded, depth+1)
				continue
			}
			tagged := name != ""
			if name == "" {
				name = field.Name
			}
			next := nativeJSONField{kind: field.Type, depth: depth, tagged: tagged}
			previous, found := candidates[name]
			switch {
			case !found || depth < previous.depth:
				candidates[name] = next
			case depth == previous.depth:
				if tagged && !previous.tagged {
					candidates[name] = next
				} else if tagged == previous.tagged {
					previous.ambiguous = true
					candidates[name] = previous
				}
			}
		}
	}
	collect(kind, 0)
	fields := map[string]reflect.Type{}
	for name, field := range candidates {
		if !field.ambiguous {
			fields[name] = field.kind
		}
	}
	return fields
}

func nativeJSONTagName(tag string) string {
	name, _, _ := strings.Cut(tag, ",")
	for _, character := range name {
		if !unicode.IsLetter(character) && !unicode.IsDigit(character) &&
			!strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", character) {
			return ""
		}
	}
	return name
}
