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
	// Covers Admission's fixed JSON field names, punctuation and integer scalars.
	prefixBindingOverhead = 1024
)

// InspectPrefix validates supplied bytes without filesystem access. Nil inputs
// are unsupplied; nonnil empty inputs are supplied empty tapes. Callers must
// retain that presence distinction separately from Records and Pending.
// Torn/orphan data is invalid, and errors never return a partial prefix.
// This does not establish execution, persistence acknowledgement or authority.
func InspectPrefix(ctx context.Context, events, payload []byte, admissions []*Admitted) (*Prefix, error) {
	return inspectPrefix(ctx, events, payload, admissions, false)
}

// InspectCollectionPrefix selects public 2.0 preparation and stream bounds.
// It does not widen InspectPrefix's private 1.0 admission limits.
func InspectCollectionPrefix(ctx context.Context, events, payload []byte, admissions []*Admitted) (*Prefix, error) {
	return inspectPrefix(ctx, events, payload, admissions, true)
}

func inspectPrefix(ctx context.Context, events, payload []byte, admissions []*Admitted, collection bool) (*Prefix, error) {
	if err := prefixInputsProfile(ctx, events, payload, admissions, collection); err != nil {
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
	return prefixInputsProfile(ctx, events, payload, admissions, false)
}

func prefixInputsProfile(ctx context.Context, events, payload []byte, admissions []*Admitted, collection bool) error {
	if ctx == nil {
		return fmt.Errorf("native prefix inspection requires a context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	streamLimit, admissionLimit := assurance.MaxLabelBytes, maxPrefixAdmissionBytes
	if collection {
		streamLimit, admissionLimit = CollectionStreamBytes, collectionEncodedBytes
	}
	for _, input := range [][]byte{events, payload} {
		if len(input) > streamLimit {
			if collection {
				return fmt.Errorf("native collection prefix exceeds 16 MiB stream limit")
			}
			return fmt.Errorf("native prefix input exceeds 1 MiB")
		}
		if bytes.Count(input, []byte{'\n'}) > maxPrefixEvents {
			return fmt.Errorf("native prefix exceeds line limit")
		}
	}
	if len(admissions) > maxPrefixEvents || len(admissions) > bytes.Count(events, []byte{'\n'}) {
		return fmt.Errorf("native prefix exceeds admission count limit")
	}
	remaining := admissionLimit
	for _, admitted := range admissions {
		if err := ctx.Err(); err != nil {
			return err
		}
		if collection {
			remaining = admissionLimit
		}
		var err error
		remaining, err = prefixAdmissionRemaining(admitted, remaining, collection)
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

func prefixAdmissionRemaining(admitted *Admitted, remaining int, collection bool) (int, error) {
	if admitted == nil || admitted.prepared == nil {
		return 0, fmt.Errorf("native prefix requires independently admitted inputs")
	}
	preparationLimit, identityLimit := assurance.MaxLabelBytes, assurance.MaxLabelBytes
	if collection {
		preparationLimit, identityLimit = collectionEncodedBytes, CollectionStreamBytes
	}
	size := len(admitted.prepared.data)
	if size > preparationLimit || size > remaining {
		return 0, fmt.Errorf("native prefix exceeds retained admission byte limit")
	}
	if collection {
		data, err := admitted.prepared.view()
		if err != nil {
			return 0, err
		}
		if data.CollectionVersion != "2.0" {
			return 0, fmt.Errorf("native collection prefix requires public 2.0 preparation")
		}
	}
	// Public descriptors have their own byte limit; their bindings are
	// separately bounded here and by the actual event stream.
	if !collection {
		remaining -= size
	}
	if remaining < prefixBindingOverhead {
		return 0, fmt.Errorf("native prefix exceeds retained admission byte limit")
	}
	remaining -= prefixBindingOverhead
	binding := admitted.binding
	for _, text := range []string{
		binding.CollectionID, binding.Key.ClusterID, binding.Key.TaskID, binding.Key.EvalID, string(binding.Key.Arm),
		binding.PolicyDigest.SHA256, binding.PolicyDigest.Encoding,
		binding.ProfileDigest.SHA256, binding.ProfileDigest.Encoding,
		binding.PlanDigest.SHA256, binding.PlanDigest.Encoding,
		binding.RequestDigest.SHA256, binding.RequestDigest.Encoding,
	} {
		// Bound JSON escaping before multiplication or seal serialization.
		if len(text) > identityLimit || len(text) > remaining/6 {
			return 0, fmt.Errorf("native prefix exceeds retained admission identity or escaped byte limit")
		}
		remaining -= 6 * len(text)
	}
	return remaining, nil
}
