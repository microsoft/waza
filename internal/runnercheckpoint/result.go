package runnercheckpoint

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

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

var resultSchemas = sync.OnceValues(func() (map[string]*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaloader.Offline{})
	for name, text := range map[string]string{
		"runner-checkpoint-result-1.0.schema.json":    schemas.RunnerCheckpointResultSchemaJSON,
		"runner-checkpoint-result-1.1.schema.json":    schemas.RunnerCheckpointResultV11SchemaJSON,
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
	result := make(map[string]*jsonschema.Schema, 2)
	for version, name := range map[string]string{
		"1.0": "runner-checkpoint-result-1.0.schema.json",
		"1.1": "runner-checkpoint-result-1.1.schema.json",
	} {
		schema, err := compiler.Compile("https://microsoft.github.io/waza/schemas/" + name)
		if err != nil {
			return nil, err
		}
		result[version] = schema
	}
	return result, nil
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
	var header struct {
		SchemaVersion string `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, fmt.Errorf("decoding checkpoint result version: %w", err)
	}
	schemasByVersion, err := resultSchemas()
	if err != nil {
		return nil, err
	}
	schema := schemasByVersion[header.SchemaVersion]
	if schema == nil {
		return nil, fmt.Errorf("unsupported checkpoint result schema version %q", header.SchemaVersion)
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
	for _, diagnostic := range result.Diagnostics {
		if !utf8.ValidString(diagnostic.Code) || !utf8.ValidString(diagnostic.Message) {
			return nil, fmt.Errorf("checkpoint result diagnostic is not UTF-8")
		}
	}
	if result.Submission != nil {
		if _, err := runnerunit.EncodeSubmissionResult(*result.Submission); err != nil {
			return nil, err
		}
	}
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
	if result.SchemaVersion == "1.1" {
		return validateOperationError(result)
	}
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
	if result.Source != nil && submission.Reservation != nil &&
		result.Source.Compatibility.Work != submission.Reservation.Logical.Work {
		return fmt.Errorf("checkpoint source work contradicts the actual submission reservation")
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

func validateOperationError(result Result) error {
	if result.Status != "operation_error" || result.ExitCode != 2 || result.ErrorCode == "" {
		return fmt.Errorf("operation error result requires operation_error status, exit 2, and a stable error code")
	}
	if result.BeforeDigest != "" && result.BeforeDigest == result.FinalizedDigest {
		return fmt.Errorf("different checkpoint boundaries cannot share one canonical blob digest")
	}
	if result.CompatibilityResult != "unavailable" || len(result.CompatibilityDifferences) != 0 ||
		result.RestoredUnits != nil || result.RemainingWork != nil || len(result.Diagnostics) != 0 {
		return fmt.Errorf("operation error result cannot claim checkpoint progress or include diagnostics")
	}
	if result.ErrorCode != ErrorCodeStorageCloseFailed &&
		(result.Submission != nil || result.Source != nil || result.SourceWithheld ||
			result.BeforeDigest != "" || result.FinalizedDigest != "") {
		return fmt.Errorf("pre-run operation error cannot include submission, source, or checkpoint references")
	}
	if result.SourceWithheld && result.Source != nil {
		return fmt.Errorf("withheld checkpoint source cannot be included")
	}
	if result.Submission != nil {
		if result.Submission.Report != nil {
			return fmt.Errorf("operation error result cannot include an unapproved submission report")
		}
		for _, diagnostic := range result.Submission.Diagnostics {
			if diagnostic.Message != CapturedDiagnosticMessage {
				return fmt.Errorf("operation error result contains an unprojected submission diagnostic")
			}
		}
		if _, err := runnerunit.EncodeSubmissionResult(*result.Submission); err != nil {
			return err
		}
	}
	if result.Source != nil {
		if result.Submission == nil {
			return fmt.Errorf("checkpoint source requires its admitted submission")
		}
		if _, err := executioncheckpoint.Encode(*result.Source); err != nil {
			return err
		}
		submission := result.Submission
		if submission.Reservation == nil || submission.Completion == nil {
			return fmt.Errorf("checkpoint source requires its exact admitted reservation and completion")
		}
		if result.Source.Compatibility.Work != submission.Reservation.Logical.Work {
			return fmt.Errorf("checkpoint source work contradicts the admitted submission")
		}
		reservation, err := executionattempt.EncodeAttempt(*submission.Reservation)
		if err != nil {
			return err
		}
		completion, err := executionledger.EncodeCompletion(*submission.Completion)
		if err != nil {
			return err
		}
		switch result.Source.Boundary {
		case executioncheckpoint.BeforeUnit:
			if submission.Reservation.Parent == nil ||
				*submission.Reservation.Parent != (executionattempt.Parent{
					ExecutionID: result.Source.ExecutionID, Relationship: "retry",
				}) || submission.Reservation.ExecutionID == result.Source.ExecutionID {
				return fmt.Errorf("before restore does not retain its exact original parent and fresh child")
			}
		case executioncheckpoint.FinalizedUnit:
			if submission.Reservation.ExecutionID != result.Source.ExecutionID ||
				artifactmanifest.Hash(reservation) != result.Source.ReservationDigest ||
				artifactmanifest.Hash(completion) != result.Source.CompletionDigest ||
				submission.Completion.ManifestDigest != result.Source.ManifestDigest {
				return fmt.Errorf("finalized restore differs from its exact original checkpoint source")
			}
		default:
			return fmt.Errorf("operation error result has an unsupported checkpoint source boundary")
		}
	}
	if result.Submission == nil && (result.BeforeDigest != "" || result.FinalizedDigest != "") {
		return fmt.Errorf("checkpoint references require their admitted submission")
	}
	return nil
}
