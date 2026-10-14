package graders

import (
	"context"
	"errors"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/utils"
	"github.com/stretchr/testify/require"
)

func TestPromptGraderRecordsUsageEvenOnFailure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response bool
		fail     bool
	}{
		{"success", true, false}, {"error response", true, true}, {"no response", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var recorded []models.SessionDigest
			gCtx := &Context{
				Executor: &fakePromptExecutor{execute: func(*execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
					var resp *execution.ExecutionResponse
					if tc.response {
						resp = &execution.ExecutionResponse{SessionID: "judge", Usage: &models.UsageStats{AICredits: utils.Ptr(1.25)}}
					}
					if tc.fail {
						return resp, errors.New("judge failed")
					}
					return resp, nil
				}},
				RecordUsage: func(digest models.SessionDigest) { recorded = append(recorded, digest) },
			}
			_, err := executePromptGrader(context.Background(), gCtx, &execution.ExecutionRequest{})
			if tc.fail {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			if tc.response {
				require.Len(t, recorded, 1)
				require.Equal(t, "judge", recorded[0].SessionID)
				require.Equal(t, 1.25, *recorded[0].Usage.AICredits)
			} else {
				require.Empty(t, recorded)
			}
		})
	}
}
