package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

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
	if err := ValidateNativeJSONKeys(data, RunResult{}); err != nil {
		return err
	}
	type alias RunResult
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
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
		fields := make(map[string]reflect.Type)
		for i := 0; i < kind.NumField(); i++ {
			field := kind.Field(i)
			if field.PkgPath != "" {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			fields[name] = field.Type
		}
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
