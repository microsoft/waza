//go:build ignore

// Regenerate only the independent offline profile:
// go run schemas/assurance_generate.go
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/microsoft/waza/internal/assurance"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/releasepolicy"
)

type document map[string]any

type generator struct{ definitions document }

func (g *generator) schema(typ reflect.Type) document {
	switch typ {
	case reflect.TypeFor[releasepolicy.Decision]():
		return document{"$ref": "release-artifacts-1.0.schema.json#/$defs/releasepolicy_Decision"}
	case reflect.TypeFor[assurance.Report]():
		return document{"allOf": []document{
			{"$ref": "grader-assurance-1.0.schema.json"},
			{"properties": document{
				"calibration": document{"properties": document{
					"executions": document{"const": 0}, "usage": document{"type": "null"},
					"credits": document{"type": "null"},
				}},
				"requirements": document{"items": document{"properties": document{
					"observations": document{"items": document{"properties": document{
						"source_scope": document{"const": "authored_finite_output"},
					}}},
				}}},
			}},
		}}
	case reflect.TypeFor[models.EvidenceDigest]():
		return document{"$ref": "#/$defs/jsonDigest"}
	}
	if typ.Kind() == reflect.Pointer {
		return g.schema(typ.Elem())
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
	case reflect.Slice:
		return document{"type": []string{"array", "null"}, "items": g.schema(typ.Elem())}
	case reflect.Map:
		return document{"type": "object", "additionalProperties": g.schema(typ.Elem())}
	case reflect.Bool:
		return document{"type": "boolean"}
	case reflect.String:
		if typ == reflect.TypeFor[releasepolicy.Arm]() {
			return document{"enum": []string{"baseline", "candidate"}}
		}
		return document{"type": "string"}
	case reflect.Int:
		return document{"type": "integer", "minimum": 0}
	default:
		panic(fmt.Sprintf("unsupported schema type %s", typ))
	}
}

func (g *generator) fields(typ reflect.Type) (document, []string) {
	properties, required := document{}, []string{}
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
			required = append(required, names...)
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" {
			name = field.Name
		}
		properties[name] = g.schema(field.Type)
		if !strings.Contains(tag, ",omitempty") {
			required = append(required, name)
		}
	}
	return properties, required
}

func digest(encoding string) document {
	return document{"type": "object", "additionalProperties": false,
		"description": "SHA-256 in the " + encoding + " domain; not interchangeable with other encodings or proof of authenticity.",
		"required":    []string{"encoding", "sha256"}, "properties": document{
			"encoding": document{"const": encoding},
			"sha256":   document{"type": "string", "pattern": "^[0-9a-f]{64}$"},
		}}
}

func (g *generator) properties(name string) document {
	return g.definitions[name].(document)["properties"].(document)
}

func arms(value document, required bool) document {
	schema := document{"type": "object", "additionalProperties": false,
		"properties": document{"baseline": value, "candidate": value}}
	if required {
		schema["required"] = []string{"baseline", "candidate"}
	}
	return schema
}

