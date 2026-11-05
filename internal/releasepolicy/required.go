package releasepolicy

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// Typed decoding alone silently supplies zero values for absent fields. A sealed
// document must explicitly declare every non-optional contract property.
func requireFields(value any, typ reflect.Type, path string) error {
	for typ.Kind() == reflect.Pointer {
		if value == nil {
			if typ.Elem().Kind() == reflect.Bool {
				return nil // Incomplete trial endpoint, explicitly present as null.
			}
			return fmt.Errorf("%s cannot be null", path)
		}
		typ = typ.Elem()
	}
	if value == nil && typ.Kind() != reflect.Slice && typ.Kind() != reflect.Map && typ.Kind() != reflect.Interface {
		return fmt.Errorf("%s cannot silently default from null", path)
	}
	if typ == reflect.TypeFor[json.Number]() {
		if _, ok := value.(json.Number); !ok {
			return fmt.Errorf("%s must be an exact JSON numeric token, not a string", path)
		}
		return nil
	}
	switch typ.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", path)
		}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if !field.IsExported() {
				continue
			}
			tag := field.Tag.Get("json")
			if tag == "-" {
				continue
			}
			if field.Anonymous && tag == "" {
				if err := requireFields(value, field.Type, path); err != nil {
					return err
				}
				continue
			}
			name := strings.Split(tag, ",")[0]
			if name == "" {
				name = field.Name
			}
			child, exists := object[name]
			if !exists {
				if strings.Contains(tag, ",omitempty") {
					continue
				}
				return fmt.Errorf("%s/%s is required", path, name)
			}
			if err := requireFields(child, field.Type, path+"/"+name); err != nil {
				return err
			}
		}
	case reflect.Map:
		if object, ok := value.(map[string]any); ok {
			for key, child := range object {
				if err := requireFields(child, typ.Elem(), path+"/"+key); err != nil {
					return err
				}
			}
		}
	case reflect.Slice, reflect.Array:
		if array, ok := value.([]any); ok {
			for i, child := range array {
				if err := requireFields(child, typ.Elem(), fmt.Sprintf("%s/%d", path, i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
