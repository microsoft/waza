package runnercontract

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

func boundedText(text string, limit int) string {
	text = strings.ToValidUTF8(text, "\uFFFD")
	if len(text) <= limit {
		return text
	}
	return strings.ToValidUTF8(text[:limit], "") + " [truncated]"
}

// boundedTerminal preserves full evidence when possible. If it cannot fit, keep
// bounded diagnostics and a digest of the omitted observation, never partial
// evidence masquerading as a complete observation.
func boundedTerminal(result Result) Result {
	if ValidateResult(&result) == nil {
		return result
	}
	original := result
	result.Diagnostics = nil
	// Retain the first execution evidence and the last delivery/validation cause.
	for i, diagnostic := range original.Diagnostics {
		if i >= 15 && i < len(original.Diagnostics)-2 {
			continue
		}
		if diagnostic.Code == "" {
			diagnostic.Code = "original_diagnostic"
		}
		if diagnostic.Message == "" {
			diagnostic.Message = "empty original diagnostic"
		}
		limit := 2048
		if diagnostic.Code == "publication_failed" {
			// Retain complete filesystem paths for locatable publication residue.
			limit = 16 << 10
		}
		result.Diagnostics = append(result.Diagnostics, Diagnostic{
			Code: boundedText(diagnostic.Code, 128), Message: boundedText(diagnostic.Message, limit),
		})
	}
	if ValidateResult(&result) == nil {
		return result
	}
	if original.Observation != nil {
		data, err := json.Marshal(original.Observation)
		message := fmt.Sprintf("observation omitted; encoded_bytes=%d sha256=%x", len(data), sha256.Sum256(data))
		if err != nil {
			message = "observation omitted: " + boundedText(err.Error(), 2048)
		}
		result.Diagnostics = append([]Diagnostic{{Code: "observation_omitted", Message: message}}, result.Diagnostics...)
	}
	result.Observation = nil
	if ValidateResult(&result) == nil {
		return result
	}
	// Invalid/oversized lineage or negotiation cannot be represented as valid
	// selection evidence. Preserve the observed cause without inventing identity.
	fallback := baseResult()
	fallback.Diagnostics = result.Diagnostics
	fallback.fail(InfrastructureFailure, "terminalization", "evidence_omitted", "invalid or oversized terminal metadata omitted")
	return fallback
}