func main() {
	roots := []struct {
		name, kind string
		typ        reflect.Type
	}{
		{"release-assurance-contract", releasepolicy.AssuranceContractKind, reflect.TypeFor[releasepolicy.AssuranceContract]()},
		{"release-assurance-attempt", releasepolicy.AssuranceRowKind, reflect.TypeFor[releasepolicy.AssuranceRow]()},
		{"release-assurance-ledger", releasepolicy.AssuranceLedgerKind, reflect.TypeFor[releasepolicy.AssuranceLedger]()},
		{"release-assured-decision", releasepolicy.AssuredDecisionKind, reflect.TypeFor[releasepolicy.AssuredDecision]()},
	}
	for _, root := range roots {
		g := generator{definitions: document{"jsonDigest": digest("json-v1"), "sourceDigest": digest("source-bytes")}}
		rootSchema := g.schema(root.typ)
		for _, tagged := range roots {
			name := "releasepolicy_" + tagged.typ.Name()
			if _, exists := g.definitions[name]; exists {
				properties := g.properties(name)
				properties["kind"], properties["version"] = document{"const": tagged.kind}, document{"const": releasepolicy.AssuranceVersion}
				if tagged.typ != reflect.TypeFor[releasepolicy.AssuredDecision]() {
					if _, exists := properties["collection_id"]; exists {
						properties["collection_id"] = document{"type": "string", "pattern": "^[0-9a-f]{64}$"}
					}
				}
			}
		}
		for _, name := range []string{"releasepolicy_AssuranceTask", "releasepolicy_AssuranceCheck", "releasepolicy_AssuranceInput"} {
			if _, exists := g.definitions[name]; exists {
				for _, field := range []string{"task_id", "requirement_id", "validation_key", "path"} {
					if _, exists := g.properties(name)[field]; exists {
						g.properties(name)[field] = document{"type": "string", "minLength": 1}
					}
				}
			}
		}
		if _, exists := g.definitions["releasepolicy_AssuranceContract"]; exists {
			p := g.properties("releasepolicy_AssuranceContract")
			p["nonce"] = document{"type": "string", "pattern": "^[0-9a-f]{64}$"}
			p["arms"] = arms(g.schema(reflect.TypeFor[releasepolicy.AssuranceArm]()), true)
			a := g.properties("releasepolicy_AssuranceArm")
			a["mode"] = document{"const": "authored_finite_output"}
			for _, field := range []string{"eval_source_digest", "executable_digest", "labels_digest", "review_digest"} {
				a[field] = document{"$ref": "#/$defs/sourceDigest"}
			}
			for _, field := range []string{"tasks", "checks", "inputs"} {
				a[field].(document)["type"], a[field].(document)["minItems"] = "array", 1
			}
			g.properties("releasepolicy_AssuranceInput")["source_digest"] = document{"$ref": "#/$defs/sourceDigest"}
			check := g.properties("models_RequirementCheck")
			check["scope"] = document{"enum": []string{"eval", "task"}}
			check["grader"], check["after_turn"] = document{"type": "string", "minLength": 1}, document{"const": 0}
		}
		if _, exists := g.definitions["releasepolicy_AssuranceRow"]; exists {
			g.properties("releasepolicy_AssuranceRow")["sequence"] = document{"type": "integer", "minimum": 1}
			for _, field := range []string{"trial_ordinal", "attempt_ordinal"} {
				g.properties("releasepolicy_AttemptKey")[field] = document{"type": "integer", "minimum": 1}
			}
			for _, field := range []string{"cluster_id", "task_id", "eval_id"} {
				g.properties("releasepolicy_AttemptKey")[field] = document{"type": "string", "minLength": 1}
			}
			for _, field := range []string{"run_number", "attempt_count"} {
				g.properties("models_EvidenceOrigin")[field] = document{"type": "integer", "minimum": 1}
			}
			g.definitions["releasepolicy_AssuranceOutput"] = document{"oneOf": []document{
				{"type": "object", "additionalProperties": false, "required": []string{"availability", "value"},
					"properties": document{"availability": document{"const": "available"}, "value": document{"type": "string"}}},
				{"type": "object", "additionalProperties": false, "required": []string{"availability", "reason"},
					"properties": document{"availability": document{"const": "unavailable"}, "reason": document{"type": "string", "pattern": "\\S"}}},
			}}
		}
		if _, exists := g.definitions["releasepolicy_AssuranceLedger"]; exists {
			p := g.properties("releasepolicy_AssuranceLedger")
			p["raw_result_digests"] = arms(document{"$ref": "#/$defs/sourceDigest"}, true)
			p["stream_digest"] = document{"$ref": "#/$defs/sourceDigest"}
			p["rows"].(document)["type"], p["rows"].(document)["minItems"] = "array", 1
		}
		if _, exists := g.definitions["releasepolicy_AssuredDecision"]; exists {
			p := g.properties("releasepolicy_AssuredDecision")
			p["assurance"] = document{"allOf": []document{
				g.schema(reflect.TypeFor[releasepolicy.Dimension]()),
				{"properties": document{"state": document{"enum": []string{"not_assessed", "passed", "failed", "invalid", "incomplete"}}}},
			}}
			p["regrade"] = document{"allOf": []document{
				g.schema(reflect.TypeFor[releasepolicy.Dimension]()),
				{"properties": document{"state": document{"enum": []string{"not_assessed", "passed", "invalid", "incomplete", "mismatched"}}}},
			}}
			p["reports"] = arms(g.schema(reflect.TypeFor[assurance.Report]()), false)
			p["base_decision"] = document{"allOf": []document{
				g.schema(reflect.TypeFor[releasepolicy.Decision]()),
				{"properties": document{"accepted": document{"const": false}}},
			}}
			// A fail-closed assessment can return before a contract is admitted.
			p["contract_digest"] = document{"oneOf": []document{
				{"$ref": "#/$defs/jsonDigest"},
				{"type": "object", "additionalProperties": false, "required": []string{"encoding", "sha256"},
					"properties": document{"encoding": document{"const": ""}, "sha256": document{"const": ""}}},
			}}
			p["collection_id"] = document{"type": "string", "pattern": "^([0-9a-f]{64})?$"}
			passingReport := document{"allOf": []document{
				g.schema(reflect.TypeFor[assurance.Report]()),
				{"properties": document{
					"state": document{"const": "passed"},
					"review": document{"properties": document{
						"current_source_accepted": document{"const": true},
						"eligible":                document{"const": true},
						"declared_state":          document{"const": "reviewed"},
					}},
				}},
			}}
			conditions := document{}
			for field, states := range map[string][]string{
				"compatibility": {"compatible"}, "completeness": {"complete"}, "operations": {"observed"},
				"golden": {"passed", "not_required"}, "billing": {"within_budget", "not_required"},
				"statistics": {"noninferiority", "improvement"},
			} {
				conditions[field] = document{"properties": document{"state": document{"enum": states}}}
			}
			g.definitions["releasepolicy_AssuredDecision"].(document)["allOf"] = []document{
				{"if": document{"properties": document{"accepted": document{"const": true}}},
					"then": document{"properties": document{
						"contract_digest": document{"$ref": "#/$defs/jsonDigest"},
						"collection_id":   document{"type": "string", "pattern": "^[0-9a-f]{64}$"},
						"reports":         arms(passingReport, true),
						"assurance":       document{"properties": document{"state": document{"const": "passed"}}},
						"regrade":         document{"properties": document{"state": document{"const": "passed"}}},
						"base_decision":   document{"properties": conditions},
					}}},
			}
		}
		rootSchema["$schema"] = "https://json-schema.org/draft/2020-12/schema"
		rootSchema["$id"] = "https://raw.githubusercontent.com/microsoft/waza/main/schemas/" + root.name + "-1.0.schema.json"
		rootSchema["title"] = root.kind + " 1.0 (independent offline profile)"
		rootSchema["description"] = "Structural admission only. Strict readers additionally verify original JSON tokens, self-digest omission, exact source bytes, ordered replay, source/review freshness, deterministic regrading and base non-assurance conditions. Hashes are local consistency, not authentication."
		rootSchema["$defs"] = g.definitions
		data, err := json.MarshalIndent(rootSchema, "", "  ")
		if err != nil {
			panic(err)
		}
		if err := os.WriteFile(filepath.Join("schemas", root.name+"-1.0.schema.json"), append(data, '\n'), 0o644); err != nil {
			panic(err)
		}
	}
}
