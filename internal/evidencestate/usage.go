package evidencestate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// UsageEvidence separates saved-usage shape/completeness from declarations.
// Even validated recorded usage does not attest that SDK execution occurred.
type UsageEvidence struct {
	State         State
	DeclaredState State
	Verification  string
	Reason        string
	Usage         *models.UsageStats
}

var usageSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	const text = `{
		"type":"object","additionalProperties":false,
		"required":["turns","input_tokens","output_tokens","cache_read_tokens","cache_write_tokens","premium_requests"],
		"properties":{
			"turns":{"$ref":"#/$defs/count"},"input_tokens":{"$ref":"#/$defs/count"},
			"output_tokens":{"$ref":"#/$defs/count"},"cache_read_tokens":{"$ref":"#/$defs/count"},
			"cache_write_tokens":{"$ref":"#/$defs/count"},"premium_requests":{"$ref":"#/$defs/number"},
			"ai_credits":{"type":["number","null"],"minimum":0},
			"provider":{"type":"string"},"provider_host":{"type":"string"},
			"model_metrics":{"type":"object","additionalProperties":{
				"type":"object","additionalProperties":false,
				"required":["input_tokens","output_tokens","cache_read_tokens","cache_write_tokens","request_count","request_cost"],
				"properties":{
					"input_tokens":{"$ref":"#/$defs/count"},"output_tokens":{"$ref":"#/$defs/count"},
					"cache_read_tokens":{"$ref":"#/$defs/count"},"cache_write_tokens":{"$ref":"#/$defs/count"},
					"request_count":{"$ref":"#/$defs/number"},"request_cost":{"$ref":"#/$defs/number"},
					"ai_credits":{"type":["number","null"],"minimum":0}
				}
			}}
		},
		"$defs":{"count":{"type":"integer","minimum":0},"number":{"type":"number","minimum":0}}
	}`
	c := jsonschema.NewCompiler()
	c.UseLoader(schemaloader.Offline{})
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
	if err != nil {
		return nil, err
	}
	if err := c.AddResource("recorded-usage.json", doc); err != nil {
		return nil, err
	}
	return c.Compile("recorded-usage.json")
})

// ReadUsage admits only one explicitly referenced, non-terminal application/json
// artifact containing actual models.UsageStats wire fields. Artifact names are
// caller-assigned aliases; neither a filename nor a terminal implies usage.
// Aggregators must require State == Present and non-nil Usage. Incomplete
// recorded data remains available here for review, never an authoritative total.
func ReadUsage(ctx context.Context, backend artifactmanifest.Backend, evidenceDigest string) (UsageEvidence, error) {
	_, usage, err := InspectWithUsage(ctx, backend, evidenceDigest)
	return usage, err
}

// InspectWithUsage combines declaration inspection and recorded usage admission
// in one source pass. Usage errors do not rewrite the separate byte-integrity
// receipt or its declarations; consumers must inspect both results.
func InspectWithUsage(ctx context.Context, backend artifactmanifest.Backend, evidenceDigest string) (Inspection, UsageEvidence, error) {
	result := UsageEvidence{State: Unavailable, Verification: "unverified", Reason: "usage_not_verified"}
	inspection, source, err := inspectEvidence(ctx, backend, evidenceDigest)
	if err != nil {
		return inspection, result, err
	}
	result, err = admitUsage(inspection.Document, source)
	return inspection, result, err
}

func admitUsage(d *Document, source *artifactmanifest.CompletedPublication) (UsageEvidence, error) {
	result := UsageEvidence{State: Unavailable, Verification: "unverified", Reason: "usage_not_verified"}
	for _, record := range d.Categories {
		if record.Category != Usage {
			continue
		}
		result.State, result.DeclaredState, result.Verification, result.Reason = record.State, record.State, "declared-state-only", string(record.Reason)
		if record.State != Present {
			return result, nil
		}
		if *record.ObservedCount == 0 {
			result.Reason = "declared_present_empty"
			return result, nil
		}
		result.State, result.Reason = Unavailable, "usage_not_verified"
		if *record.ObservedCount != 1 || len(record.Artifacts) != 1 {
			return result, fmt.Errorf("%w: usage requires exactly one recorded UsageStats artifact", ErrInvalid)
		}
		origin := &source.Manifest
		name := record.Artifacts[0]
		if name == origin.TerminalArtifact {
			return result, fmt.Errorf("%w: terminal envelopes do not contain UsageStats evidence", ErrInvalid)
		}
		for _, a := range origin.Artifacts {
			if a.Name != name {
				continue
			}
			if a.MediaType != "application/json" {
				return result, fmt.Errorf("%w: recorded usage requires application/json", ErrInvalid)
			}
			data := source.ArtifactBytes[name]
			value, err := jsonutil.Parse(data)
			if err != nil {
				return result, fmt.Errorf("%w: recorded usage JSON: %w", ErrInvalid, err)
			}
			schema, err := usageSchema()
			if err != nil {
				return result, err
			}
			if err := schema.Validate(value); err != nil {
				return result, fmt.Errorf("%w: recorded usage fields: %w", ErrInvalid, err)
			}
			var usage models.UsageStats
			if err := json.Unmarshal(data, &usage); err != nil {
				return result, fmt.Errorf("%w: decoding recorded usage: %w", ErrInvalid, err)
			}
			for model := range usage.ModelMetrics {
				if strings.TrimSpace(model) == "" {
					return result, fmt.Errorf("%w: recorded usage model attribution label is empty", ErrInvalid)
				}
			}
			if usage.ProviderHost != "" {
				host, err := url.Parse("https://" + usage.ProviderHost)
				if err != nil || host.Hostname() == "" || host.Host != usage.ProviderHost ||
					host.User != nil || host.Path != "" || host.RawQuery != "" || host.Fragment != "" {
					return result, fmt.Errorf("%w: recorded usage provider_host must omit credentials, paths, queries and fragments", ErrInvalid)
				}
			}
			result.State, result.Usage, result.Verification, result.Reason = Present, &usage, "source-content-and-recorded-usage-shape-only", ""
			if usage.AICredits == nil || len(usage.ModelMetrics) == 0 {
				result.State, result.Reason = Incomplete, "incomplete_credit_or_model_attribution"
			}
			for _, metrics := range usage.ModelMetrics {
				if metrics.AICredits == nil {
					result.State, result.Reason = Incomplete, "incomplete_credit_or_model_attribution"
				}
			}
			return result, nil
		}
		return result, artifactmanifest.ErrConflict
	}
	return result, fmt.Errorf("%w: usage category is missing", ErrInvalid)
}
