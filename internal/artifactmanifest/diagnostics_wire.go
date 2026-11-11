package artifactmanifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/microsoft/waza/internal/jsonutil"
)

type diagnosticPrevious struct {
	Revision uint32 `json:"revision"`
	Digest   string `json:"digest"`
}

type diagnosticHeader struct {
	Kind              string              `json:"kind"`
	SchemaVersion     string              `json:"schema_version"`
	ExecutionID       string              `json:"execution_id"`
	ReservationDigest string              `json:"reservation_digest"`
	InvocationID      string              `json:"invocation_id"`
	Revision          uint32              `json:"revision"`
	Previous          *diagnosticPrevious `json:"previous"`
}

type diagnosticWire struct {
	diagnosticHeader
	Payload json.RawMessage `json:"payload"`
}

func validDiagnosticScope(scope Scope) bool {
	execution, executionOK := strings.CutPrefix(scope.ExecutionID, "exec-")
	invocation, invocationOK := strings.CutPrefix(scope.InvocationID, "inv-")
	return executionOK && invocationOK && validDigest(execution) &&
		validDigest(invocation) && validDigest(scope.ReservationDigest)
}

func diagnosticKey(scope Scope, revision uint32) string {
	return fmt.Sprintf("%s-%s-%s-%s-%04d", DiagnosticsNamespaceV1,
		scope.ExecutionID, scope.ReservationDigest, scope.InvocationID, revision)
}

// Raw payload bytes are owned and canonically encoded by the lifecycle owner.
// Encoding the outer header separately avoids RawMessage compaction/HTML escaping.
func encodeDiagnostic(scope Scope, revision uint32, previous *Cursor, payload json.RawMessage) ([]byte, error) {
	if !validDiagnosticScope(scope) || revision > MaxDiagnosticRevision || len(payload) > MaxDiagnosticBytes {
		return nil, ErrInvalid
	}
	if revision == 0 && previous != nil || revision != 0 &&
		(previous == nil || previous.Scope != scope || previous.Revision != revision-1 || !validDigest(previous.Digest)) {
		return nil, fmt.Errorf("%w: diagnostic previous cursor", ErrInvalid)
	}
	doc, err := jsonutil.Parse(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: diagnostic payload: %w", ErrInvalid, err)
	}
	if _, ok := doc.(map[string]any); !ok || !bytes.Equal(payload, bytes.TrimSpace(payload)) {
		return nil, fmt.Errorf("%w: diagnostic payload must be an unpadded JSON object", ErrInvalid)
	}
	header := diagnosticHeader{
		Kind: "waza.execution-diagnostic", SchemaVersion: "1.0", ExecutionID: scope.ExecutionID,
		ReservationDigest: scope.ReservationDigest, InvocationID: scope.InvocationID, Revision: revision,
	}
	if previous != nil {
		header.Previous = &diagnosticPrevious{Revision: previous.Revision, Digest: previous.Digest}
	}
	data, err := json.Marshal(header)
	if err != nil {
		return nil, fmt.Errorf("encoding diagnostic header: %w", err)
	}
	data = append(data[:len(data)-1], []byte(`,"payload":`)...)
	data = append(data, payload...)
	data = append(data, '}')
	if len(data) > MaxDiagnosticBytes {
		return nil, fmt.Errorf("%w: diagnostic envelope exceeds %d bytes", ErrInvalid, MaxDiagnosticBytes)
	}
	return data, nil
}

func parseDiagnostic(data []byte, scope Scope, revision uint32) (Record, error) {
	fail := func(err error) (Record, error) {
		return Record{}, fmt.Errorf("%w: diagnostic slot: %w", ErrCorrupt, err)
	}
	if len(data) > MaxDiagnosticBytes {
		return fail(ErrInvalid)
	}
	doc, err := jsonutil.Parse(data)
	if err != nil {
		return fail(err)
	}
	object, ok := doc.(map[string]any)
	if !ok || len(object) != 8 {
		return fail(ErrInvalid)
	}
	if !diagnosticRevisionToken(object["revision"], revision) {
		return fail(ErrInvalid)
	}
	if revision == 0 {
		if object["previous"] != nil {
			return fail(ErrInvalid)
		}
	} else {
		previous, ok := object["previous"].(map[string]any)
		if !ok || len(previous) != 2 || !diagnosticRevisionToken(previous["revision"], revision-1) {
			return fail(ErrInvalid)
		}
	}
	var wire diagnosticWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return fail(err)
	}
	if wire.Kind != "waza.execution-diagnostic" || wire.SchemaVersion != "1.0" ||
		wire.ExecutionID != scope.ExecutionID || wire.ReservationDigest != scope.ReservationDigest ||
		wire.InvocationID != scope.InvocationID {
		return fail(ErrConflict)
	}
	var previous *Cursor
	if wire.Previous != nil {
		previous = &Cursor{Scope: scope, Revision: wire.Previous.Revision, Digest: wire.Previous.Digest}
	}
	canonical, err := encodeDiagnostic(scope, revision, previous, wire.Payload)
	if err != nil {
		return fail(err)
	}
	if !bytes.Equal(data, canonical) {
		return fail(fmt.Errorf("%w: noncanonical diagnostic envelope", ErrInvalid))
	}
	return Record{
		Cursor: Cursor{Scope: scope, Revision: revision, Digest: Hash(data)}, Bytes: bytes.Clone(data),
		Payload: bytes.Clone(wire.Payload), Previous: previous, Verification: DiagnosticSlotOnly,
	}, nil
}

func diagnosticRevisionToken(value any, expected uint32) bool {
	number, ok := value.(json.Number)
	return ok && expected <= MaxDiagnosticRevision && number.String() == strconv.FormatUint(uint64(expected), 10)
}
