//go:build ignore

// Regenerate structural release schemas with:
// go run internal/releasepolicy/schema_generate.go
// Normative cross-artifact/state/statistical validation remains in Go readers.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/microsoft/waza/internal/webapi"
)

type document map[string]any

type generator struct {
	definitions document
}

func (g *generator) schema(typ reflect.Type) document {
	if typ == reflect.TypeFor[json.Number]() {
		return document{"type": "number"}
	}
	if typ == reflect.TypeFor[time.Time]() {
		return document{"type": "string", "format": "date-time"}
	}
	if typ == reflect.TypeFor[json.RawMessage]() {
		return document{}
	}
	if typ.Kind() == reflect.Pointer {
		schema := g.schema(typ.Elem())
		if typ.Elem().Kind() == reflect.Bool {
			return document{"anyOf": []document{schema, {"type": "null"}}}
		}
		return schema
	}
	switch typ.Kind() {
	case reflect.Struct:
		name := filepath.Base(typ.PkgPath()) + "_" + typ.Name()
		if _, exists := g.definitions[name]; !exists {
			g.definitions[name] = document{}
			properties, required := g.fields(typ)
			g.definitions[name] = document{"type": "object", "additionalProperties": false,
				"properties": properties, "required": required}
		}
		return document{"$ref": "#/$defs/" + name}
	case reflect.Slice, reflect.Array:
		if typ.Elem().Kind() == reflect.Uint8 {
			return document{"type": []string{"string", "null"}, "contentEncoding": "base64"}
		}
		return document{"type": []string{"array", "null"}, "items": g.schema(typ.Elem())}
	case reflect.Map:
		return document{"type": []string{"object", "null"}, "additionalProperties": g.schema(typ.Elem())}
	case reflect.Bool:
		return document{"type": "boolean"}
	case reflect.String:
		if typ == reflect.TypeFor[releasepolicy.Arm]() {
			return document{"enum": []string{"baseline", "candidate"}}
		}
		return document{"type": "string"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return document{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return document{"type": "number"}
	case reflect.Interface:
		return document{}
	default:
		panic(fmt.Sprintf("unsupported schema type: %s", typ))
	}
}

func (g *generator) fields(typ reflect.Type) (document, []string) {
	properties, required := document{}, []string{}
	requiredNames := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag := field.Tag.Get("json")
		if !field.IsExported() || tag == "-" {
			continue
		}
		if field.Anonymous && tag == "" {
			children, names := g.fields(field.Type)
			for name, value := range children {
				properties[name] = value
			}
			for _, name := range names {
				if !requiredNames[name] {
					required = append(required, name)
					requiredNames[name] = true
				}
			}
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" {
			name = field.Name
		}
		properties[name] = g.schema(field.Type)
		if !strings.Contains(tag, ",omitempty") && !requiredNames[name] {
			required = append(required, name)
			requiredNames[name] = true
		}
	}
	return properties, required
}

func main() {
	g := generator{definitions: document{}}
	roots := []struct {
		name, kind string
		typ        reflect.Type
	}{
		{"release-policy", releasepolicy.PolicyKind, reflect.TypeFor[releasepolicy.Policy]()},
		{"release-resolved-plan", releasepolicy.PlanKind, reflect.TypeFor[releasepolicy.ResolvedPlan]()},
		{"release-receipt", releasepolicy.ReceiptKind, reflect.TypeFor[releasepolicy.Receipt]()},
		{"release-journal", releasepolicy.JournalKind, reflect.TypeFor[releasepolicy.Journal]()},
		{"release-result-binding", releasepolicy.BindingKind, reflect.TypeFor[releasepolicy.ResultBinding]()},
		{"release-results", "waza.release-results", reflect.TypeFor[releasepolicy.Results]()},
		{"release-decision", "waza.release-decision", reflect.TypeFor[releasepolicy.Decision]()},
		{"release-decision-view", "waza.release-decision-view", reflect.TypeFor[webapi.ReleaseDecision]()},
	}
	for _, root := range roots {
		g.schema(root.typ)
		definition := g.definitions[filepath.Base(root.typ.PkgPath())+"_"+root.typ.Name()].(document)
		properties := definition["properties"].(document)
		properties["kind"], properties["version"] = document{"const": root.kind}, document{"const": releasepolicy.Version}
	}
	policy := g.definitions["releasepolicy_Policy"].(document)["properties"].(document)
	policy["arms"] = document{"type": "object", "required": []string{"baseline", "candidate"},
		"additionalProperties": false, "properties": document{
			"baseline":  g.schema(reflect.TypeFor[releasepolicy.PolicyArm]()),
			"candidate": g.schema(reflect.TypeFor[releasepolicy.PolicyArm]())}}
	alternatives := []document{}
	for _, root := range roots {
		alternatives = append(alternatives, document{"$ref": "#/$defs/" + filepath.Base(root.typ.PkgPath()) + "_" + root.typ.Name()})
	}
	writeSchema("release-artifacts-1.0.schema.json", document{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"$id":         "https://raw.githubusercontent.com/microsoft/waza/main/schemas/release-artifacts-1.0.schema.json",
		"title":       "Waza release artifacts 1.0",
		"description": "Generated structural schema. Normative numeric-token identity, conditional availability, ordered replay, source binding and statistical admission are additionally enforced by Waza's strict Go reader.",
		"oneOf":       alternatives, "$defs": g.definitions})
	for _, root := range roots {
		schema := document{"$schema": "https://json-schema.org/draft/2020-12/schema",
			"$id":         "https://raw.githubusercontent.com/microsoft/waza/main/schemas/" + root.name + "-1.0.schema.json",
			"title":       root.kind + " 1.0",
			"description": "Generated structural schema. Normative numeric-token identity, conditional availability, ordered replay, source binding and statistical admission are additionally enforced by Waza's strict Go reader.",
			"$ref":        "release-artifacts-1.0.schema.json#/$defs/" + filepath.Base(root.typ.PkgPath()) + "_" + root.typ.Name()}
		writeSchema(root.name+"-1.0.schema.json", schema)
	}
}

func writeSchema(name string, schema document) {
	data, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join("schemas", name), append(data, '\n'), 0o644); err != nil {
		panic(err)
	}
}
