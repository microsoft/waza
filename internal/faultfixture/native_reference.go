package faultfixture

import (
	"fmt"
	"strconv"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
)

// NativeReference verifies an actual captured array ordinal. The native caller
// must supply its own association; this helper never joins by name or matcher.
// Successful verification is not a grader, enforcement or integration verdict.
func NativeReference(manifest *models.EvidenceManifest, origin models.EvidenceOrigin, artifact string, ordinal int, content []byte, requireComplete, requireUnredacted bool) (models.EvidenceReference, error) {
	if ordinal < 0 || artifact != "command-invocations" && artifact != "tool-events" {
		return models.EvidenceReference{}, fmt.Errorf("native invocation receipt requires a captured array ordinal and supported artifact")
	}
	value, err := jsonutil.Parse(content)
	if err != nil {
		return models.EvidenceReference{}, fmt.Errorf("reading native invocation tape: %w", err)
	}
	tape, ok := value.([]any)
	if !ok {
		return models.EvidenceReference{}, fmt.Errorf("native invocation tape must be an array")
	}
	if ordinal >= len(tape) {
		return models.EvidenceReference{}, fmt.Errorf("native invocation ordinal is not captured")
	}
	if _, ok := tape[ordinal].(map[string]any); !ok {
		return models.EvidenceReference{}, fmt.Errorf("native invocation receipt requires a captured object")
	}
	reference, err := evidence.Reference(manifest, artifact)
	if err != nil {
		return models.EvidenceReference{}, err
	}
	reference.Origin = origin
	reference.Pointer = "/" + strconv.Itoa(ordinal)
	if err := evidence.VerifyContent(manifest, reference, content, requireComplete, requireUnredacted); err != nil {
		return models.EvidenceReference{}, err
	}
	return reference, nil
}
