package models

import "encoding/json"

// InfrastructureDiagnostic contains only allowlisted, bounded public metadata.
type InfrastructureDiagnostic struct {
	Code              InfrastructureDiagnosticCode `json:"code"`
	Limit             int64                        `json:"limit,omitempty"`
	AdditionalFailure bool                         `json:"additional_failure,omitempty"`
}

type InfrastructureDiagnosticCode string

const (
	InfrastructureError              InfrastructureDiagnosticCode = "infrastructure_error"
	CaptureFailed                    InfrastructureDiagnosticCode = "workspace_capture_failed"
	CaptureFileCount                 InfrastructureDiagnosticCode = "workspace_capture_file_count"
	CaptureEntryCount                InfrastructureDiagnosticCode = "workspace_capture_entry_count"
	CaptureFileBytes                 InfrastructureDiagnosticCode = "workspace_capture_file_bytes"
	CaptureTotalBytes                InfrastructureDiagnosticCode = "workspace_capture_total_bytes"
	MaxInfrastructureDiagnosticLimit                              = int64(1 << 40)
)

// Safe returns a copy with unknown codes and invalid limits withheld.
func (d InfrastructureDiagnostic) Safe() InfrastructureDiagnostic {
	switch d.Code {
	case CaptureFileCount, CaptureEntryCount, CaptureFileBytes, CaptureTotalBytes:
		if d.Limit <= 0 || d.Limit > MaxInfrastructureDiagnosticLimit {
			d.Limit = 0
		}
	case CaptureFailed, InfrastructureError:
		d.Limit = 0
	default:
		d.Code, d.Limit = InfrastructureError, 0
	}
	return d
}

func (d InfrastructureDiagnostic) MarshalJSON() ([]byte, error) {
	type wire InfrastructureDiagnostic
	return json.Marshal(wire(d.Safe()))
}
