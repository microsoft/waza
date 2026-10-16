package nativetask

import (
	"bytes"
	"context"
	"fmt"

	"github.com/microsoft/waza/internal/assurance"
)

const (
	maxPrefixEvents         = 65536
	maxPrefixAdmissionBytes = 16 << 20
)

// InspectPrefix validates supplied bytes without filesystem access. Nil inputs
// are unsupplied; nonnil empty inputs are supplied empty tapes. Callers must
// retain that presence distinction separately from Records and Pending.
// Torn/orphan data is invalid, and errors never return a partial prefix.
// This does not establish execution, persistence acknowledgement or authority.
func InspectPrefix(ctx context.Context, events, payload []byte, admissions []*Admitted) (*Prefix, error) {
	if err := prefixInputs(ctx, events, payload, admissions); err != nil {
		return nil, err
	}
	result, err := verifyPrefix(ctx, events, payload, admissions)
	if err != nil {
		return nil, fmt.Errorf("inspecting native prefix: %w", err)
	}
	count := len(result.Records)
	if result.Pending != nil {
		count++
	}
	if count != len(admissions) {
		return nil, fmt.Errorf("native prefix admissions must exactly cover supplied starts")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func prefixInputs(ctx context.Context, events, payload []byte, admissions []*Admitted) error {
	if ctx == nil {
		return fmt.Errorf("native prefix inspection requires a context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, input := range [][]byte{events, payload} {
		if len(input) > assurance.MaxLabelBytes {
			return fmt.Errorf("native prefix input exceeds 1 MiB")
		}
		if bytes.Count(input, []byte{'\n'}) > maxPrefixEvents {
			return fmt.Errorf("native prefix exceeds line limit")
		}
	}
	if len(admissions) > maxPrefixEvents || len(admissions) > bytes.Count(events, []byte{'\n'}) {
		return fmt.Errorf("native prefix exceeds admission count limit")
	}
	remaining := maxPrefixAdmissionBytes
	for _, admitted := range admissions {
		if err := ctx.Err(); err != nil {
			return err
		}
		if admitted == nil || admitted.prepared == nil {
			return fmt.Errorf("native prefix requires independently admitted inputs")
		}
		size := len(admitted.prepared.data)
		if size > assurance.MaxLabelBytes || size > remaining {
			return fmt.Errorf("native prefix exceeds retained admission byte limit")
		}
		remaining -= size
	}
	return ctx.Err()
}
