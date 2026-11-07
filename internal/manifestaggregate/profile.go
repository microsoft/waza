package manifestaggregate

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"sync"

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
	if len(data) > 16384 {
		return request, fmt.Errorf("%w: request exceeds 16 KiB", ErrInvalid)
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
	return nil
}
