package storage

import (
	"fmt"
	"reflect"
	"time"

	"github.com/microsoft/waza/internal/models"
)

// cloneDashboardValue only receives trees decoded from result artifacts, never
// caller data (which could contain cycles). Struct value copies retain private
// presence flags; mutable exported fields, including typed SDK data, are detached
// without invoking lossy JSON marshalers. time.Time's private location is immutable;
// EvidenceManifest owns its private raw bytes and supplies a native Clone.
func cloneDashboardValue(value reflect.Value) (reflect.Value, error) {
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface:
		if value.IsNil() {
			return value, nil
		}
		element, err := cloneDashboardValue(value.Elem())
		if err != nil {
			return reflect.Value{}, err
		}
		copy := reflect.New(value.Type()).Elem()
		if value.Kind() == reflect.Pointer {
			copy.Set(reflect.New(value.Type().Elem()))
			copy.Elem().Set(element)
		} else {
			copy.Set(element)
		}
		return copy, nil
	case reflect.Struct:
		switch value.Type() {
		case reflect.TypeFor[time.Time]():
			return value, nil
		case reflect.TypeFor[models.EvidenceManifest]():
			if !value.CanInterface() {
				return reflect.Value{}, fmt.Errorf("cannot isolate unexported %s", value.Type())
			}
			manifest, ok := value.Interface().(models.EvidenceManifest)
			if !ok {
				return reflect.Value{}, fmt.Errorf("cannot isolate %s", value.Type())
			}
			return reflect.ValueOf(*manifest.Clone()), nil
		}
		copy := reflect.New(value.Type()).Elem()
		copy.Set(value)
		for i := range value.NumField() {
			field := value.Type().Field(i)
			if !field.IsExported() {
				if dashboardTypeHasReferences(field.Type) {
					return reflect.Value{}, fmt.Errorf("cannot isolate private mutable field %s.%s", value.Type(), field.Name)
				}
				continue
			}
			if !dashboardTypeHasReferences(field.Type) {
				continue
			}
			cloned, err := cloneDashboardValue(value.Field(i))
			if err != nil {
				return reflect.Value{}, err
			}
			copy.Field(i).Set(cloned)
		}
		return copy, nil
	case reflect.Slice:
		if value.IsNil() {
			return value, nil
		}
		copy := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		reflect.Copy(copy, value)
		if !dashboardTypeHasReferences(value.Type().Elem()) {
			return copy, nil
		}
		for i := range value.Len() {
			element, err := cloneDashboardValue(value.Index(i))
			if err != nil {
				return reflect.Value{}, err
			}
			copy.Index(i).Set(element)
		}
		return copy, nil
	case reflect.Map:
		if value.IsNil() {
			return value, nil
		}
		copy := reflect.MakeMapWithSize(value.Type(), value.Len())
		entries := value.MapRange()
		for entries.Next() {
			key, err := cloneDashboardValue(entries.Key())
			if err != nil {
				return reflect.Value{}, err
			}
			element, err := cloneDashboardValue(entries.Value())
			if err != nil {
				return reflect.Value{}, err
			}
			copy.SetMapIndex(key, element)
		}
		return copy, nil
	case reflect.Array:
		copy := reflect.New(value.Type()).Elem()
		for i := range value.Len() {
			element, err := cloneDashboardValue(value.Index(i))
			if err != nil {
				return reflect.Value{}, err
			}
			copy.Index(i).Set(element)
		}
		return copy, nil
	case reflect.Chan, reflect.Func, reflect.UnsafePointer:
		return reflect.Value{}, fmt.Errorf("cannot isolate result value of type %s", value.Type())
	default:
		return value, nil
	}
}

func dashboardTypeHasReferences(value reflect.Type) bool {
	switch value.Kind() {
	case reflect.Struct:
		if value == reflect.TypeFor[time.Time]() {
			return false
		}
		for i := range value.NumField() {
			if dashboardTypeHasReferences(value.Field(i).Type) {
				return true
			}
		}
		return false
	case reflect.Array:
		return dashboardTypeHasReferences(value.Elem())
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128, reflect.String:
		return false
	default:
		return true
	}
}
