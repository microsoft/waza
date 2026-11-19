package runnerunit

import (
	"bytes"
	"fmt"

	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/runnerterminal"
)

func submissionSnapshot(ledger *executionledger.Ledger, creator executionledger.Creator, report Report, raw, terminal []byte,
	binding *runnerterminal.Binding) (SubmissionSnapshot, error) {
	reservation, err := ledger.Reservation(creator)
	if err != nil {
		return SubmissionSnapshot{}, err
	}
	envelope, err := runnerterminal.Validate(terminal, binding)
	if err != nil {
		return SubmissionSnapshot{}, err
	}
	detached, err := cloneSubmissionReport(report, raw)
	if err != nil {
		return SubmissionSnapshot{}, err
	}
	return SubmissionSnapshot{Reservation: *reservation, Terminal: *envelope, Report: bytes.Clone(raw), ObservedReport: detached}, nil
}

func validatePublicationTerminal(original, projected runnerterminal.Envelope, binding *runnerterminal.Binding) (*runnerterminal.Envelope, error) {
	originalBytes, err := runnerterminal.Encode(original, binding)
	if err != nil {
		return nil, err
	}
	projectedBytes, err := runnerterminal.Encode(projected, binding)
	if err != nil {
		return nil, err
	}
	copy, err := runnerterminal.Validate(projectedBytes, binding)
	if err != nil {
		return nil, err
	}
	if len(copy.Diagnostics) == len(original.Diagnostics) {
		for i := range copy.Diagnostics {
			copy.Diagnostics[i].Message = original.Diagnostics[i].Message
		}
	}
	if copy.Observation != nil && original.Observation != nil {
		copy.Observation.ErrorMsg = original.Observation.ErrorMsg
	}
	normalized, err := runnerterminal.Encode(*copy, binding)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(normalized, originalBytes) {
		return nil, fmt.Errorf("publication policy changed original native outcome, context or evidence")
	}
	return runnerterminal.Validate(projectedBytes, binding)
}
