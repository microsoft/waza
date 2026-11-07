package manifestaggregate

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type compiledProfiles struct {
	report  *jsonschema.Schema
	request *jsonschema.Schema
}

var profiles = sync.OnceValues(func() (compiledProfiles, error) {
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaloader.Offline{})
	for name, text := range map[string]string{
		"execution-evidence-1.0.schema.json": schemas.ExecutionEvidenceSchemaJSON,
		"execution-attempt-1.0.schema.json":  schemas.ExecutionAttemptSchemaJSON,
		"artifact-manifest-1.0.schema.json":  schemas.ArtifactManifestSchemaJSON,
		"runner-terminal-1.0.schema.json":    schemas.RunnerTerminalSchemaJSON,
	} {
		document, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
		if err != nil {
			return compiledProfiles{}, err
		}
		for _, root := range []string{"https://microsoft.github.io/waza/schemas/", "https://raw.githubusercontent.com/microsoft/waza/main/schemas/"} {
			if err := compiler.AddResource(root+name, document); err != nil {
				return compiledProfiles{}, err
			}
		}
	}
	document, err := jsonschema.UnmarshalJSON(strings.NewReader(schemas.ManifestAggregateSchemaJSON))
	if err != nil {
		return compiledProfiles{}, err
	}
	const resource = "https://microsoft.github.io/waza/schemas/manifest-aggregate-1.0.schema.json"
	if err := compiler.AddResource(resource, document); err != nil {
		return compiledProfiles{}, err
	}
	report, err := compiler.Compile(resource)
	if err != nil {
		return compiledProfiles{}, err
	}
	request, err := compiler.Compile(resource + "#/$defs/request")
	return compiledProfiles{report: report, request: request}, err
})

