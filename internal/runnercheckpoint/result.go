package runnercheckpoint

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executioncheckpoint"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const MaxResultBytes = runnerunit.MaxSubmissionResultBytes + executioncheckpoint.MaxBytes + 65536

var resultSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaloader.Offline{})
	for name, text := range map[string]string{
		"runner-checkpoint-result-1.0.schema.json":    schemas.RunnerCheckpointResultSchemaJSON,
		"execution-checkpoint-1.0.schema.json":        schemas.ExecutionCheckpointSchemaJSON,
		"execution-submission-result-1.0.schema.json": schemas.ExecutionSubmissionResultSchemaJSON,
		"execution-attempt-1.0.schema.json":           schemas.ExecutionAttemptSchemaJSON,
		"execution-completion-1.0.schema.json":        schemas.ExecutionCompletionSchemaJSON,
		"artifact-manifest-1.0.schema.json":           schemas.ArtifactManifestSchemaJSON,
		"runner-terminal-1.0.schema.json":             schemas.RunnerTerminalSchemaJSON,
		"runner-unit-1.0.schema.json":                 schemas.RunnerUnitSchemaJSON,
		"runner-negotiation-response-1.0.schema.json": schemas.RunnerNegotiationResponseSchemaJSON,
	} {
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
		if err != nil {
			return nil, fmt.Errorf("reading checkpoint result schema: %w", err)
		}
		if err := compiler.AddResource("https://microsoft.github.io/waza/schemas/"+name, doc); err != nil {
			return nil, fmt.Errorf("registering checkpoint result schema: %w", err)
		}
	}
	return compiler.Compile("https://microsoft.github.io/waza/schemas/runner-checkpoint-result-1.0.schema.json")
})

// ParseResult admits strict wire declarations and cross-field context, not
// source verification, recovery authority, or permission to disclose a report.
func ParseResult(data []byte) (*Result, error) {
	if len(data) > MaxResultBytes {
		return nil, fmt.Errorf("checkpoint result exceeds %d bytes", MaxResultBytes)
	}
	doc, err := jsonutil.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parsing checkpoint result: %w", err)
	}
	schema, err := resultSchema()
	if err != nil {
		return nil, err
	}
	if err := schema.Validate(doc); err != nil {
		return nil, fmt.Errorf("checkpoint result schema: %w", err)
	}
	var result Result
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decoding checkpoint result: %w", err)
	}
	if err := validateResult(result); err != nil {
		return nil, err
	}
	return &result, nil
}

// EncodeResult validates declarations only. The public transport must separately
// apply the shared capture/privacy policy before calling this codec.
func EncodeResult(result Result) ([]byte, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encoding checkpoint result: %w", err)
	}
	if _, err := ParseResult(data); err != nil {
		return nil, err
	}
	return data, nil
}

func validateResult(result Result) error {
	if result.BeforeDigest != "" && result.BeforeDigest == result.FinalizedDigest {
		return fmt.Errorf("different checkpoint boundaries cannot share one canonical blob digest")
	}
	if result.Source != nil {
		if _, err := executioncheckpoint.Encode(*result.Source); err != nil {
			return err
		}
	}
	if result.Submission == nil {
		if result.Status != "rejected" {
			return fmt.Errorf("checkpoint operation status requires an actual submission declaration")
		}
		return nil
	}
	submission := result.Submission
	if _, err := runnerunit.EncodeSubmissionResult(*submission); err != nil {
		return err
	}
	if result.Status == "checkpoint_error" {
		if submission.Status != "completed" {
			return fmt.Errorf("checkpoint publication failure must preserve its actual completed submission")
		}
	} else if result.Status != submission.Status || result.ExitCode != submission.ExitCode {
		return fmt.Errorf("checkpoint invocation status or exit contradicts its actual submission")
	}
	if result.Source != nil && result.Source.Boundary == executioncheckpoint.BeforeUnit && submission.Reservation != nil {
		if submission.Reservation.Parent == nil ||
			*submission.Reservation.Parent != (executionattempt.Parent{
				ExecutionID: result.Source.ExecutionID, Relationship: "retry",
			}) || submission.Reservation.ExecutionID == result.Source.ExecutionID {
			return fmt.Errorf("before restore does not retain its exact original parent and fresh child")
		}
	}
	if result.Status == "completed" && result.Source != nil && result.Source.Boundary != executioncheckpoint.BeforeUnit {
		return fmt.Errorf("a finalized source must be reused, not rerun")
	}
	if result.Status != "reused" {
		return nil
	}
	source := result.Source
	reservation, err := executionattempt.EncodeAttempt(*submission.Reservation)
	if err != nil {
		return err
	}
	completion, err := executionledger.EncodeCompletion(*submission.Completion)
	if err != nil {
		return err
	}
	if submission.Reservation.ExecutionID != source.ExecutionID ||
		artifactmanifest.Hash(reservation) != source.ReservationDigest ||
		artifactmanifest.Hash(completion) != source.CompletionDigest ||
		submission.Completion.ManifestDigest != source.ManifestDigest {
		return fmt.Errorf("finalized restore differs from its exact original checkpoint source")
	}
	return nil
}
