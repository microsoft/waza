package runnerunit

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sync"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const MaxSubmissionResultBytes = artifactmanifest.MaxArtifactBytes + 4*executionattempt.MaxSidecarBytes + runnerterminal.MaxEnvelopeBytes

var submissionResultSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	return compileSubmissionSchema("execution-submission-result-1.0.schema.json")
})

// ParseSubmissionResult validates original wire types and declared cross-field
// context. It neither reads a provider nor grants ownership or disclosure rights.
func ParseSubmissionResult(data []byte) (*Submission, error) {
	if len(data) > MaxSubmissionResultBytes {
		return nil, fmt.Errorf("submission result exceeds %d bytes", MaxSubmissionResultBytes)
	}
	doc, err := jsonutil.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parsing submission result: %w", err)
	}
	schema, err := submissionResultSchema()
	if err != nil {
		return nil, err
	}
	if err := schema.Validate(doc); err != nil {
		return nil, fmt.Errorf("submission result schema: %w", err)
	}
	var result Submission
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decoding submission result: %w", err)
	}
	if err := validateSubmissionResult(result); err != nil {
		return nil, err
	}
	return &result, nil
}

func EncodeSubmissionResult(result Submission) ([]byte, error) {
	if result.Reservation != nil {
		if _, err := executionattempt.EncodeAttempt(*result.Reservation); err != nil {
			return nil, err
		}
	}
	if result.Decision != nil {
		if _, err := executionattempt.EncodeDecision(*result.Decision); err != nil {
			return nil, err
		}
	}
	if result.Terminal != nil {
		var binding *runnerterminal.Binding
		if result.Reservation != nil {
			binding = result.Reservation.Binding.TerminalBinding()
		}
		if _, err := runnerterminal.Encode(*result.Terminal, binding); err != nil {
			return nil, err
		}
	}
	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encoding submission result: %w", err)
	}
	if _, err := ParseSubmissionResult(data); err != nil {
		return nil, err
	}
	return data, nil
}

func validateSubmissionResult(result Submission) error {
	var reservationBytes []byte
	if result.Reservation != nil {
		var err error
		reservationBytes, err = executionattempt.EncodeAttempt(*result.Reservation)
		if err != nil {
			return err
		}
		if result.Caller.Run != result.Reservation.Logical.Run || result.Caller.Trial != result.Reservation.Logical.Work {
			return fmt.Errorf("submission caller and original reservation have different logical work")
		}
		if result.Decision == nil && *result.Caller != result.Reservation.Binding.Identity {
			return fmt.Errorf("submission caller contradicts original reservation before decision")
		}
	}
	if result.Decision != nil {
		if _, err := executionattempt.EncodeDecision(*result.Decision); err != nil {
			return err
		}
		if result.Decision.Binding.Identity != *result.Caller || result.Decision.Logical != result.Reservation.Logical {
			return fmt.Errorf("submission decision contradicts caller or logical work")
		}
		switch result.Decision.Policy {
		case "distinct":
			if result.Decision.ExecutionID != result.Reservation.ExecutionID ||
				!reflect.DeepEqual(result.Decision.Binding, result.Reservation.Binding) {
				return fmt.Errorf("distinct decision contradicts original owned reservation")
			}
		case "reuse":
			if err := executionledger.ValidateReuseBinding(*result.Decision, *result.Reservation); err != nil {
				return err
			}
		}
	}
	if result.Completion != nil {
		if _, err := executionledger.EncodeCompletion(*result.Completion); err != nil {
			return err
		}
		if result.Completion.ExecutionID != result.Reservation.ExecutionID ||
			result.Completion.ReservationDigest != artifactmanifest.Hash(reservationBytes) {
			return fmt.Errorf("completion contradicts canonical original reservation")
		}
		if result.Status == "reused" && result.Decision.Source.ManifestDigest != result.Completion.ManifestDigest {
			return fmt.Errorf("reuse decision contradicts original manifest reference")
		}
	}
	if result.Terminal != nil {
		var binding *runnerterminal.Binding
		if result.Reservation != nil {
			binding = result.Reservation.Binding.TerminalBinding()
		}
		if _, err := runnerterminal.Encode(*result.Terminal, binding); err != nil {
			return err
		}
		if (result.Status == "completed" || result.Status == "reused" || result.Status == "rejected") &&
			result.ExitCode != result.Terminal.ExitCode {
			return fmt.Errorf("submission exit code contradicts original terminal")
		}
		if result.Status != "reused" && result.EngineCalls != result.Terminal.EngineCalls {
			return fmt.Errorf("submission new calls contradict original terminal")
		}
	}
	if len(result.Report) != 0 {
		source := &executionledger.CompletedExecution{
			Reservation: executionledger.Reservation{Attempt: *result.Reservation},
		}
		if err := validateSourceReport(result.Report, source, result.Terminal); err != nil {
			return err
		}
	}
	return nil
}