func ParseRequest(data []byte) (Request, error) {
	var request Request
	if len(data) > MaxRequestBytes {
		return request, fmt.Errorf("%w: request exceeds 32 KiB", ErrInvalid)
	}
	value, err := jsonutil.Parse(data)
	if err != nil {
		return request, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	profile, err := profiles()
	if err != nil {
		return request, err
	}
	if err := profile.request.Validate(value); err != nil {
		return request, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if err := json.Unmarshal(data, &request); err != nil {
		return request, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return request, request.Validate()
}

func validateReport(data []byte) error {
	value, err := jsonutil.Parse(data)
	if err != nil {
		return err
	}
	profile, err := profiles()
	if err != nil {
		return err
	}
	if err := profile.report.Validate(value); err != nil {
		return err
	}
	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		return err
	}
	counts := map[string]int{}
	members := map[string]Member{}
	previous := ""
	for _, member := range report.Members {
		if member.ManifestSHA256 <= previous {
			return fmt.Errorf("%w: aggregate membership must be sorted and unique", ErrInvalid)
		}
		previous = member.ManifestSHA256
		counts[member.State]++
		members[member.ManifestSHA256] = member
		if member.Outcome != "" && member.State != "conflicting" && member.State != string(member.Outcome) {
			return fmt.Errorf("%w: member state contradicts recorded outcome", ErrInvalid)
		}
		if member.Verdicts != nil {
			lastID := ""
			for _, verdict := range *member.Verdicts {
				if verdict.Identifier <= lastID {
					return fmt.Errorf("%w: verdicts must be sorted and unique", ErrInvalid)
				}
				lastID = verdict.Identifier
			}
		}
	}
	if !maps.Equal(counts, report.StateCounts) {
		return fmt.Errorf("%w: state counts contradict membership", ErrInvalid)
	}
	lastConflict := ""
	for _, conflict := range report.Conflicts {
		key := conflict.Logical.Run + "\x00" + conflict.Logical.Work
		if key <= lastConflict {
			return fmt.Errorf("%w: conflicts must be sorted and unique", ErrInvalid)
		}
		lastConflict = key
		lastDigest := ""
		for _, digest := range conflict.Manifests {
			member, ok := members[digest]
			if !ok || member.State != "conflicting" || member.Logical == nil || *member.Logical != conflict.Logical || digest <= lastDigest {
				return fmt.Errorf("%w: conflict membership is inconsistent", ErrInvalid)
			}
			lastDigest = digest
		}
	}
	if report.Evidence != nil {
		if err := validateEvidenceReport(report); err != nil {
			return err
		}
	}
	return nil
}

func validateEvidenceReport(report Report) error {
	evidence := report.Evidence
	if len(evidence.Members) != len(report.Members) {
		return fmt.Errorf("%w: evidence membership differs from the expected manifest set", ErrInvalid)
	}
	counts := map[evidencestate.Category]map[evidencestate.State]int{}
	admittedCounts := map[evidencestate.Category]map[evidencestate.State]int{}
	candidates := 0
	for index, member := range evidence.Members {
		if member.ManifestSHA256 != report.Members[index].ManifestSHA256 {
			return fmt.Errorf("%w: evidence origins must match sorted expected membership", ErrInvalid)
		}
		expectedState := "not_selected"
		if len(member.Candidates) == 1 {
			expectedState = member.Candidates[0].State
		} else if len(member.Candidates) > 1 {
			expectedState = "conflicting"
		}
		if member.State != expectedState {
			return fmt.Errorf("%w: evidence state contradicts its candidates", ErrInvalid)
		}
		previous := ""
		for _, candidate := range member.Candidates {
			candidates++
			if candidate.EvidenceSHA256 <= previous {
				return fmt.Errorf("%w: evidence candidates must be sorted and unique", ErrInvalid)
			}
			previous = candidate.EvidenceSHA256
			if source := candidate.SourceContext; source != nil {
				if candidate.State != "verified" ||
					(source.Source != nil && source.Source.ManifestDigest != candidate.OriginManifest) ||
					(source.Source != nil && source.State != "verified" && report.Completion != "incomplete") ||
					(source.State == "verified" && (source.Source == nil || source.Verification != "content-and-declared-context-only")) ||
					(source.State != "verified" && source.Verification != "unverified") {
					return fmt.Errorf("%w: original source context contradicts admitted evidence", ErrInvalid)
				}
				if parent := source.ParentContext; parent != nil {
					if source.State != "verified" || source.Source == nil ||
						(parent.Relationship != "retry" && parent.Relationship != "branch") ||
						(parent.Relationship == "branch" && parent.State != "unavailable") ||
						(parent.State == "verified" && (parent.Relationship != "retry" || parent.Source == nil ||
							parent.Verification != "content-and-declared-context-only" ||
							parent.Source.ExecutionID == source.Source.ExecutionID ||
							parent.Source.ManifestDigest == source.Source.ManifestDigest)) ||
						(parent.State != "verified" && (parent.Source != nil || parent.Verification != "unverified")) ||
						(parent.Relationship == "retry" && parent.State != "verified" && report.Completion != "incomplete") {
						return fmt.Errorf("%w: immediate parent context contradicts admitted child", ErrInvalid)
					}
				}
			}
			if candidate.State != "verified" {
				continue
			}
			if candidate.OriginManifest != member.ManifestSHA256 || !slices.Contains(candidate.Sources, member.ManifestSHA256) {
				return fmt.Errorf("%w: verified evidence origin/source membership conflicts", ErrInvalid)
			}
			for sourceIndex, source := range candidate.Sources {
				if sourceIndex > 0 && source <= candidate.Sources[sourceIndex-1] {
					return fmt.Errorf("%w: evidence source manifests must be sorted and unique", ErrInvalid)
				}
			}
			previousCategory := ""
			for _, declaration := range candidate.Declarations {
				if string(declaration.Category) <= previousCategory {
					return fmt.Errorf("%w: evidence categories must be sorted and unique", ErrInvalid)
				}
				previousCategory = string(declaration.Category)
				values := []*string{declaration.ObservedCount}
				if declaration.Limit != nil {
					values = append(values, &declaration.Limit.Observed, declaration.Limit.Total)
				}
				for _, value := range values {
					if value != nil {
						n, err := strconv.ParseInt(*value, 10, 64)
						if err != nil || n < 0 || strconv.FormatInt(n, 10) != *value {
							return fmt.Errorf("%w: declared integer metadata must be canonical nonnegative int64 text", ErrInvalid)
						}
					}
				}
				if member.State == "verified" {
					if counts[declaration.Category] == nil {
						counts[declaration.Category] = map[evidencestate.State]int{}
					}
					counts[declaration.Category][declaration.DeclaredState]++
				}
			}
			if candidate.Usage == nil {
				return fmt.Errorf("%w: verified evidence must retain explicit usage availability", ErrInvalid)
			}
			usage := candidate.Usage
			for categoryIndex, category := range candidate.Categories {
				if categoryIndex >= len(candidate.Declarations) ||
					category.Category != candidate.Declarations[categoryIndex].Category ||
					category.DeclaredState != candidate.Declarations[categoryIndex].DeclaredState {
					return fmt.Errorf("%w: admitted categories contradict original declarations", ErrInvalid)
				}
				if category.State == evidencestate.Present && category.ObservedCount == nil &&
					(category.Verification == "source-content-and-recorded-category-shape-only" ||
						category.Verification == "source-content-and-recorded-usage-shape-only") {
					return fmt.Errorf("%w: present native categories require observed counts", ErrInvalid)
				}
				if category.Verification == "recorded-degradation-only" &&
					(category.State != evidencestate.Incomplete || category.ObservedCount != nil) {
					return fmt.Errorf("%w: recorded degradation must remain incomplete without an admitted count", ErrInvalid)
				}
				if category.State == evidencestate.Present && category.Verification != "source-content-and-recorded-category-shape-only" &&
					category.Verification != "source-content-and-recorded-usage-shape-only" &&
					(category.Category != evidencestate.Usage || category.Verification != "declared-state-only" ||
						category.Reason != "declared_present_empty" || category.ObservedCount != nil) {
					return fmt.Errorf("%w: present categories require native counts or declared present-empty usage", ErrInvalid)
				}
				if category.ObservedCount != nil {
					n, err := strconv.ParseInt(*category.ObservedCount, 10, 64)
					if err != nil || n < 0 || strconv.FormatInt(n, 10) != *category.ObservedCount ||
						!categoryAdmitted(category) {
						return fmt.Errorf("%w: observed counts require canonical native category admission", ErrInvalid)
					}
				}
				if category.Category == evidencestate.Usage &&
					(category.State != usage.State || category.DeclaredState != usage.DeclaredState ||
						category.Verification != usage.Verification || category.Reason != usage.Reason) {
					return fmt.Errorf("%w: usage view contradicts category admission", ErrInvalid)
				}
				if member.State == "verified" && categoryAdmitted(category) {
					if admittedCounts[category.Category] == nil {
						admittedCounts[category.Category] = map[evidencestate.State]int{}
					}
					admittedCounts[category.Category][category.State]++
				}
			}
			if usage.Recorded != nil {
				if usage.Verification != "source-content-and-recorded-usage-shape-only" ||
					usage.DeclaredState != evidencestate.Present ||
					(usage.State != evidencestate.Present && usage.State != evidencestate.Incomplete) {
					return fmt.Errorf("%w: recorded usage requires explicit native shape admission", ErrInvalid)
				}
				if usage.State == evidencestate.Present {
					if usage.Recorded.AICredits == nil || len(usage.Recorded.ModelMetrics) == 0 {
						return fmt.Errorf("%w: present recorded usage requires complete credit attribution", ErrInvalid)
					}
					for _, model := range usage.Recorded.ModelMetrics {
						if model.AICredits == nil {
							return fmt.Errorf("%w: present recorded usage requires per-model credits", ErrInvalid)
						}
					}
				}
			} else if usage.State == evidencestate.Present &&
				(usage.Reason != "declared_present_empty" || usage.Verification != "declared-state-only") {
				return fmt.Errorf("%w: present usage without native content must remain declared present-empty", ErrInvalid)
			}
		}
	}
	if candidates > MaxInputs || len(counts) != len(evidence.DeclarationStateCounts) {
		return fmt.Errorf("%w: evidence candidate/count bounds are inconsistent", ErrInvalid)
	}
	for category, states := range counts {
		if !maps.Equal(states, evidence.DeclarationStateCounts[category]) {
			return fmt.Errorf("%w: declaration counts contradict admitted metadata", ErrInvalid)
		}
	}
	if len(admittedCounts) != len(evidence.CategoryStateCounts) {
		return fmt.Errorf("%w: native category counts contradict admitted metadata", ErrInvalid)
	}
	for category, states := range admittedCounts {
		if !maps.Equal(states, evidence.CategoryStateCounts[category]) {
			return fmt.Errorf("%w: native category counts contradict admitted metadata", ErrInvalid)
		}
	}
	return nil
}
