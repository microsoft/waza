// Package faultfixture validates fault response source before normalization.
// It does not enable public sequence execution or provide runtime evidence.
package faultfixture

import "github.com/microsoft/waza/internal/models"

type Format = models.FaultSourceFormat

const (
	JSON = models.FaultSourceJSON
	YAML = models.FaultSourceYAML
)

// ValidateCommandResponse checks a CLI matcher and any finite source sequence.
// Sequence eligibility requires the explicit enclosing scenario version.
func ValidateCommandResponse(data []byte, format Format, enclosingVersion string) error {
	return models.ValidateCommandFaultResponse(data, format, enclosingVersion)
}

// ValidateMCPResponse checks an MCP matcher and any finite source sequence.
// Callers must run this before serialization can erase zero/null field presence.
func ValidateMCPResponse(data []byte, format Format, enclosingVersion string) error {
	return models.ValidateMCPFaultResponse(data, format, enclosingVersion)
}

type object = models.FaultSourceObject

func decodeSource(data []byte, format Format) (object, []string, error) {
	return models.DecodeFaultSource(data, format)
}

func decodeObject(data []byte) (object, []string, error) {
	return models.DecodeFaultObject(data)
}
