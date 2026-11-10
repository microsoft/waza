package manifestaggregate

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

var reportSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaloader.Offline{})
	for name, text := range map[string]string{
		"manifest-aggregate-1.0.schema.json": schemas.ManifestAggregateSchemaJSON,
		"artifact-manifest-1.0.schema.json":  schemas.ArtifactManifestSchemaJSON,
		"runner-terminal-1.0.schema.json":    schemas.RunnerTerminalSchemaJSON,
	} {
		document, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
		if err != nil {
			return nil, err
		}
		if err := compiler.AddResource("https://microsoft.github.io/waza/schemas/"+name, document); err != nil {
			return nil, err
		}
	}
	return compiler.Compile("https://microsoft.github.io/waza/schemas/manifest-aggregate-1.0.schema.json")
})

// Parse admits the report's wire profile and internal population accounting.
// It does not reverify sources or turn a saved report into current evidence.
func Parse(data []byte) (*Report, error) {
	if len(data) > MaxReportBytes {
		return nil, fmt.Errorf("%w: report exceeds byte limit", ErrInvalid)
	}
	value, err := jsonutil.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%w: report JSON: %w", ErrInvalid, err)
	}
	schema, err := reportSchema()
	if err != nil {
		return nil, err
	}
	if err := schema.Validate(value); err != nil {
		return nil, fmt.Errorf("%w: report schema: %w", ErrInvalid, err)
	}
	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, err
	}
	if err := accounting(report); err != nil {
		return nil, err
	}
	return &report, nil
}

func accounting(report Report) error {
	fail := func() error { return fmt.Errorf("%w: inconsistent population accounting or ordering", ErrInvalid) }
	counts := Counts{Members: len(report.Members), MemberStates: map[string]int{}, MemberOutcomes: map[string]int{}}
	manifests, executions := map[string]bool{}, map[string]bool{}
	profiles := map[string][]Input{}
	state := "complete"
	for index, member := range report.Members {
		if index > 0 && compareInput(report.Members[index-1].Input, member.Input) >= 0 {
			return fail()
		}
		if !slices.IsSorted(member.Diagnostics) {
			return fail()
		}
		for i, category := range member.Categories {
			if i > 0 && member.Categories[i-1].Category >= category.Category {
				return fail()
			}
		}
		counts.MemberStates[member.State]++
		if member.Outcome != "" {
			counts.MemberOutcomes[string(member.Outcome)]++
		}
		if member.Binding != nil {
			manifests[member.Manifest] = true
			wire, err := json.Marshal(member.Binding)
			if err != nil {
				return err
			}
			key := artifactmanifest.Hash(wire)
			profiles[key] = append(profiles[key], member.Input)
		}
		if member.Execution != "" {
			executions[member.Execution] = true
		}
		if member.State == "conflicting" {
			state = "conflicting"
		} else if member.State != "admitted" && state != "conflicting" {
			state = "incomplete"
		}
	}
	counts.VerifiedManifests, counts.VerifiedExecutions = len(manifests), len(executions)
	expectedGroups := []Group{}
	for _, key := range sortedKeys(profiles) {
		expectedGroups = append(expectedGroups, Group{Profile: key, Members: profiles[key]})
	}
	if !reflect.DeepEqual(counts, report.Counts) || !reflect.DeepEqual(expectedGroups, report.Groups) ||
		state != report.State || report.Usage.Executions != len(executions) || !slices.IsSorted(report.Usage.Diagnostics) {
		return fail()
	}
	if report.Usage.State == "complete" && (len(executions) == 0 || state != "complete") {
		return fail()
	}
	return nil
}

// Encode emits typed field order, lexical reference/profile/code order inherited
// from Build, sorted JSON map keys, and no newline or self-referential digest.
func Encode(report Report) ([]byte, error) {
	data, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	if _, err := Parse(data); err != nil {
		return nil, err
	}
	return data, nil
}

// ParseInputs rejects duplicate keys, unknown fields and explicit null/empty
// evidence references before typed decoding can erase their presence.
func ParseInputs(data []byte) ([]Input, error) {
	if len(data) > artifactmanifest.MaxManifestBytes {
		return nil, fmt.Errorf("%w: input set exceeds byte limit", ErrInvalid)
	}
	value, err := jsonutil.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%w: input JSON: %w", ErrInvalid, err)
	}
	array, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%w: expected reference array", ErrInvalid)
	}
	for _, value := range array {
		object, ok := value.(map[string]any)
		if !ok || len(object) < 1 || len(object) > 2 {
			return nil, ErrInvalid
		}
		for key, field := range object {
			text, ok := field.(string)
			if !ok || text == "" || (key != "manifest_sha256" && key != "evidence_sha256") {
				return nil, ErrInvalid
			}
		}
		if _, ok := object["manifest_sha256"]; !ok {
			return nil, ErrInvalid
		}
	}
	var inputs []Input
	if err := json.Unmarshal(data, &inputs); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return Normalize(inputs)
}
