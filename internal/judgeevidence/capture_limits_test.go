package judgeevidence

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestCaptureRejectsUnboundedOrUnsafeFileMetadataBeforeCopying(t *testing.T) {
	for _, test := range []struct {
		name    string
		capture *models.JudgeFileCapture
	}{
		{"diagnostic content", &models.JudgeFileCapture{Diagnostics: []models.EvidenceDiagnostic{{Code: "file_capture_failed", Message: "PRIVATE_DIAGNOSTIC_CANARY"}}}},
		{"unknown diagnostic", &models.JudgeFileCapture{Diagnostics: []models.EvidenceDiagnostic{{Code: "PRIVATE_DIAGNOSTIC_CANARY"}}}},
		{"diagnostic overflow", &models.JudgeFileCapture{Diagnostics: make([]models.EvidenceDiagnostic, 65)}},
		{"negative redactions", &models.JudgeFileCapture{Redaction: models.EvidenceRedaction{MatchCount: -1}}},
		{"overflow redactions", &models.JudgeFileCapture{Redaction: models.EvidenceRedaction{MatchCount: int(^uint(0) >> 1)}}},
		{"rule content", &models.JudgeFileCapture{Redaction: models.EvidenceRedaction{AppliedRules: []string{"PRIVATE_DIAGNOSTIC_CANARY"}}}},
		{"rule overflow", &models.JudgeFileCapture{Redaction: models.EvidenceRedaction{AppliedRules: make([]string, 65)}}},
		{"file overflow", &models.JudgeFileCapture{Files: make([]models.JudgeEvidenceFile, 65)}},
		{"oversized file", &models.JudgeFileCapture{Files: []models.JudgeEvidenceFile{{Path: "report.txt", Content: strings.Repeat("x", models.JudgeEvidenceMaxFileBytes+1)}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := observedInput()
			input.Selection.Files = []string{"report.txt"}
			input.Files = test.capture
			bundle := Capture(input)
			require.Error(t, Verify(bundle, input.Origin))
			encoded, err := json.Marshal(bundle)
			require.NoError(t, err)
			require.LessOrEqual(t, len(encoded), models.JudgeEvidenceMaxBytes)
			require.NotContains(t, string(encoded), "PRIVATE_DIAGNOSTIC_CANARY")
			require.NotContains(t, string(encoded), strings.Repeat("x", models.JudgeEvidenceMaxFileBytes+1))
			require.NotEmpty(t, bundle.Diagnostics)
		})
	}
}
