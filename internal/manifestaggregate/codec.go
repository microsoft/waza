package manifestaggregate

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
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
	executionManifests := map[string]string{}
	manifestOutcomes, manifestProfiles := map[string]string{}, map[string]string{}
	profiles := map[string][]Input{}
	state := "complete"
	allUsageEligible := true
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
			outcome := string(member.Outcome)
			if previous, ok := manifestOutcomes[member.Manifest]; ok && previous != outcome {
				return fail()
			}
			manifestOutcomes[member.Manifest] = outcome
			counts.MemberOutcomes[outcome]++
		}
		if member.Binding != nil {
			if !slices.IsSorted(member.Binding.GraderIDs) {
				return fail()
			}
			manifests[member.Manifest] = true
			wire, err := json.Marshal(member.Binding)
			if err != nil {
				return err
			}
			key := artifactmanifest.Hash(wire)
			// A content-addressed manifest has exactly one binding across its views.
			if previous, ok := manifestProfiles[member.Manifest]; ok && previous != key {
				return fail()
			}
			manifestProfiles[member.Manifest] = key
			profiles[key] = append(profiles[key], member.Input)
		}
		if member.Execution != "" {
			if manifest, ok := executionManifests[member.Execution]; ok && manifest != member.Manifest {
				return fail()
			}
			executionManifests[member.Execution] = member.Manifest
			executions[member.Execution] = true
		}
		allUsageEligible = allUsageEligible && memberUsageEligible(member)
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
	if report.Usage.State == "complete" && (len(executions) == 0 || state != "complete" || !allUsageEligible) {
		return fail()
	}
	return nil
}

func memberUsageEligible(member Member) bool {
	if member.Execution == "" || slices.Contains(member.Diagnostics, "cached_historical_usage_excluded") {
		return false
	}
	present := false
	for _, category := range member.Categories {
		if category.Source == evidencestate.Cached {
			return false
		}
		if category.Category == evidencestate.Usage {
			present = category.State == evidencestate.Present && category.DeclaredState == evidencestate.Present &&
				category.Verification == "source-content-and-recorded-usage-shape-only" &&
				category.ObservedCount != nil && *category.ObservedCount == 1
		}
	}
	return present
}

// Encode emits typed field order, lexical reference/profile/code order inherited
// from Build, sorted JSON map keys, and no newline or self-referential digest.
func Encode(report Report) ([]byte, error) {
	for _, member := range report.Members {
		if member.Binding != nil {
			for _, text := range append(slices.Clone(member.Binding.GraderIDs), member.Binding.Selection.Shard, member.Binding.Selection.Task) {
				if !utf8.ValidString(text) {
					return nil, fmt.Errorf("%w: binding is not UTF-8", ErrInvalid)
				}
			}
		}
	}
	if usage := report.Usage.Recorded; usage != nil {
		if !utf8.ValidString(usage.Provider) || !utf8.ValidString(usage.ProviderHost) {
			return nil, fmt.Errorf("%w: usage metadata is not UTF-8", ErrInvalid)
		}
		for model := range usage.ModelMetrics {
			if !utf8.ValidString(model) {
				return nil, fmt.Errorf("%w: usage model is not UTF-8", ErrInvalid)
			}
		}
	}
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
